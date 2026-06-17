package catalog

import (
	"fmt"
	"strings"
)

// CanarySpec is a synthetic monitoring canary — a scheduled probe of an endpoint
// (the login canary / uptime check our observability uses). AWS-complete
// (aws_synthetics_canary); others hard-unsupported.
type CanarySpec struct {
	Name           string
	Provider       string
	ArtifactBucket string // S3 bucket (var/name) for run artifacts
	Schedule       string // rate expression, e.g. "rate(5 minutes)"
	RuntimeVersion string // e.g. syn-nodejs-puppeteer-9.0
	HandlerScript  string // canary handler script body (cloud-init style)
}

// CanaryPlan is the deterministic concrete translation.
type CanaryPlan struct {
	Provider       string `json:"provider"`
	CSP            string `json:"csp"`
	Name           string `json:"name"`
	ArtifactBucket string `json:"artifact_bucket"`
	Schedule       string `json:"schedule"`
	RuntimeVersion string `json:"runtime_version"`
	HandlerScript  string `json:"handler_script"`
	ResourceType   string `json:"resource_type"`
}

// TranslateCanary resolves a CanarySpec into a concrete plan.
func TranslateCanary(spec CanarySpec) (CanaryPlan, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return CanaryPlan{}, fmt.Errorf("canary: name is required")
	}
	csp, ok := ProviderToCSP(spec.Provider)
	if !ok {
		return CanaryPlan{}, fmt.Errorf("canary: unknown provider %q", spec.Provider)
	}
	if strings.TrimSpace(spec.ArtifactBucket) == "" {
		return CanaryPlan{}, fmt.Errorf("canary: artifact_bucket is required")
	}
	if strings.TrimSpace(spec.Schedule) == "" {
		return CanaryPlan{}, fmt.Errorf("canary: schedule (e.g. rate(5 minutes)) is required")
	}
	provider := strings.ToLower(strings.TrimSpace(spec.Provider))
	if provider != ProviderAWS {
		return CanaryPlan{}, fmt.Errorf("canary: unsupported on provider %q (supported: aws CloudWatch Synthetics). "+
			"Hard plan-time error, never an invented resource", provider)
	}
	runtime := spec.RuntimeVersion
	if runtime == "" {
		runtime = "syn-nodejs-puppeteer-9.0"
	}
	return CanaryPlan{
		Provider: provider, CSP: csp, Name: spec.Name, ArtifactBucket: spec.ArtifactBucket,
		Schedule: spec.Schedule, RuntimeVersion: runtime, HandlerScript: spec.HandlerScript,
		ResourceType: "aws_synthetics_canary",
	}, nil
}

// RenderCanaryHCL renders a resolved plan. The execution role + zipped handler are
// out-of-band (referenced by name/var), matching the wave-1 out-of-band pattern.
func RenderCanaryHCL(plan CanaryPlan) (string, error) {
	if plan.Provider != ProviderAWS {
		return "", fmt.Errorf("canary: no renderer for provider %q", plan.Provider)
	}
	var b strings.Builder
	rn := tfName(plan.Name)
	fmt.Fprintf(&b, "resource \"aws_synthetics_canary\" %q {\n", rn)
	fmt.Fprintf(&b, "  name                 = %q\n", plan.Name)
	fmt.Fprintf(&b, "  artifact_s3_location = %q\n", "s3://"+plan.ArtifactBucket+"/"+plan.Name)
	fmt.Fprintf(&b, "  execution_role_arn   = var.%s_canary_role_arn\n", tfName(plan.Name))
	fmt.Fprintf(&b, "  runtime_version      = %q\n", plan.RuntimeVersion)
	fmt.Fprintf(&b, "  handler              = \"index.handler\"\n")
	fmt.Fprintf(&b, "  zip_file             = var.%s_canary_zip\n", tfName(plan.Name))
	b.WriteString("  schedule {\n")
	fmt.Fprintf(&b, "    expression = %q\n", plan.Schedule)
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String(), nil
}
