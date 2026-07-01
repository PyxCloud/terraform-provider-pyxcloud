package catalog

// do_baseline.go — pd-MIG-CUTOVER-F0-01 (EPIC-AWS-TO-DO-MIGRATION).
//
// The DigitalOcean ACCOUNT BASELINE: the foundational target layer the rest of
// the AWS->DO cutover lands on. Where full_estate.go proves the WHOLE topology
// re-renders on DO (the dry-run validation milestone), this constructor is the
// narrower, deploy-first FOUNDATION — the substrate resources every later
// cutover phase depends on:
//
//   - the compute substrate: the 6 platform services as canonical scale-groups
//     of 1 -> DOKS kubernetes clusters (node-pools), sized from the catalog.
//   - TWO Managed Postgres clusters (PG17) as the concrete migration targets for
//     the two production databases:
//       * keycloak-db  — 100 GB (the SSO/Keycloak store)
//       * pyx-main-db  —  80 GB (the pyx-backend main store)
//     Both descend to digitalocean_database_cluster (engine=pg, version=17),
//     wired into the account VPC.
//   - object-storage (Spaces) — the object-storage baseline the ~18 bucket
//     targets land in (one representative Spaces bucket with the private-by-
//     default + versioning + lifecycle parity the migration carries).
//   - a DO Load Balancer (+ the DOKS ingress path) — the shared-ALB replacement
//     fronting the backend service.
//   - the VPC / network foundation + the account firewall: AssembleHCL
//     synthesises `digitalocean_vpc` + `digitalocean_firewall` from the estate
//     name automatically, so every baseline resource above is placed in the one
//     account network with no hand-wiring.
//
// GAP (noted, not hacked in): DigitalOcean *Projects* (digitalocean_project, the
// account-level resource-grouping envelope) have NO catalog component in the
// provider today. The baseline therefore covers the network foundation via the
// VPC (the real network boundary); the Project grouping is a follow-up component
// (there is no existing resource type to reuse, so it is left as a gap rather
// than invented here).

// DO account-baseline sizing constants — the two Managed Postgres migration
// targets. PG17 is pinned explicitly (the DO managed-cluster `version` is passed
// through verbatim by the renderer). Storage GB is the production allocation the
// cutover must land.
const (
	// doBaselinePGVersion pins PostgreSQL 17 for both managed clusters.
	doBaselinePGVersion = "17"
	// keycloakDBStorageGB / pyxMainDBStorageGB are the production allocations for
	// the two migration-target databases.
	keycloakDBStorageGB = 100 // SSO / Keycloak store
	pyxMainDBStorageGB  = 80  // pyx-backend main store

	// The two DB clusters are sized 2vCPU/4GiB — the smallest DO managed-postgres
	// class the catalog carries above the floor (db-s-2vcpu-4gb, fra1). Sizing is
	// requested CPU/RAM resolved by the catalog, never a hand-picked size token.
	doBaselineDBCPU = 2
	doBaselineDBRAM = 4
)

// DOBaselineComponents returns the DigitalOcean account-baseline as a slice of
// canonical AssembleComponents. It REUSES the existing catalog components (no new
// resource types are invented): the 6 platform scale-groups (-> DOKS), two
// managed-databases (-> digitalocean_database_cluster), one object-storage (->
// digitalocean_spaces_bucket) and one load-balancer (-> digitalocean_loadbalancer
// + DOKS ingress). The VPC + firewall are synthesised by AssembleHCL.
//
// arch/os default to the environment-wide defaults when empty; kubernetesVersion
// pins the DOKS control-plane version for every node-pool.
func DOBaselineComponents(arch, os, kubernetesVersion string) []AssembleComponent {
	// The compute substrate: the 6 platform services -> DOKS clusters, sized for
	// the services from the catalog (the same canonical scale-group-of-1 pattern
	// full_estate uses).
	comps := PlatformScaleGroupComponents(arch, os, kubernetesVersion)

	comps = append(comps,
		// keycloak-db: PG17, 100 GB, HA. -> digitalocean_database_cluster (pg 17).
		AssembleComponent{
			Name: "keycloak-db", Type: "managed-database",
			MDB: &AssembleMDB{
				Engine: DBEnginePostgres, Version: doBaselinePGVersion,
				CPU: doBaselineDBCPU, RAM: doBaselineDBRAM,
				StorageGB: keycloakDBStorageGB, HA: true, Encrypted: true,
			},
		},
		// pyx-main-db: PG17, 80 GB, HA. -> digitalocean_database_cluster (pg 17).
		AssembleComponent{
			Name: "pyx-main-db", Type: "managed-database",
			MDB: &AssembleMDB{
				Engine: DBEnginePostgres, Version: doBaselinePGVersion,
				CPU: doBaselineDBCPU, RAM: doBaselineDBRAM,
				StorageGB: pyxMainDBStorageGB, HA: true, Encrypted: true,
			},
		},
		// object-storage baseline (Spaces): one representative bucket carrying the
		// private-by-default + versioning + lifecycle parity the ~18 bucket targets
		// need. -> digitalocean_spaces_bucket (+ policy).
		AssembleComponent{
			Name: "assets", Type: "object-storage",
			ObjectStorage: &AssembleObjectStorage{
				Versioning: true,
				Lifecycle: []LifecycleRule{
					{ID: "expire-tmp", Prefix: "tmp/", Enabled: true, ExpireDays: 30},
					{ID: "abort-mpu", Enabled: true, AbortIncompleteMultipartDays: 7},
				},
				SSE:          &SSEConfig{Algorithm: SSEAlgoAES256},
				BucketPolicy: `{"Version":"2012-10-17","Statement":[]}`,
			},
		},
		// The shared-ALB replacement: a DO load-balancer fronting the backend
		// scale-group, with the DOKS L7 ingress path. -> digitalocean_loadbalancer
		// + kubernetes_manifest ingress.
		AssembleComponent{
			Name: "edge-lb", Type: "load-balancer",
			LB: &AssembleLB{
				TargetKind: "scale-group", TargetName: "backend",
				HealthProtocol: "http", HealthCheckPort: 8080, HealthCheckPath: "/q/health",
				Listeners: []AssembleLBListener{
					{Port: 443, Protocol: "https", Rules: []AssembleLBRoutingRule{
						{Priority: 100, HostHeaders: []string{"admin.passo.build"},
							AdminVPNCIDRs: []string{"10.8.0.0/24"}, TargetName: "sso"},
						{Priority: 200, HostHeaders: []string{"app.passo.build"}, TargetName: "backend"},
					}},
				},
			},
		},
	)
	return comps
}

// DOBaselineInput is the canonical DigitalOcean account-baseline AssembleInput —
// the single source the F0 cutover foundation renders. Provider is always
// digitalocean (this is the DO target layer); region/arch/os/kubernetesVersion
// are threaded so the render is a concrete, catalog-resolved plan.
func DOBaselineInput(region, arch, os, kubernetesVersion string) AssembleInput {
	return AssembleInput{
		Name:       "passo-do-baseline",
		Provider:   ProviderDigitalOcean,
		Region:     region,
		Expose:     []int{443},
		Components: DOBaselineComponents(arch, os, kubernetesVersion),
	}
}
