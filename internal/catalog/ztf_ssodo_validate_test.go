package catalog

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSSODOTerraformValidate renders a minimal DigitalOcean estate whose sso
// scale-group carries the DO user_data override, writes the HCL, and runs
// `terraform validate`. Skipped when terraform is not on PATH. This is a helper
// test used to prove `terraform validate` is green for the DO render; it is not
// part of the normal unit surface (guarded by the terraform binary + a short
// timeout).
func TestSSODOTerraformValidate(t *testing.T) {
	// Requires terraform + network (init downloads the DO provider), so it is
	// opt-in: set PYX_TF_VALIDATE=1 to run it. The wiring/assembler proofs
	// (TestWithSSODOUserDataWiresOnlySSO / TestSSODOUserDataRendersOnDigitalOcean)
	// cover the catalog invariant with no external dependency.
	if os.Getenv("PYX_TF_VALIDATE") == "" {
		t.Skip("set PYX_TF_VALIDATE=1 to run the terraform validate proof")
	}
	if _, err := exec.LookPath("terraform"); err != nil {
		t.Skip("terraform not on PATH")
	}
	ud, err := RenderSSODOBootstrapUserData(validSSODOSpec())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	comps := WithSSODOUserData(PlatformScaleGroupComponents("", "", ""), ud)
	in := AssembleInput{
		Name: "sso-do-validate", Provider: ProviderDigitalOcean, Region: "Frankfurt",
		Expose: []int{443}, Components: comps,
	}
	docs, err := AssembleHCL(context.Background(), MustEmbedded(), in)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(strings.Join(docs, "\n\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-backend=false"}, {"validate"}} {
		cmd := exec.Command("terraform", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("terraform %v failed: %v\n%s", args, err, out)
		}
	}
}
