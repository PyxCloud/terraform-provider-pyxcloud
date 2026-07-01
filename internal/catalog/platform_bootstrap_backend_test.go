package catalog

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"
)

// TestRenderBackendDORequiresEnvironment asserts the bootstrap refuses to render
// without an environment (it drives the OIDC/Vault/console hostnames).
func TestRenderBackendDORequiresEnvironment(t *testing.T) {
	t.Parallel()
	if _, err := RenderBackendDOUserData(BackendBootstrapSpec{}); err == nil {
		t.Fatal("want error for missing environment, got nil")
	}
}

// TestRenderBackendDOUsesDOMainDBAndNoIMDS is the core cutover assertion: the
// rendered DO backend bootstrap points PYX_MAIN_DATABASE_JDBC_URL at the DO
// pyx-main-db (mesh_app) URL variable (NOT an RDS/AWS host), and it carries NO
// AWS IMDS (169.254.169.254) health-probe / CloudWatch publish — the DO health
// check is a plain local :8080 curl.
func TestRenderBackendDOUsesDOMainDBAndNoIMDS(t *testing.T) {
	t.Parallel()
	ud, err := RenderBackendDOUserData(BackendBootstrapSpec{Environment: "beta"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// PYX_MAIN_DATABASE points at the DO pyx-main-db (mesh_app) URL variable.
	if !strings.Contains(ud, "PYX_MAIN_DATABASE_JDBC_URL=${var.do_main_db_url}") {
		t.Error("PYX_MAIN_DATABASE_JDBC_URL must be sourced from the DO pyx-main-db URL variable ${var.do_main_db_url}")
	}
	if !strings.Contains(ud, "mesh_app") {
		t.Error("bootstrap should name the DO pyx-main-db target database (mesh_app)")
	}

	// NO AWS IMDS health-probe: the 169.254.169.254 metadata lookup must be gone.
	if strings.Contains(ud, "169.254.169.254") {
		t.Error("DO bootstrap must NOT contain the AWS IMDS 169.254.169.254 health-probe (DO metadata differs)")
	}
	// The local :8080 health check must be present.
	if !strings.Contains(ud, "http://localhost:8080/q/health/live") {
		t.Error("DO bootstrap must use a local :8080 health check")
	}
}

// TestRenderBackendDOAWSCouplingsAdapted asserts the specific AWS-coupling
// decisions for the cutover: CloudWatch/X-Ray dropped, SAST-ASG disabled, AWS SDK
// creds KEPT as a passthrough, native binary pulled from DO Spaces, Vault URL kept.
func TestRenderBackendDOAWSCouplingsAdapted(t *testing.T) {
	t.Parallel()
	ud, err := RenderBackendDOUserData(BackendBootstrapSpec{Environment: "beta"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// CloudWatch / X-Ray DROPPED.
	for _, dropped := range []string{"amazon-cloudwatch-agent", "cloudwatch-agent", "put-metric-data", "cw-agent-api.json"} {
		if strings.Contains(ud, dropped) {
			t.Errorf("DO bootstrap must DROP CloudWatch/X-Ray; found %q", dropped)
		}
	}

	// SAST-ASG integration DISABLED.
	if !strings.Contains(ud, "PYX_SAST_ASG_ENABLED=false") {
		t.Error("SAST-ASG integration must be DISABLED (PYX_SAST_ASG_ENABLED=false) for the DO cutover")
	}

	// AWS SDK creds KEPT as a passthrough (cross-cloud calls).
	for _, kept := range []string{
		"AWS_ACCESS_KEY_ID=${var.aws_access_key_id}",
		"AWS_SECRET_ACCESS_KEY=${var.aws_secret_access_key}",
	} {
		if !strings.Contains(ud, kept) {
			t.Errorf("AWS SDK creds passthrough must be KEPT; missing %q", kept)
		}
	}

	// Native binary from DO Spaces (fra1 S3-compatible endpoint), version 0.4.49.
	for _, want := range []string{
		"s3://pyx-artifacts-fra1/beta/pyxcloud",
		"fra1.digitaloceanspaces.com",
		"--endpoint-url",
		"0.4.49",
	} {
		if !strings.Contains(ud, want) {
			t.Errorf("DO Spaces native-binary pull missing %q", want)
		}
	}

	// Vault URL KEPT.
	if !strings.Contains(ud, "VAULT_URL=https://beta-vault.pyxcloud.io") {
		t.Error("Vault URL (beta-vault.pyxcloud.io) must be KEPT")
	}
}

// TestRenderBackendDOInlinesNoSecretValues is the security invariant: every
// credential is referenced by Terraform variable, never embedded as a literal.
func TestRenderBackendDOInlinesNoSecretValues(t *testing.T) {
	t.Parallel()
	ud, err := RenderBackendDOUserData(BackendBootstrapSpec{Environment: "beta"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, ref := range []string{
		"${var.do_main_db_url}",
		"${var.do_spaces_key}",
		"${var.do_spaces_secret}",
		"${var.oidc_client_secret}",
		"${var.gh_pat}",
		"${var.stripe_token}",
		"${var.aws_access_key_id}",
		"${var.aws_secret_access_key}",
	} {
		if !strings.Contains(ud, ref) {
			t.Errorf("expected secret to be referenced by variable %q, but it was not", ref)
		}
	}
}

// TestBackendBootstrapVariableNamesPartitioned asserts the DO Spaces keys, the DB
// URL and the credential vars are marked sensitive (not plain).
func TestBackendBootstrapVariableNamesPartitioned(t *testing.T) {
	t.Parallel()
	plain, sensitive := BackendBootstrapSpec{Environment: "beta"}.BackendBootstrapVariableNames()
	sensSet := map[string]bool{}
	for _, s := range sensitive {
		sensSet[s] = true
	}
	for _, mustBeSensitive := range []string{
		"do_main_db_url", "do_spaces_key", "do_spaces_secret",
		"oidc_client_secret", "gh_pat", "stripe_token",
		"aws_access_key_id", "aws_secret_access_key",
	} {
		if !sensSet[mustBeSensitive] {
			t.Errorf("var %q must be sensitive", mustBeSensitive)
		}
	}
	for _, p := range plain {
		if sensSet[p] {
			t.Errorf("var %q listed both plain and sensitive", p)
		}
	}
}

// TestBackendDOScaleGroupComponentWiresDOUserData is the integration proof: the
// backend DO bootstrap lands on the `backend` scale-group's
// UserDataByProvider["digitalocean"] (and the generic UserData stays empty so
// non-DO placements are unaffected).
func TestBackendDOScaleGroupComponentWiresDOUserData(t *testing.T) {
	t.Parallel()
	comp, err := BackendDOScaleGroupComponent("", "", "", BackendBootstrapSpec{Environment: "beta"})
	if err != nil {
		t.Fatalf("component: %v", err)
	}
	if comp.Name != "backend" || comp.Type != "virtual-machine-scale-group" || comp.ScaleGroup == nil {
		t.Fatalf("unexpected component shape: %+v", comp)
	}
	do := comp.ScaleGroup.UserDataByProvider[ProviderDigitalOcean]
	if !strings.Contains(do, "PYX_MAIN_DATABASE_JDBC_URL=${var.do_main_db_url}") {
		t.Fatal("backend scale-group did not receive the DO bootstrap on UserDataByProvider[\"digitalocean\"]")
	}
	if comp.ScaleGroup.UserData != "" {
		t.Error("generic UserData must stay empty so non-DO placements are unaffected")
	}
}

// TestBackendDOTerraformValidate is the executable plan-only proof that the
// backend DO scale-group (with the ported user_data) descends to valid,
// initialisable DigitalOcean HCL: assemble a minimal DO environment carrying the
// backend component, emit the `variable {}` declarations the bootstrap
// references, and run `terraform init && validate`. Skipped when no terraform
// binary is on PATH (so `go test ./...` stays green in a binary-less CI).
func TestBackendDOTerraformValidate(t *testing.T) {
	if os.Getenv("PYX_TF_VALIDATE") == "0" {
		t.Skip("PYX_TF_VALIDATE=0: terraform validate explicitly disabled")
	}
	execPath, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("terraform binary not on PATH: the string round-trips prove the render")
	}

	spec := BackendBootstrapSpec{Environment: "beta"}
	comp, err := BackendDOScaleGroupComponent("x86_64", "ubuntu", "1.30", spec)
	if err != nil {
		t.Fatalf("component: %v", err)
	}
	docs, err := AssembleHCL(context.Background(), MustEmbedded(), AssembleInput{
		Name:       "backend-cutover",
		Provider:   "digitalocean",
		Region:     "Frankfurt",
		Expose:     []int{8080},
		Components: []AssembleComponent{comp},
	})
	if err != nil {
		t.Fatalf("AssembleHCL backend (DO): %v", err)
	}

	// The bootstrap references ${var.x}; emit the matching declarations so the
	// rendered .tf is self-contained and validate-able (the CLI/assembler wires
	// these to Secrets Manager at apply time).
	plain, sensitive := spec.BackendBootstrapVariableNames()
	var vb strings.Builder
	for _, p := range plain {
		fmt.Fprintf(&vb, "variable %q {\n  type    = string\n  default = \"\"\n}\n\n", p)
	}
	for _, s := range sensitive {
		fmt.Fprintf(&vb, "variable %q {\n  type      = string\n  sensitive = true\n  default   = \"\"\n}\n\n", s)
	}
	docs = append(docs, vb.String())

	dir := t.TempDir()
	for i, d := range docs {
		name := filepath.Join(dir, fmt.Sprintf("pyx_%03d.tf", i))
		if werr := os.WriteFile(name, []byte(d), 0o644); werr != nil {
			t.Fatalf("write doc %d: %v", i, werr)
		}
	}

	tf, err := tfexec.NewTerraform(dir, execPath)
	if err != nil {
		t.Fatalf("tfexec: %v", err)
	}
	ctx := context.Background()
	if ierr := tf.Init(ctx, tfexec.Upgrade(false)); ierr != nil {
		t.Fatalf("terraform init failed — backend DO render not initialisable: %v", ierr)
	}
	vout, verr := tf.Validate(ctx)
	if verr != nil {
		t.Fatalf("terraform validate failed — backend DO render not valid HCL: %v", verr)
	}
	if !vout.Valid {
		t.Fatalf("terraform validate reported INVALID: %d diagnostics", vout.ErrorCount)
	}
	t.Log("terraform init && validate: GREEN — backend DO user_data render is valid HCL")
}
