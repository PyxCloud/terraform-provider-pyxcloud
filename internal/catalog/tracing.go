package catalog

import (
	"context"
	"fmt"
	"strings"
)

// tracing is the abstract `tracing` component: "collect and store distributed
// traces for this environment". Like tls-certificate and scheduled-trigger it has
// NO sizing catalog — a tracing backend is region/cluster-scoped — so the only
// catalog lookup is the region (region_name + provider -> csp_region). The
// component therefore depends on the RegionCatalog only.
//
// Per-provider mapping (pd-MIG-TRACING-TEMPO-OTEL — replace X-Ray):
//
//   - AWS: aws_xray_group + aws_xray_sampling_rule — the managed tracing service
//     being migrated AWAY from. X-Ray ingests traces via the X-Ray daemon /
//     OTel-to-X-Ray exporter and stores them server-side; the group + sampling
//     rule are the Terraform-expressible control surface.
//   - DigitalOcean: Grafana Tempo (trace store) + an OpenTelemetry Collector
//     (ingest/export) deployed to a DOKS cluster. DO has no managed tracing
//     service, so the canonical, plan-time-expressible replacement is the CNCF
//     OTel-collector -> Tempo pipeline — exactly the X-Ray replacement this task
//     asks for. Tempo runs as a Deployment fronted by a Service (the OTLP +
//     query endpoints); the collector runs as a Deployment that exports to Tempo.
//
// The DO path emits kubernetes_manifest resources (a Deployment + Service for
// Tempo, a ConfigMap + Deployment for the collector), reusing the SAME
// kubernetes/DOKS-cluster data-source convention the cert-manager / scheduled-
// trigger DOKS paths already use — no new cluster-wiring vocabulary is forked.

// Canonical tracing type tokens. `tracing` is canonical; `distributed-tracing`,
// `tempo`, and `trace-collector` are accepted aliases (all name the same component).
const (
	TypeTracing            = "tracing"
	TypeDistributedTracing = "distributed-tracing"
	TypeTempo              = "tempo"
	TypeTraceCollector     = "trace-collector"
	TypeOTelTracing        = "otel-tracing"
)

// Default container images for the DO Tempo + OTel-collector pipeline. Pinned to
// stable tags so the rendered plan is deterministic; overridable on the spec.
const (
	defaultTempoImage     = "grafana/tempo:2.4.1"
	defaultOTelCollImage  = "otel/opentelemetry-collector-contrib:0.103.1"
	defaultTracingNS      = "observability"
	defaultOTLPGRPCPort   = 4317
	defaultOTLPHTTPPort   = 4318
	defaultTempoQueryPort = 3200
)

// TracingSpec is the abstract description of a tracing backend. Provider-neutral.
type TracingSpec struct {
	Name     string // component name, e.g. "app-traces"
	Region   string // abstract pyx region_name
	Provider string // aws | digitalocean | ...

	// SamplingRate is the fraction of requests traced (0 < rate <= 1). Empty/0
	// defaults to 0.1 (10%) — the sane default that bounds trace volume/cost. On
	// AWS this drives the X-Ray sampling rule's fixed_rate; on DO it configures the
	// OTel collector's probabilistic sampler.
	SamplingRate float64

	// ── Tempo + OTel collector (DigitalOcean) ──
	// ClusterName is the existing DOKS cluster the pipeline runs on. Required for DO;
	// ignored on AWS.
	ClusterName string
	// Namespace is the Kubernetes namespace for the Tempo + collector workloads.
	// Empty -> "observability".
	Namespace string
	// TempoImage / CollectorImage override the default container images (DO).
	TempoImage     string
	CollectorImage string
	// RetentionHours bounds how long Tempo keeps traces (block retention). Empty/0
	// defaults to 72h (3 days) — a cost-bounded default, never unbounded.
	RetentionHours int
}

// TracingPlan is the deterministic, catalog-resolved concrete translation of a
// TracingSpec for one provider. STRUCTURED plan (not rendered .tf) — the provider
// owns rendering and state, consistent with the other components.
type TracingPlan struct {
	Provider   string `json:"provider"`    // aws | digitalocean
	CSP        string `json:"csp"`         // catalog token: aws | do
	RegionName string `json:"region_name"` // abstract pyx region
	CSPRegion  string `json:"csp_region"`  // concrete provider region (catalog-resolved)

	Name         string  `json:"name"`
	SamplingRate float64 `json:"sampling_rate"`

	// ── Tempo + OTel collector (DigitalOcean) ──
	ClusterName    string `json:"cluster_name,omitempty"`
	Namespace      string `json:"namespace,omitempty"`
	TempoImage     string `json:"tempo_image,omitempty"`
	CollectorImage string `json:"collector_image,omitempty"`
	RetentionHours int    `json:"retention_hours,omitempty"`
	OTLPGRPCPort   int    `json:"otlp_grpc_port,omitempty"`
	OTLPHTTPPort   int    `json:"otlp_http_port,omitempty"`
	TempoQueryPort int    `json:"tempo_query_port,omitempty"`

	ResourceType string `json:"resource_type"` // top provider resource
}

