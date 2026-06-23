package catalog

// full_estate.go — pd-MIG-PLAN-DRYRUN-ESTATE (EPIC-AWS-TO-DO-MIGRATION).
//
// FullEstateInput assembles the WHOLE passo.build production estate as a single
// canonical abstract topology so the AWS->DigitalOcean migration can be PROVED
// to plan cleanly end-to-end (plan-only, no apply). It is the representative
// "everything at once" topology the dry-run milestone validates:
//
//   - the 5 platform services (SSO / VPN / observability / SAST / backend) as
//     canonical scale-groups of 1 (PlatformScaleGroupComponents -> DOKS node-pools)
//   - container-registry (AWS ECR -> DO Container Registry)
//   - key-value-store (DynamoDB JIT allowlist -> DO Managed Redis)
//   - object-storage with S3->Spaces parity (lifecycle + SSE + policy + access-logs)
//   - a layer-7 load-balancer fronting the backend scale-group (ALB L7 -> DO LB +
//     ingress), with the admin/VPN source-IP gate
//   - tracing (X-Ray -> Grafana Tempo + OTel collector on DOKS)
//   - tls-certificate (ACM -> cert-manager + Let's Encrypt on DOKS)
//   - a scheduled-trigger (EventBridge cron -> DOKS CronJob)
//   - a reserved-ip (Elastic IP -> DO Reserved IP, the VPN stable endpoint)
//   - a secrets-manager (mitigated to self-hosted Vault on DO, native on AWS)
//
// The same constructor targets any provider; the migration dry-run renders it
// for DigitalOcean and asserts `terraform init/validate` is green with no
// unsupported-component / ErrAutoscaleUnsupported / missing-render error.

// clusterName is the canonical DOKS cluster the in-cluster components
// (scheduled-trigger CronJob, cert-manager manifests, OTel collector) target.
// On DigitalOcean every platform scale-group descends to its own DOKS cluster;
// the in-cluster workloads reference the backend cluster by name.
const fullEstateClusterName = "backend"

// FullEstateComponents returns the representative full-estate topology as a
// slice of canonical AssembleComponents, ready to drop into an AssembleInput.
//
// arch/os default to the environment-wide defaults when empty; kubernetesVersion
// pins the DOKS control-plane version for every node-pool on a DigitalOcean
// placement (ignored elsewhere).
func FullEstateComponents(arch, os, kubernetesVersion string) []AssembleComponent {
	comps := PlatformScaleGroupComponents(arch, os, kubernetesVersion)

	comps = append(comps,
		// AWS ECR -> DO Container Registry (the per-service image store).
		AssembleComponent{
			Name: "app-images", Type: "container-registry",
			ContainerRegistry: &AssembleContainerRegistry{Tier: "professional", GarbageCollection: true},
		},
		// DynamoDB JIT allowlist -> DO Managed Redis (private, VPC-wired).
		AssembleComponent{
			Name: "jit-allowlist", Type: "key-value-store",
			KeyValueStore: &AssembleKeyValueStore{PartitionKey: "subject", MemoryGB: 1, HA: true},
		},
		// S3 -> Spaces with full feature parity carried through the assembler.
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
				AccessLogs:   &AccessLogConfig{TargetBucket: "audit-logs"},
			},
		},
		// ALB (L7) -> DO load-balancer + ingress, fronting the backend scale-group.
		// One default 443 listener plus an admin host-header rule gated to the VPN CIDR.
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
		// X-Ray -> Grafana Tempo + an OTel collector on DOKS.
		AssembleComponent{
			Name: "app-traces", Type: "tracing",
			Tracing: &AssembleTracing{SamplingRate: 0.2, ClusterName: fullEstateClusterName, RetentionHours: 168},
		},
		// ACM -> cert-manager + Let's Encrypt (production) on DOKS.
		AssembleComponent{
			Name: "app-tls", Type: "tls-certificate",
			TLSCertificate: &AssembleTLSCertificate{
				Domains: []string{"app.passo.build", "admin.passo.build"},
				Email:   "ops@passo.build", Production: true, ClusterName: fullEstateClusterName,
			},
		},
		// EventBridge cron -> DOKS CronJob (the nightly maintenance job).
		AssembleComponent{
			Name: "nightly", Type: "scheduled-trigger",
			ScheduledTrigger: &AssembleScheduledTrigger{
				Schedule: "cron(0 3 * * ? *)", Image: "registry.passo.build/maint:latest",
				ClusterName: fullEstateClusterName,
			},
		},
		// Elastic IP -> DO Reserved IP (the VPN gateway's stable public endpoint).
		AssembleComponent{
			Name: "vpn-endpoint", Type: "reserved-ip",
			ReservedIP: &AssembleReservedIP{},
		},
		// Secrets Manager -> native on AWS; mitigated to self-hosted Vault on DO.
		AssembleComponent{
			Name: "app-secrets", Type: "secrets-manager",
			Secrets: &AssembleSecrets{Description: "passo.build app secrets", RotationDays: 30},
		},
	)
	return comps
}

// FullEstateInput is the canonical full-estate AssembleInput for the given
// provider/region — the single source the migration dry-run renders.
func FullEstateInput(provider, region, arch, os, kubernetesVersion string) AssembleInput {
	return AssembleInput{
		Name:       "passo-estate",
		Provider:   provider,
		Region:     region,
		Expose:     []int{443},
		Components: FullEstateComponents(arch, os, kubernetesVersion),
	}
}
