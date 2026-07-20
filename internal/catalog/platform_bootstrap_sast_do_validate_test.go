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

// TestSastDOScaleGroupTerraformValidate renders a DO estate whose sast
// scale-group carries the DO bootstrap on UserDataByProvider["digitalocean"] and
// runs terraform init && validate. Proves the heredoc'd DO runner user_data is
// valid, plannable DO HCL. Temporary verification test (zz_ prefix).
func TestSastDOScaleGroupTerraformValidate(t *testing.T) {
	execPath, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("terraform not on PATH")
	}
	doUD, err := RenderSastDOBootstrapUserData(SastDOBootstrapSpec{Environment: "beta"})
	if err != nil {
		t.Fatalf("render sast do: %v", err)
	}
	in := DOBaselineInput("Frankfurt", "x86_64", "ubuntu", "1.30")
	// POST-PURGE (2026-07-10): sast is no longer one of the baseline services
	// (DOBaselineServices() dropped it along with the live sast pool — see
	// do_baseline.go's file-header note), so this test synthesizes its own
	// standalone sast scale-group component, shaped like DOBaselineInput used to
	// emit it, to prove the generic AssembleHCL path still renders valid DO HCL
	// for the sast DO bootstrap independent of the baseline's service set.
	in.Components = append(in.Components, AssembleComponent{
		Name: "sast",
		Type: "virtual-machine-scale-group",
		ScaleGroup: &AssembleScaleGroup{
			Architecture:       "x86_64",
			CPU:                "2",
			RAM:                "4",
			OS:                 "ubuntu",
			Min:                1,
			Max:                1,
			Desired:            1,
			Health:             HealthEC2,
			UserDataByProvider: map[string]string{"digitalocean": doUD},
		},
	})
	docs, err := AssembleHCL(context.Background(), MustEmbedded(), in)
	if err != nil {
		t.Fatalf("AssembleHCL: %v", err)
	}
	joined := strings.Join(docs, "\n")
	if !strings.Contains(joined, "registry.digitalocean.com/pyx-registry/pyx-sast:latest") {
		t.Fatal("rendered HCL missing the sast DO image reference")
	}

	dir := t.TempDir()
	for i, d := range docs {
		if werr := os.WriteFile(filepath.Join(dir, fmt.Sprintf("pyx_%03d.tf", i)), []byte(d), 0o644); werr != nil {
			t.Fatalf("write: %v", werr)
		}
	}
	tf, err := tfexec.NewTerraform(dir, execPath)
	if err != nil {
		t.Fatalf("tfexec: %v", err)
	}
	ctx := context.Background()
	if err := tf.Init(ctx, tfexec.Upgrade(false)); err != nil {
		t.Fatalf("terraform init: %v", err)
	}
	vout, verr := tf.Validate(ctx)
	if verr != nil {
		t.Fatalf("terraform validate: %v", verr)
	}
	if !vout.Valid {
		for _, d := range vout.Diagnostics {
			t.Logf("diag: %s | %s", d.Summary, d.Detail)
		}
		t.Fatalf("INVALID: %d diagnostics", vout.ErrorCount)
	}
	t.Log("terraform init && validate GREEN for DO sast scale-group")
}
