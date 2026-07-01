package catalog

// prod_estate.go — pd-MIG-CUTOVER-F0-03 (EPIC-AWS-TO-DO-MIGRATION).
//
// The CANONICAL FULL-PROD-ESTATE source of truth. Where full_estate.go is the
// representative "one of each platform-service + one of each managed peer"
// dry-run topology, and do_baseline.go is the narrow deploy-first DO foundation,
// THIS file models the WHOLE passo.build PRODUCTION estate — every real resource
// class that exists in prod today — as one canonical abstract topology, so the
// AWS->DigitalOcean cutover can be translated resource-for-resource and the
// residual bespoke gaps (components that cannot descend to DO) can be enumerated
// exactly.
//
// It is the single abstract source the F0 cutover renders BOTH ways:
//
//   - ProdEstateInput(aws, …)          — the SOURCE estate, everything as it runs
//     on AWS today (incl. the AWS-only components: SES + the 3 Amplify frontends).
//   - ProdEstateInput(digitalocean, …) — the TARGET estate, the same topology
//     MINUS the components that have no DigitalOcean catalog mapping (the bespoke
//     gaps, enumerated in docs/cutover/BESPOKE-GAPS.md and excluded here so the DO
//     render is a clean, plannable plan-only artefact rather than a hard error).
//
// The estate is assembled from the existing catalog components ONLY (no new
// resource types are invented here — new mappings for the gaps are F1 work). Every
// component below is one of the catalog's canonical Assemble* components and
// descends through AssembleHCL to concrete per-provider HCL.

// prodEstateClusterName is the canonical DOKS cluster the in-cluster components
// (CronJob, cert-manager, OTel collector, Vault-HA, LGTM, RabbitMQ operator)
// target on a DigitalOcean placement. On DO every platform scale-group descends
// to its own DOKS cluster; the in-cluster workloads reference the backend cluster.
const prodEstateClusterName = "backend"

// prodBuckets is the REAL production S3 bucket set (~18 buckets) the cutover must
// land. Each descends to aws_s3_bucket on AWS and digitalocean_spaces_bucket on
// DO (Spaces is S3-compatible, so the whole set migrates as a class). Names are
// DNS-safe canonical component names; the concrete provider bucket name is
// derived by the renderer (with the env-name suffix for global-namespace safety).
//
// The set is grouped by role. `versioned` turns on object-versioning for the
// buckets that carry mutable state (artifacts, state, assets); log/scan sinks are
// unversioned. `accessLogged` routes S3/Spaces server access logs to the
// alb-access-logs bucket (the audit sink) for the buckets that front user/edge
// traffic.
var prodBuckets = []struct {
	name         string
	versioned    bool
	accessLogged bool
	role         string
}{
	// ── build / deploy artifacts ──
	{"deploy-artifacts", true, false, "CI deploy artifacts (native binaries, bundles)"},
	{"api-artifact", true, false, "pyx-backend API build artifacts"},
	{"provisioning", true, false, "environment provisioning bundles / cloud-init"},
	{"provisioning-scripts", true, false, "per-cloud provisioning scripts"},
	{"lambda-artifacts", true, false, "pyx-lambda / serverless deployment packages"},
	// ── frontend static assets (the built SPA/marketing bundles behind the CDN) ──
	{"app-assets", true, true, "console app static assets (built bundle)"},
	{"pyx-frontend", true, true, "marketing/frontend static bundle"},
	{"vibe-assets", true, true, "vibe frontend static assets"},
	{"public-downloads", false, true, "public CLI/download artifacts"},
	// ── state / config ──
	{"terraform-state", true, false, "terraform remote state (versioned, locked)"},
	{"env-config", true, false, "per-environment config snapshots"},
	{"backup", true, false, "database / config backups"},
	// ── scan / audit / logs ──
	{"sast-scan", false, false, "SAST scan reports (Semgrep/SonarQube output)"},
	{"alb-access-logs", false, false, "ALB / edge access logs (the audit sink)"},
	{"cloudtrail-logs", false, false, "API audit trail logs"},
	{"observability-dumps", false, false, "observability aggregator dumps"},
	// ── app data ──
	{"user-uploads", true, true, "user-uploaded content (private)"},
	{"reports", true, false, "generated user reports"},
}

