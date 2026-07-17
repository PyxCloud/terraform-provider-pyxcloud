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

// TestRenderBackendDOUsesGoDBEnvAndNoIMDS is the core cutover assertion: the
// rendered DO backend bootstrap points PYX_QUARKUS_DATASOURCE_JDBC_URL at the
// DO pyx-main-db URL variable (NOT an RDS/AWS host), and it carries NO AWS IMDS
// health-probe / CloudWatch publish. The DO health checks are the Go /healthz
// and /readyz endpoints.
func TestRenderBackendDOUsesGoDBEnvAndNoIMDS(t *testing.T) {
	t.Parallel()
	ud, err := RenderBackendDOUserData(BackendBootstrapSpec{Environment: "beta"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// The Go loader reads quarkus.datasource.* through PYX_QUARKUS_DATASOURCE_*.
	if !strings.Contains(ud, "PYX_QUARKUS_DATASOURCE_JDBC_URL=${var.do_main_db_url}") {
		t.Error("PYX_QUARKUS_DATASOURCE_JDBC_URL must be sourced from the DO pyx-main-db URL variable ${var.do_main_db_url}")
	}
	if !strings.Contains(ud, "mesh_app") {
		t.Error("bootstrap should name the DO pyx-main-db target database (mesh_app)")
	}

	// The DB secret may be a libpq URI (postgres://...). The bootstrap MUST
	// normalize it in place to jdbc:postgresql:// and split username/password for
	// the Go pgx pool.
	if !strings.Contains(ud, "jdbc:postgresql://") {
		t.Error("bootstrap must normalize the DB URL to the jdbc:postgresql:// scheme")
	}
	if !strings.Contains(ud, "PYX_QUARKUS_DATASOURCE_USERNAME=") {
		t.Error("bootstrap must derive PYX_QUARKUS_DATASOURCE_USERNAME from the libpq URI")
	}
	if !strings.Contains(ud, "PYX_QUARKUS_DATASOURCE_PASSWORD=") {
		t.Error("bootstrap must derive PYX_QUARKUS_DATASOURCE_PASSWORD from the libpq URI")
	}

	// NO AWS IMDS health-probe: the 169.254.169.254 metadata lookup must be gone.
	if strings.Contains(ud, "169.254.169.254") {
		t.Error("DO bootstrap must NOT contain the AWS IMDS 169.254.169.254 health-probe (DO metadata differs)")
	}
	// The local :8080 Go health checks must be present.
	if !strings.Contains(ud, "http://localhost:8080/healthz") || !strings.Contains(ud, "http://localhost:8080/readyz") {
		t.Error("DO bootstrap must use local Go /healthz and /readyz checks")
	}
}

// TestRenderBackendDOAWSCouplingsAdapted asserts the specific AWS-coupling
// decisions for the cutover: CloudWatch/X-Ray dropped, SAST-ASG disabled, AWS SDK
// creds KEPT as a passthrough, Go binary pulled from DO Spaces, Vault URL kept.
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

	// Go binary from DO Spaces (fra1 S3-compatible endpoint), version 0.4.60.
	for _, want := range []string{
		"BUCKET=\"beta-pyxcloud-artifact\"",
		"pyx-backend",
		"fra1.digitaloceanspaces.com",
		"--endpoint-url",
		"0.4.60",
	} {
		if !strings.Contains(ud, want) {
			t.Errorf("DO Spaces Go binary pull missing %q", want)
		}
	}

	// Vault URL KEPT.
	if !strings.Contains(ud, "PYX_VAULT_ADDR=https://beta-vault.pyxcloud.io") {
		t.Error("Vault URL (beta-vault.pyxcloud.io) must be kept through PYX_VAULT_ADDR")
	}
}

// TestRenderBackendDONoJavaRuntimeRegressions asserts the provider-owned
// bootstrap no longer carries Java/Quarkus native runtime scaffolding.
func TestRenderBackendDONoJavaRuntimeRegressions(t *testing.T) {
	t.Parallel()
	ud, err := RenderBackendDOUserData(BackendBootstrapSpec{Environment: "beta"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, banned := range []string{
		"QUARKUS_LANGCHAIN4J_OPENAI_API_KEY",
		"/q/health",
		"/home/main/pyxcloud",
		"pyxcloud.service",
		"-Xmx",
		"SmallRye",
	} {
		if strings.Contains(ud, banned) {
			t.Errorf("Go backend bootstrap must not contain Java/Quarkus artifact %q", banned)
		}
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
	if !strings.Contains(do, "PYX_QUARKUS_DATASOURCE_JDBC_URL=${var.do_main_db_url}") {
		t.Fatal("backend scale-group did not receive the DO bootstrap on UserDataByProvider[\"digitalocean\"]")
	}
	if comp.ScaleGroup.UserData != "" {
		t.Error("generic UserData must stay empty so non-DO placements are unaffected")
	}
}

// TestRenderBackendDONoFormatSentinels is the renderer regression for the #127
// EONORM heredoc: RenderBackendDOUserData builds lines via fmt.Fprintf(format+"\n"),
// and the jdbc-normalization Python block contains literal `%s`/`%` (the Python
// format operator). If those lines are passed as a format string with no args, Go
// mangles them into `%!s(MISSING)` / `%!(MISSING)` sentinels, producing a broken
// on-box normalizer → the fresh droplet keeps the raw postgres:// URL → pgjdbc
// rejects the scheme → Hibernate fails → crash-loop (the F2-02 symptom). Assert the
// rendered user_data carries NO printf-error sentinels anywhere.
func TestRenderBackendDONoFormatSentinels(t *testing.T) {
	t.Parallel()
	ud, err := RenderBackendDOUserData(BackendBootstrapSpec{Environment: "beta"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, sentinel := range []string{"%!", "MISSING"} {
		if strings.Contains(ud, sentinel) {
			t.Errorf("rendered backend user_data contains a fmt error sentinel %q — a literal %% leaked through fmt.Fprintf", sentinel)
		}
	}
	// The normalizer's format lines must survive VERBATIM.
	for _, want := range []string{
		`out.append("PYX_QUARKUS_DATASOURCE_JDBC_URL=jdbc:postgresql://%s:%s/%s%s" % (host, port, db, q))`,
		`out.append("PYX_QUARKUS_DATASOURCE_USERNAME=%s" % user)`,
		`out.append("PYX_QUARKUS_DATASOURCE_PASSWORD=%s" % pw)`,
	} {
		if !strings.Contains(ud, want) {
			t.Errorf("EONORM normalizer line not emitted verbatim; missing:\n%s", want)
		}
	}
}

// TestRenderBackendDOEONORMExecutesUnderPython3 extracts the rendered EONORM Python
// heredoc and runs it under python3 against a sample /home/main/env holding a libpq
// postgres:// URL, then asserts it produced a valid jdbc:postgresql:// line plus the
// split-out username/password — proving the on-box normalizer actually works (no
// SyntaxError from mangled `%` verbs). Skipped when python3 is not on PATH.
func TestRenderBackendDOEONORMExecutesUnderPython3(t *testing.T) {
	t.Parallel()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not on PATH")
	}
	ud, err := RenderBackendDOUserData(BackendBootstrapSpec{Environment: "beta"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// Extract the body between the `python3 - <<'EONORM'` opener and the `EONORM`
	// terminator (the Python source that runs on the box).
	const opener = "python3 - <<'EONORM'"
	start := strings.Index(ud, opener)
	if start < 0 {
		t.Fatal("EONORM opener not found in rendered user_data")
	}
	body := ud[start+len(opener):]
	end := strings.Index(body, "\nEONORM")
	if end < 0 {
		t.Fatal("EONORM terminator not found in rendered user_data")
	}
	pySrc := strings.TrimPrefix(body[:end], "\n")

	// Provide a sample env with a libpq postgres:// URL for the script to normalize.
	dir := t.TempDir()
	envPath := filepath.Join(dir, "env")
	const sample = "postgres://user:pass@host:25060/mesh_app?sslmode=require"
	if werr := os.WriteFile(envPath, []byte("PYX_QUARKUS_DATASOURCE_JDBC_URL="+sample+"\n"), 0o644); werr != nil {
		t.Fatalf("write sample env: %v", werr)
	}
	// The script hardcodes p = "/home/main/env"; retarget it to our temp file.
	pySrc = strings.Replace(pySrc, `p = "/home/main/env"`, `p = "`+envPath+`"`, 1)

	cmd := exec.Command(py, "-c", pySrc)
	if out, cerr := cmd.CombinedOutput(); cerr != nil {
		t.Fatalf("EONORM python3 execution failed (mangled %%? SyntaxError?): %v\noutput:\n%s\nsource:\n%s", cerr, out, pySrc)
	}

	got, rerr := os.ReadFile(envPath)
	if rerr != nil {
		t.Fatalf("read normalized env: %v", rerr)
	}
	result := string(got)
	for _, want := range []string{
		"PYX_QUARKUS_DATASOURCE_JDBC_URL=jdbc:postgresql://host:25060/mesh_app?sslmode=require",
		"PYX_QUARKUS_DATASOURCE_USERNAME=user",
		"PYX_QUARKUS_DATASOURCE_PASSWORD=pass",
	} {
		if !strings.Contains(result, want) {
			t.Errorf("normalizer output missing %q\ngot:\n%s", want, result)
		}
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

	// The consolidated assembler (assemble.go) now emits the `variable {}`
	// declarations the DO platform bootstraps reference (the union across all six
	// services), so the rendered .tf is already self-contained — the test no longer
	// declares them manually (doing so would duplicate the assembler's output and
	// terraform rejects duplicate variable declarations). We assert the assembler
	// actually declared the backend's referenced vars below.
	joined := strings.Join(docs, "\n")
	plain, sensitive := spec.BackendBootstrapVariableNames()
	for _, name := range append(append([]string{}, plain...), sensitive...) {
		if strings.Contains(joined, "${var."+name+"}") &&
			!strings.Contains(joined, "variable \""+name+"\" {") {
			t.Fatalf("assembler did not declare referenced variable %q", name)
		}
	}

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
