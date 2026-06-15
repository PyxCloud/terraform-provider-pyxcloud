package catalog

import (
	"context"
	"fmt"
	"strings"
)

// ServerlessFunction is the abstract `serverless-function` (lambda) component
// (SPEC §5.8) — the FINAL wave-1 component:
//
//   - AWS: aws_lambda_function (zip package from a deployment artifact, an
//     execution role, memory/timeout). NOT URL-exposed by default (private; an
//     invoke is via an event source / API gateway wired separately).
//   - GCP: google_cloudfunctions2_function (2nd-gen Cloud Functions on Cloud Run),
//     source from a bucket object, entry point, memory/timeout. Private by default
//     (no allUsers invoker binding).
//   - DigitalOcean: digitalocean_app with a `functions` component (App Platform
//     Functions) — the clean DO serverless answer. DO Functions are deployed from
//     a source repo/dir; the App Platform spec carries the function component.
//
// SECURITY: a function is PRIVATE by default — no public function URL / no
// allUsers invoker. Public exposure is an explicit opt-in (a separate API
// gateway / invoker binding), never emitted by the macro component.

// Canonical serverless runtimes (cross-provider tokens). We accept a small,
// cross-provider set; the per-provider render maps these to the provider's
// concrete runtime identifier.
const (
	RuntimeNode   = "nodejs"
	RuntimePython = "python"
	RuntimeGo     = "go"
)

// ServerlessSpec is the abstract serverless-function description. Provider-neutral.
type ServerlessSpec struct {
	Name     string
	Region   string
	Provider string

	// Runtime is the canonical runtime family (nodejs | python | go).
	Runtime string
	// RuntimeVersion is the version, e.g. "20" (node), "3.12" (python), "1.22" (go).
	// Empty -> a sensible per-runtime default.
	RuntimeVersion string
	// Handler is the entrypoint, e.g. "index.handler" / "main.handler".
	Handler string
	// MemoryMB / TimeoutSeconds size the function. 0 -> provider defaults.
	MemoryMB       int
	TimeoutSeconds int

	// SourceArtifact is the deployment package reference (a zip path for AWS, a
	// bucket object for GCP, a source dir/repo for DO). Provider-neutral string.
	SourceArtifact string
}