// TranslateTracing resolves a TracingSpec into a concrete TracingPlan using the
// catalog. Deterministic and catalog-driven: the csp_region comes from the region
// catalog (never invented). Any missing catalog data — or a provider with no
// expressible tracing primitive — surfaces as a hard plan-time error (never a
// silent fallback), per SPEC §4.
func TranslateTracing(ctx context.Context, cat RegionCatalog, spec TracingSpec) (TracingPlan, error) {
	if err := validateTracingSpec(spec); err != nil {
		return TracingPlan{}, err
	}
	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return TracingPlan{}, err
	}
	provider := lc(spec.Provider)
	name := canonicalName(spec.Name, "pyxcloud-tracing")

	rate := spec.SamplingRate
	if rate <= 0 {
		rate = 0.1
	}

	plan := TracingPlan{
		Provider:     provider,
		CSP:          row.CSP,
		RegionName:   row.RegionName,
		CSPRegion:    row.CSPRegion,
		Name:         name,
		SamplingRate: rate,
	}

	switch provider {
	case ProviderAWS:
		// X-Ray group + sampling rule — the service being migrated away from. X-Ray
		// stores traces server-side; no cluster/namespace needed.
		plan.ResourceType = "aws_xray_group"
	case ProviderDigitalOcean:
		// Tempo + OTel collector on an existing DOKS cluster.
		cluster := strings.TrimSpace(spec.ClusterName)
		if cluster == "" {
			return TracingPlan{}, fmt.Errorf(
				"tracing: digitalocean replaces X-Ray with Grafana Tempo + an OpenTelemetry collector on a " +
					"DOKS cluster (DO has no managed tracing service) — cluster_name is required. This is a " +
					"hard plan-time error, never a silent fallback")
		}
		ns := strings.TrimSpace(spec.Namespace)
		if ns == "" {
			ns = defaultTracingNS
		}
		tempoImg := strings.TrimSpace(spec.TempoImage)
		if tempoImg == "" {
			tempoImg = defaultTempoImage
		}
		collImg := strings.TrimSpace(spec.CollectorImage)
		if collImg == "" {
			collImg = defaultOTelCollImage
		}
		retention := spec.RetentionHours
		if retention <= 0 {
			retention = 72
		}
		plan.ClusterName = cluster
		plan.Namespace = ns
		plan.TempoImage = tempoImg
		plan.CollectorImage = collImg
		plan.RetentionHours = retention
		plan.OTLPGRPCPort = defaultOTLPGRPCPort
		plan.OTLPHTTPPort = defaultOTLPHTTPPort
		plan.TempoQueryPort = defaultTempoQueryPort
		plan.ResourceType = "kubernetes_manifest"
	default:
		return TracingPlan{}, ErrComponentUnsupported{
			Component: TypeTracing, Provider: provider, CSP: row.CSP, CSPRegion: row.CSPRegion,
			Alternative: "tracing is supported on aws (X-Ray: aws_xray_group) and digitalocean " +
				"(Grafana Tempo + an OpenTelemetry collector on DOKS); for other providers run the " +
				"OTel-collector -> Tempo pipeline on a managed-kubernetes cluster",
		}
	}
	return plan, nil
}

func validateTracingSpec(spec TracingSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("tracing: region (abstract pyx region_name) is required")
	}
	if strings.TrimSpace(spec.Provider) == "" {
		return fmt.Errorf("tracing: provider is required (aws | digitalocean)")
	}
	if _, ok := ProviderToCSP(spec.Provider); !ok {
		return fmt.Errorf("tracing: unknown provider %q (aws | digitalocean)", spec.Provider)
	}
	if spec.SamplingRate < 0 || spec.SamplingRate > 1 {
		return fmt.Errorf("tracing: sampling_rate %.3f out of range (0 < rate <= 1)", spec.SamplingRate)
	}
	if spec.RetentionHours < 0 {
		return fmt.Errorf("tracing: retention_hours %d must be >= 0", spec.RetentionHours)
	}
	return nil
}

// CanonicalTracingType maps an accepted type token to the canonical tracing
// token, reporting whether it is recognised.
func CanonicalTracingType(t string) (string, bool) {
	switch lc(t) {
	case TypeTracing, TypeDistributedTracing, TypeTempo, TypeTraceCollector, TypeOTelTracing:
		return TypeTracing, true
	default:
		return "", false
	}
}