// prodAccessLogSink is the bucket every access-logged bucket routes server access
// logs to (the audit sink). It is itself a member of prodBuckets (unversioned).
const prodAccessLogSink = "alb-access-logs"

// prodObjectStorageComponents materialises prodBuckets as canonical
// object-storage components with S3->Spaces parity (versioning + AES256 SSE +
// lifecycle + optional access-logging) carried through the assembler.
func prodObjectStorageComponents() []AssembleComponent {
	out := make([]AssembleComponent, 0, len(prodBuckets))
	for _, b := range prodBuckets {
		os := &AssembleObjectStorage{
			Versioning: b.versioned,
			// S3->Spaces AES256 at-rest parity (KMS is AWS-only and deliberately not
			// used, so the bucket set migrates cleanly).
			SSE: &SSEConfig{Algorithm: SSEAlgoAES256},
			// Baseline lifecycle parity: expire scratch objects and abort dangling
			// multipart uploads (both S3-compatible; carried onto Spaces).
			Lifecycle: []LifecycleRule{
				{ID: "expire-tmp", Prefix: "tmp/", Enabled: true, ExpireDays: 30},
				{ID: "abort-mpu", Enabled: true, AbortIncompleteMultipartDays: 7},
			},
		}
		// Route edge/user-facing buckets' server access logs to the audit sink — but
		// never the sink onto itself (that is a plan-time cycle).
		if b.accessLogged && b.name != prodAccessLogSink {
			os.AccessLogs = &AccessLogConfig{TargetBucket: prodAccessLogSink}
		}
		out = append(out, AssembleComponent{
			Name: b.name, Type: "object-storage", ObjectStorage: os,
		})
	}
	return out
}

// prodManagedDatabaseComponents is the two production Managed Postgres clusters
// (the concrete migration targets), PG17, HA, encrypted. -> aws_db_instance /
// digitalocean_database_cluster.
func prodManagedDatabaseComponents() []AssembleComponent {
	const (
		pgVersion = "17"
		dbCPU     = 2
		dbRAM     = 4
	)
	return []AssembleComponent{
		{
			Name: "keycloak-db", Type: "managed-database",
			MDB: &AssembleMDB{
				Engine: DBEnginePostgres, Version: pgVersion,
				CPU: dbCPU, RAM: dbRAM, StorageGB: 100, HA: true, Encrypted: true,
			},
		},
		{
			Name: "pyx-main-db", Type: "managed-database",
			MDB: &AssembleMDB{
				Engine: DBEnginePostgres, Version: pgVersion,
				CPU: dbCPU, RAM: dbRAM, StorageGB: 80, HA: true, Encrypted: true,
			},
		},
	}
}