// ServerlessPlan is the deterministic, catalog-resolved concrete translation.
type ServerlessPlan struct {
	Provider   string `json:"provider"`
	CSP        string `json:"csp"`
	RegionName string `json:"region_name"`
	CSPRegion  string `json:"csp_region"`

	Name            string `json:"name"`
	Runtime         string `json:"runtime"`          // canonical family
	ConcreteRuntime string `json:"concrete_runtime"` // provider runtime id, e.g. nodejs20.x
	Handler         string `json:"handler"`
	MemoryMB        int    `json:"memory_mb"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
	SourceArtifact  string `json:"source_artifact"`

	ResourceType string `json:"resource_type"`
}

var defaultRuntimeVersions = map[string]string{
	RuntimeNode:   "20",
	RuntimePython: "3.12",
	RuntimeGo:     "1.x",
}

// TranslateServerless resolves a ServerlessSpec into a concrete ServerlessPlan.
// Catalog-driven for the region; the concrete runtime id is derived per provider.
// All three providers have a clean serverless primitive (DO App Platform
// Functions), so there is no unsupported path.
func TranslateServerless(ctx context.Context, cat RegionCatalog, spec ServerlessSpec) (ServerlessPlan, error) {
	if err := validateServerlessSpec(spec); err != nil {
		return ServerlessPlan{}, err
	}
	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return ServerlessPlan{}, err
	}
	provider := lc(spec.Provider)

	runtime := lc(spec.Runtime)
	if runtime == "" {
		runtime = RuntimeNode
	}
	version := strings.TrimSpace(spec.RuntimeVersion)
	if version == "" {
		version = defaultRuntimeVersions[runtime]
	}
	handler := strings.TrimSpace(spec.Handler)
	if handler == "" {
		handler = "index.handler"
	}
	mem := spec.MemoryMB
	if mem <= 0 {
		mem = 128
	}
	timeout := spec.TimeoutSeconds
	if timeout <= 0 {
		timeout = 30
	}

	plan := ServerlessPlan{
		Provider:        provider,
		CSP:             row.CSP,
		RegionName:      row.RegionName,
		CSPRegion:       row.CSPRegion,
		Name:            canonicalName(spec.Name, "pyxcloud-fn"),
		Runtime:         runtime,
		ConcreteRuntime: concreteRuntime(provider, runtime, version),
		Handler:         handler,
		MemoryMB:        mem,
		TimeoutSeconds:  timeout,
		SourceArtifact:  strings.TrimSpace(spec.SourceArtifact),
	}
	switch provider {
	case ProviderAWS:
		plan.ResourceType = "aws_lambda_function"
	case ProviderGCP:
		plan.ResourceType = "google_cloudfunctions2_function"
	case ProviderDigitalOcean:
		plan.ResourceType = "digitalocean_app"
	case ProviderAlibaba:
		plan.ResourceType = "alicloud_fcv3_function"
	}
	return plan, nil
}

// concreteRuntime maps a canonical (runtime, version) to the provider's runtime id.
func concreteRuntime(provider, runtime, version string) string {
	switch provider {
	case ProviderAWS:
		// AWS Lambda runtime identifiers.
		switch runtime {
		case RuntimeNode:
			return "nodejs" + version + ".x"
		case RuntimePython:
			return "python" + version
		case RuntimeGo:
			return "provided.al2023" // Go runs on the custom runtime on AL2023
		}
	case ProviderGCP:
		// Cloud Functions runtime identifiers.
		switch runtime {
		case RuntimeNode:
			return "nodejs" + version
		case RuntimePython:
			return "python" + strings.ReplaceAll(version, ".", "")
		case RuntimeGo:
			return "go" + strings.ReplaceAll(version, ".x", "")
		}
	case ProviderDigitalOcean:
		// DO Functions runtime identifiers.
		switch runtime {
		case RuntimeNode:
			return "node:" + strings.Split(version, ".")[0]
		case RuntimePython:
			return "python:" + version
		case RuntimeGo:
			return "go:" + strings.TrimSuffix(version, ".x")
		}
	case ProviderAlibaba:
		// Alibaba Function Compute (FC) v3 runtime identifiers.
		switch runtime {
		case RuntimeNode:
			return "nodejs" + version
		case RuntimePython:
			return "python" + version
		case RuntimeGo:
			return "go1"
		}
	}
	return runtime + version
}

func validateServerlessSpec(spec ServerlessSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("serverless-function: region (abstract pyx region_name) is required")
	}
	if _, ok := ProviderToCSP(spec.Provider); !ok {
		return fmt.Errorf("serverless-function: unknown provider %q (aws | gcp | digitalocean)", spec.Provider)
	}
	if rt := lc(spec.Runtime); rt != "" && rt != RuntimeNode && rt != RuntimePython && rt != RuntimeGo {
		return fmt.Errorf("serverless-function: unsupported runtime %q (nodejs | python | go)", spec.Runtime)
	}
	if spec.MemoryMB < 0 {
		return fmt.Errorf("serverless-function: memory_mb must be >= 0")
	}
	if spec.TimeoutSeconds < 0 {
		return fmt.Errorf("serverless-function: timeout_seconds must be >= 0")
	}
	return nil
}

// CanonicalServerlessType reports whether t names the serverless-function
// component (accepts the lambda alias).
func CanonicalServerlessType(t string) (string, bool) {
	switch lc(t) {
	case TypeServerlessFunction, "serverless", "lambda", "function":
		return TypeServerlessFunction, true
	}
	return "", false
}
