package catalog

import (
	"context"
	"fmt"
	"strings"
)

// Synthetics is the abstract `synthetics` / `uptime-check` component: a scheduled
// canary that probes an endpoint — the canonical form of the per-provider scripts'
// aws_synthetics_canary glue (e.g. the login canary).
//
//   - AWS: aws_synthetics_canary. The canary's artifact bucket and execution role
//     are supplied out of band (terraform vars) — the same out-of-band-credential
//     pattern the LB target / DB password use — never invented here.
//   - GCP: google_monitoring_uptime_check_config (a simpler uptime check).
//   - DigitalOcean: digitalocean_uptime_check.

// SyntheticsSpec is the abstract canary description.
type SyntheticsSpec struct {
	Name           string
	Region         string
	Provider       string
	TargetURL      string // the endpoint to probe (GCP/DO); AWS uses the script
	Runtime        string // AWS canary runtime, e.g. syn-nodejs-puppeteer-9.0
	Handler        string // AWS canary handler, e.g. index.handler
	ScheduleExpr   string // AWS rate(...) / cron(...); defaults rate(5 minutes)
	ArtifactBucket string // AWS artifact S3 bucket (out of band); empty -> var
	ExecRoleARN    string // AWS execution role ARN (out of band); empty -> var
}

// SyntheticsPlan is the resolved concrete plan.
type SyntheticsPlan struct {
	Provider     string `json:"provider"`
	CSP          string `json:"csp"`
	RegionName   string `json:"region_name"`
	CSPRegion    string `json:"csp_region"`
	Name         string `json:"name"`
	TargetURL    string `json:"target_url"`
	Runtime      string `json:"runtime"`
	Handler      string `json:"handler"`
	ScheduleExpr string `json:"schedule_expr"`
	ArtifactB    string `json:"artifact_bucket"`
	ExecRoleARN  string `json:"exec_role_arn"`
	ResourceType string `json:"resource_type"`
}

// TranslateSynthetics resolves a SyntheticsSpec.
func TranslateSynthetics(ctx context.Context, cat RegionCatalog, spec SyntheticsSpec) (SyntheticsPlan, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return SyntheticsPlan{}, fmt.Errorf("synthetics: name is required")
	}
	csp, ok := ProviderToCSP(spec.Provider)
	if !ok {
		return SyntheticsPlan{}, fmt.Errorf("synthetics: unknown provider %q", spec.Provider)
	}
	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return SyntheticsPlan{}, err
	}
	p := SyntheticsPlan{
		Provider: strings.ToLower(spec.Provider), CSP: csp,
		RegionName: row.RegionName, CSPRegion: row.CSPRegion,
		Name: spec.Name, TargetURL: spec.TargetURL,
		Runtime: spec.Runtime, Handler: spec.Handler, ScheduleExpr: spec.ScheduleExpr,
		ArtifactB: spec.ArtifactBucket, ExecRoleARN: spec.ExecRoleARN,
	}
	switch p.Provider {
	case ProviderAWS:
		if p.Runtime == "" {
			p.Runtime = "syn-nodejs-puppeteer-9.0"
		}
		if p.Handler == "" {
			p.Handler = "index.handler"
		}
		if p.ScheduleExpr == "" {
			p.ScheduleExpr = "rate(5 minutes)"
		}
		p.ResourceType = "aws_synthetics_canary"
	case ProviderGCP:
		p.ResourceType = "google_monitoring_uptime_check_config"
	case ProviderDigitalOcean:
		p.ResourceType = "digitalocean_uptime_check"
	default:
		return SyntheticsPlan{}, fmt.Errorf("synthetics: unsupported provider %q", spec.Provider)
	}
	if (p.Provider == ProviderGCP || p.Provider == ProviderDigitalOcean) && strings.TrimSpace(p.TargetURL) == "" {
		return SyntheticsPlan{}, fmt.Errorf("synthetics %q: target_url is required for %s uptime checks", spec.Name, p.Provider)
	}
	return p, nil
}

// RenderSyntheticsHCL renders a SyntheticsPlan.
func RenderSyntheticsHCL(p SyntheticsPlan) (string, error) {
	var b strings.Builder
	name := tfName(p.Name)
	switch p.Provider {
	case ProviderAWS:
		artifact := fmt.Sprintf("%q", p.ArtifactB)
		if strings.TrimSpace(p.ArtifactB) == "" {
			artifact = "var." + name + "_artifact_bucket"
			fmt.Fprintf(&b, "variable %q {\n  type = string\n}\n\n", name+"_artifact_bucket")
		}
		role := fmt.Sprintf("%q", p.ExecRoleARN)
		if strings.TrimSpace(p.ExecRoleARN) == "" {
			role = "var." + name + "_exec_role_arn"
			fmt.Fprintf(&b, "variable %q {\n  type = string\n}\n\n", name+"_exec_role_arn")
		}
		fmt.Fprintf(&b, "resource \"aws_synthetics_canary\" %q {\n", name)
		fmt.Fprintf(&b, "  name                 = %q\n", awsCanaryName(p.Name))
		fmt.Fprintf(&b, "  artifact_s3_location = \"s3://${%s}/%s\"\n", strings.TrimPrefix(artifact, "var."), p.Name)
		fmt.Fprintf(&b, "  execution_role_arn   = %s\n", role)
		fmt.Fprintf(&b, "  handler              = %q\n", p.Handler)
		fmt.Fprintf(&b, "  runtime_version      = %q\n", p.Runtime)
		fmt.Fprintf(&b, "  zip_file             = var.%s_zip\n", name)
		b.WriteString("  schedule {\n")
		fmt.Fprintf(&b, "    expression = %q\n", p.ScheduleExpr)
		b.WriteString("  }\n")
		fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
		b.WriteString("}\n\n")
		fmt.Fprintf(&b, "variable %q {\n  type = string\n  description = \"Path to the zipped canary script.\"\n}\n", name+"_zip")
		return b.String(), nil
	case ProviderGCP:
		fmt.Fprintf(&b, "resource \"google_monitoring_uptime_check_config\" %q {\n", name)
		fmt.Fprintf(&b, "  display_name = %q\n", p.Name)
		b.WriteString("  http_check { path = \"/\" use_ssl = true }\n")
		b.WriteString("  monitored_resource {\n    type = \"uptime_url\"\n")
		fmt.Fprintf(&b, "    labels = { host = %q }\n", hostOf(p.TargetURL))
		b.WriteString("  }\n}\n")
		return b.String(), nil
	case ProviderDigitalOcean:
		fmt.Fprintf(&b, "resource \"digitalocean_uptime_check\" %q {\n", name)
		fmt.Fprintf(&b, "  name   = %q\n", p.Name)
		fmt.Fprintf(&b, "  target = %q\n", p.TargetURL)
		b.WriteString("}\n")
		return b.String(), nil
	default:
		return "", fmt.Errorf("synthetics: render unsupported for provider %q", p.Provider)
	}
}

// awsCanaryName clamps to the AWS canary name charset/length ([0-9a-z_-], <=21).
func awsCanaryName(name string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			out.WriteRune(r)
		}
	}
	s := out.String()
	if len(s) > 21 {
		s = s[:21]
	}
	if s == "" {
		s = "pyxcanary"
	}
	return s
}

func hostOf(url string) string {
	u := strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
	if i := strings.IndexByte(u, '/'); i >= 0 {
		u = u[:i]
	}
	return u
}
