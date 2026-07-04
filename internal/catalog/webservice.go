package catalog

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// web-service is the abstract `web-service` component — an ALWAYS-ON HTTP/SSE
// service (a persistent long-lived server with N>=1 instances), as opposed to the
// request-scoped, cold-starting `serverless-function`. It exists because a
// session-holding server (the MCP streamable-HTTP/SSE server, an OpenAI-compatible
// completions proxy) CANNOT run as a Function: DO Functions are request-scoped and
// time-bounded (the live board/api + mcp/api functions carry a 3s timeout), so an
// SSE session server belongs here, not in serverless-function.
//
// This is DELIBERATELY distinct from `container-service` (which is an alias for
// managed-kubernetes / DOKS and is load-bearing for the operator pattern) and from
// `serverless-function` (FaaS). It is a PaaS web service.
//
// SCOPE (SPEC §5 ethos — a managed always-on HTTP service that maps cleanly):
//   - DigitalOcean: digitalocean_app with a `service {}` component (App Platform).
//   - AWS:          aws_apprunner_service    (future; unsupported for now).
//   - GCP:          google_cloud_run_v2_service (future; unsupported for now).
//
// Like object-storage / reserved-ip it has NO sizing catalog: App Platform
// instance sizes are flat named slugs (basic-xxs …), so the only catalog lookup is
// the region (region_name + provider -> csp_region). Depends on RegionCatalog only.

// Canonical web-service type token. `web-service` is canonical; `app-service` and
// `app-platform-service` are accepted aliases (all name the same component). NB:
// intentionally NOT `container-service` — that token remains an alias for
// managed-kubernetes (operator pattern), see kubernetes.go.
const (
	TypeWebService         = "web-service"
	TypeAppService         = "app-service"
	TypeAppPlatformService = "app-platform-service"

	// webServiceSourceGit/Image are the accepted SourceKind values.
	webServiceSourceGit   = "git"
	webServiceSourceImage = "image"

	// DO App Platform defaults.
	defaultWebServiceInstanceSize  = "basic-xxs"
	defaultWebServiceInstanceCount = 1
	defaultWebServiceHTTPPort      = 8080
)

// WebServiceSpec is the abstract description of an always-on web service — the
// canonical `web-service { source, http_port, instance_size, instance_count,
// health_check_path, env, domain }`, placed in the place's region. Provider-neutral.
type WebServiceSpec struct {
	Name     string // web-service/component name, e.g. "mcp-server"
	Region   string // abstract pyx region_name, e.g. "Frankfurt"
	Provider string // provider-facing name: aws | gcp | digitalocean

	// SourceKind is "git" (build from a repo) or "image" (a prebuilt container
	// image). Empty defaults to "git" (mirrors serverless-function's git source).
	SourceKind string
	// Git source (SourceKind=git). The repo URL + branch are supplied out-of-band
	// via generated variables (never inlined) so no repo coordinates leak into HCL.
	SourceDir string
	// Image source (SourceKind=image): a registry image reference.
	ImageRegistryType string // DOCR | DOCKER_HUB
	ImageRepository   string
	ImageTag          string

	HTTPPort        int               // container listen port; default 8080
	InstanceSize    string            // App Platform slug; default basic-xxs
	InstanceCount   int               // always-on replicas; default 1 (>=1)
	HealthCheckPath string            // HTTP health path, e.g. "/health"; optional
	Env             map[string]string // plain (non-secret) env vars; deterministic order
	CustomDomain    string            // e.g. "inaudito.passo.build"; optional
}