// prodPlatformPeerComponents is the shared platform layer that fronts / supports
// the 6 platform scale-groups: the edge L7 LB, container-registry, JIT key-value
// store, tracing, monitoring (LGTM), TLS, the nightly scheduled-trigger, the VPN
// reserved-ip, the secrets store (Vault-HA on DO), and the prod message queue.
// Every one has a DigitalOcean mapping (they descend on both providers).
func prodPlatformPeerComponents(provider string) []AssembleComponent {
	return []AssembleComponent{
		// ALB (L7) -> DO load-balancer + DOKS ingress, fronting the backend
		// scale-group, with the admin host-header rule gated to the VPN CIDR.
		//
		// Per-host DISTINCT-service routing (admin->sso, app->backend, mcp->mcp) is
		// carried on BOTH providers (GAP-4 resolved): on DigitalOcean the DOKS Ingress
		// backends a distinct service per host; on AWS the LB renderer now synthesises a
		// dedicated aws_lb_target_group per rule TargetName (+ ASG attachment), so the
		// AWS source render is fully plannable with per-host distinct targets — parity
		// with the DO Ingress. The admin-VPN source-IP gate is preserved on both.
		func() AssembleComponent {
			rules := []AssembleLBRoutingRule{
				{Priority: 100, HostHeaders: []string{"admin.passo.build"}, AdminVPNCIDRs: []string{"10.8.0.0/24"}, TargetName: "sso"},
				{Priority: 200, HostHeaders: []string{"app.passo.build"}, TargetName: "backend"},
				{Priority: 300, HostHeaders: []string{"mcp.passo.build"}, TargetName: "mcp"},
			}
			return AssembleComponent{
				Name: "edge-lb", Type: "load-balancer",
				LB: &AssembleLB{
					TargetKind: "scale-group", TargetName: "backend",
					HealthProtocol: "http", HealthCheckPort: 8080, HealthCheckPath: "/q/health",
					Listeners: []AssembleLBListener{
						{Port: 443, Protocol: "https", Rules: rules},
					},
				},
			}
		}(),
		// AWS ECR -> DO Container Registry (the per-service image store).
		{
			Name: "app-images", Type: "container-registry",
			ContainerRegistry: &AssembleContainerRegistry{Tier: "professional", GarbageCollection: true},
		},
		// DynamoDB JIT allowlist -> DO Managed Redis (private, VPC-wired).
		{
			Name: "jit-allowlist", Type: "key-value-store",
			KeyValueStore: &AssembleKeyValueStore{PartitionKey: "subject", MemoryGB: 1, HA: true},
		},
		// X-Ray -> Grafana Tempo + OTel collector on DOKS.
		{
			Name: "app-traces", Type: "tracing",
			Tracing: &AssembleTracing{SamplingRate: 0.2, ClusterName: prodEstateClusterName, RetentionHours: 168},
		},
		// CloudWatch + SNS -> the LGTM stack on DOKS (kube-prometheus-stack + Loki +
		// Grafana + Alertmanager). CloudWatch alarms -> PrometheusRule alerts.
		{
			Name: "app-monitoring", Type: "monitoring",
			Monitoring: &AssembleMonitoring{
				ClusterName: prodEstateClusterName,
				ScrapeTargets: []ScrapeTarget{
					{Name: "backend", MatchLabels: map[string]string{"app": "backend"}, Port: "metrics", Path: "/q/metrics"},
				},
				Alarms: []MetricAlarm{
					{Name: "backend-cpu-high", Namespace: "AWS/EC2", MetricName: "node_cpu_high_ratio",
						ComparisonOperator: "GreaterThanThreshold", Threshold: 0.8, EvaluationPeriods: 3, PeriodSeconds: 60, Severity: "warning"},
					{Name: "backend-5xx", Namespace: "AWS/ApplicationELB", MetricName: "http_server_requests_5xx_rate",
						ComparisonOperator: "GreaterThanThreshold", Threshold: 5, EvaluationPeriods: 5, PeriodSeconds: 60, Severity: "critical"},
				},
				TempoDatasourceName: "app-traces",
			},
		},
		// ACM -> cert-manager + Let's Encrypt (production) on DOKS.
		{
			Name: "app-tls", Type: "tls-certificate",
			TLSCertificate: &AssembleTLSCertificate{
				Domains: []string{"app.passo.build", "admin.passo.build", "mcp.passo.build"},
				Email:   "ops@passo.build", Production: true, ClusterName: prodEstateClusterName,
			},
		},
		// EventBridge cron -> DOKS CronJob (the nightly maintenance job).
		{
			Name: "nightly", Type: "scheduled-trigger",
			ScheduledTrigger: &AssembleScheduledTrigger{
				Schedule: "cron(0 3 * * ? *)", Image: "registry.passo.build/maint:latest",
				ClusterName: prodEstateClusterName,
			},
		},
		// Elastic IP -> DO Reserved IP (the VPN gateway's stable public endpoint).
		{
			Name: "vpn-endpoint", Type: "reserved-ip",
			ReservedIP: &AssembleReservedIP{},
		},
		// SQS/prod queue -> RabbitMQ Cluster Operator on DOKS (B1 operator pattern).
		{
			Name: "app-queue", Type: "managed-queue",
			Queue: &AssembleQueue{
				VisibilityTimeoutSeconds: 30, MaxReceiveCount: 5,
				ClusterName: prodEstateClusterName,
			},
		},
		// Secrets Manager -> native on AWS; auto-aliased to Vault-HA operator on DO
		// (pd-MIG-B4-SECRETS-VAULT-AUTOALIAS). ClusterName pins the backend DOKS cluster.
		//
		// RotationDays is left 0 (no native AWS rotation): AWS secret rotation needs a
		// bespoke rotation Lambda (var.rotation_lambda_arn, supplied out of band), which
		// is not part of the abstract topology — see BESPOKE-GAPS.md. On DO, Vault-HA
		// provides rotation natively via its own leases, so no lambda is involved.
		{
			Name: "app-secrets", Type: "secrets-manager",
			Secrets: &AssembleSecrets{Description: "passo.build app secrets"},
			VaultHA: &AssembleVaultHA{ClusterName: prodEstateClusterName},
		},
	}
}