// WebServicePlan is the deterministic, catalog-resolved concrete translation of a
// WebServiceSpec for one provider. STRUCTURED plan (not rendered .tf).
type WebServicePlan struct {
	Provider   string `json:"provider"`    // aws | gcp | digitalocean
	CSP        string `json:"csp"`         // catalog token: aws | gcp | do
	RegionName string `json:"region_name"` // abstract pyx region
	CSPRegion  string `json:"csp_region"`  // concrete provider region (catalog-resolved)

	Name       string `json:"name"`        // logical name (tf resource label + app/service name)
	SourceKind string `json:"source_kind"` // git | image
	SourceDir  string `json:"source_dir,omitempty"`

	ImageRegistryType string `json:"image_registry_type,omitempty"`
	ImageRepository   string `json:"image_repository,omitempty"`
	ImageTag          string `json:"image_tag,omitempty"`

	HTTPPort        int               `json:"http_port"`
	InstanceSize    string            `json:"instance_size"`
	InstanceCount   int               `json:"instance_count"`
	HealthCheckPath string            `json:"health_check_path,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
	CustomDomain    string            `json:"custom_domain,omitempty"`

	ResourceType string `json:"resource_type"` // top provider resource, e.g. digitalocean_app
}

// WebServiceCatalog is the resolution boundary for web services. Only region
// resolution is needed (no sizing table), so RegionCatalog suffices.
type WebServiceCatalog = RegionCatalog

// TranslateWebService resolves a WebServiceSpec into a concrete WebServicePlan
// using the catalog. Deterministic and catalog-driven: the csp_region comes from
// the region catalog (never invented); missing catalog data or an unsupported
// provider surfaces as a hard plan-time error (never a silent fallback), per §4.
func TranslateWebService(ctx context.Context, cat WebServiceCatalog, spec WebServiceSpec) (WebServicePlan, error) {
	if err := validateWebServiceSpec(spec); err != nil {
		return WebServicePlan{}, err
	}

	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return WebServicePlan{}, err
	}

	provider := strings.ToLower(strings.TrimSpace(spec.Provider))

	name := strings.TrimSpace(spec.Name)
	if name == "" {
		name = "pyxcloud-web-service"
	}

	sourceKind := strings.ToLower(strings.TrimSpace(spec.SourceKind))
	if sourceKind == "" {
		sourceKind = webServiceSourceGit
	}
	if sourceKind != webServiceSourceGit && sourceKind != webServiceSourceImage {
		return WebServicePlan{}, fmt.Errorf(
			"web-service %q: source_kind %q invalid (git | image)", name, spec.SourceKind)
	}
	if sourceKind == webServiceSourceImage && strings.TrimSpace(spec.ImageRepository) == "" {
		return WebServicePlan{}, fmt.Errorf(
			"web-service %q: source_kind=image requires image_repository", name)
	}

	httpPort := spec.HTTPPort
	if httpPort == 0 {
		httpPort = defaultWebServiceHTTPPort
	}
	instanceSize := strings.TrimSpace(spec.InstanceSize)
	if instanceSize == "" {
		instanceSize = defaultWebServiceInstanceSize
	}
	instanceCount := spec.InstanceCount
	if instanceCount == 0 {
		instanceCount = defaultWebServiceInstanceCount
	}
	if instanceCount < 1 {
		return WebServicePlan{}, fmt.Errorf(
			"web-service %q: instance_count %d invalid (a web service is always-on, >=1)",
			name, instanceCount)
	}

	// The always-on PaaS web service maps cleanly only to DO App Platform in this
	// wave. aws/gcp have equivalents (App Runner / Cloud Run) but are not wired yet;
	// reject with a clean, directed error rather than an invented/partial resource.
	if provider != ProviderDigitalOcean {
		return WebServicePlan{}, ErrComponentUnsupported{
			Component: TypeWebService, Provider: provider, CSP: row.CSP, CSPRegion: row.CSPRegion,
			Alternative: "web-service (always-on PaaS) is wired for DigitalOcean App Platform " +
				"(digitalocean_app service) in this wave; the aws (aws_apprunner_service) and gcp " +
				"(google_cloud_run_v2_service) equivalents are not implemented yet — target " +
				"DigitalOcean for this component, or run it on a virtual-machine behind a load-balancer",
		}
	}

	plan := WebServicePlan{
		Provider:          provider,
		CSP:               row.CSP,
		RegionName:        row.RegionName,
		CSPRegion:         row.CSPRegion,
		Name:              name,
		SourceKind:        sourceKind,
		SourceDir:         strings.TrimSpace(spec.SourceDir),
		ImageRegistryType: strings.TrimSpace(spec.ImageRegistryType),
		ImageRepository:   strings.TrimSpace(spec.ImageRepository),
		ImageTag:          strings.TrimSpace(spec.ImageTag),
		HTTPPort:          httpPort,
		InstanceSize:      instanceSize,
		InstanceCount:     instanceCount,
		HealthCheckPath:   strings.TrimSpace(spec.HealthCheckPath),
		Env:               spec.Env,
		CustomDomain:      strings.TrimSpace(spec.CustomDomain),
		ResourceType:      "digitalocean_app",
	}
	return plan, nil
}

func validateWebServiceSpec(spec WebServiceSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("web-service: region (abstract pyx region_name) is required")
	}
	if strings.TrimSpace(spec.Provider) == "" {
		return fmt.Errorf("web-service: provider is required (aws | gcp | digitalocean)")
	}
	if _, ok := ProviderToCSP(spec.Provider); !ok {
		return fmt.Errorf("web-service: unknown provider %q (aws | gcp | digitalocean)", spec.Provider)
	}
	return nil
}

// CanonicalWebServiceType maps an accepted type token (web-service / app-service /
// app-platform-service) to the canonical web-service token, reporting whether it is
// a recognised type.
func CanonicalWebServiceType(t string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case TypeWebService, TypeAppService, TypeAppPlatformService:
		return TypeWebService, true
	default:
		return "", false
	}
}

// sortedEnvKeys returns the env keys in deterministic (sorted) order so the
// rendered HCL is stable regardless of Go map iteration order.
func sortedEnvKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