// prodBespokeAWSOnlyComponents are the production components that have NO
// DigitalOcean catalog mapping today — the residual bespoke gaps. They appear in
// the AWS SOURCE estate (where they run natively) but are EXCLUDED from the DO
// TARGET estate (they would be a hard plan-time error). Each is enumerated with
// its proposed F1 target in docs/cutover/BESPOKE-GAPS.md.
//
//   - SES sending domain: AWS-only (email.go hard-errors on DO). F1-05: external
//     provider (e.g. SendGrid/Postmark) or keep SES cross-cloud.
//   - the 3 frontends (marketing / console / vibe): historically AWS Amplify
//     static hosting. There is NO DO static-site catalog component today. F1-01:
//     a new static-site component (Spaces static hosting + Cloudflare CDN). The
//     built bundles already live in object-storage (app-assets / pyx-frontend /
//     vibe-assets); the gap is the managed static-site HOSTING/CDN wrapper, which
//     Amplify provided and DO does not have a first-class equivalent for.
func prodBespokeAWSOnlyComponents() []AssembleComponent {
	return []AssembleComponent{
		// SES transactional-email sending domain (passo.build). AWS-only.
		{
			Name: "email-sender", Type: "email",
			Email: &AssembleEmail{Domain: "passo.build"},
		},
	}
}

// ProdEstateComponents returns the canonical full-prod estate for the given
// provider. On AWS it includes the AWS-only bespoke components (SES + frontends);
// on any non-AWS provider those are excluded (they have no mapping — see
// docs/cutover/BESPOKE-GAPS.md) so the render is clean and plannable.
//
// arch/os default to the environment-wide defaults when empty; kubernetesVersion
// pins the DOKS control-plane version for every node-pool on a DO placement.
func ProdEstateComponents(provider, arch, os, kubernetesVersion string) []AssembleComponent {
	// The compute substrate: the 6 platform services -> ASGs (AWS) / DOKS (DO).
	comps := PlatformScaleGroupComponents(arch, os, kubernetesVersion)
	// The two production Managed Postgres clusters.
	comps = append(comps, prodManagedDatabaseComponents()...)
	// The ~18 production buckets (S3 -> Spaces).
	comps = append(comps, prodObjectStorageComponents()...)
	// The shared platform peer layer (LB, registry, kv, tracing, monitoring, TLS,
	// cron, reserved-ip, queue, secrets/Vault-HA).
	comps = append(comps, prodPlatformPeerComponents(provider)...)
	// AWS-only bespoke components — only on AWS. On DO they are documented gaps and
	// deliberately excluded so the DO plan is clean.
	if lc(provider) == ProviderAWS {
		comps = append(comps, prodBespokeAWSOnlyComponents()...)
	}
	return comps
}

// ProdEstateInput is the canonical full-prod-estate AssembleInput for the given
// provider/region — the single abstract source the F0 cutover renders both ways.
func ProdEstateInput(provider, region, arch, os, kubernetesVersion string) AssembleInput {
	return AssembleInput{
		Name:       "passo-prod",
		Provider:   provider,
		Region:     region,
		Expose:     []int{443},
		Components: ProdEstateComponents(provider, arch, os, kubernetesVersion),
	}
}
