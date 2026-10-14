package engineconsumer

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	engine "github.com/PyxCloud/terraform-provider-pyxcloud/pkg/engine"
)

// The DEP-01.2 done-when, second half: terraform validate on the rendered DO
// environment is green. Skipped honestly when terraform is not on PATH or
// under -short (validate needs `terraform init`, which downloads the
// provider from the registry).
func TestTerraformValidateRenderedDOEnvironment(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: terraform validate needs network for terraform init")
	}
	bin, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("terraform binary not available")
	}
	ctx := context.Background()
	docs, err := engine.Render(ctx, engine.AssembleInput{
		Name:     "wp-consumer",
		Provider: "digitalocean",
		Region:   "Frankfurt",
		Components: []engine.AssembleComponent{{
			Name:       "web",
			Type:       "virtual-machine-scale-group",
			Count:      1,
			ScaleGroup: &engine.AssembleScaleGroup{CPU: "2", RAM: "4", Min: 1, Max: 1, Desired: 1},
		}},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	dir := t.TempDir()
	for i, doc := range docs {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("doc%d.tf", i)), []byte(doc), 0o644); err != nil {
			t.Fatalf("write doc %d: %v", i, err)
		}
	}
	init := exec.Command(bin, "init", "-input=false")
	init.Dir = dir
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("terraform init: %v\n%s", err, out)
	}
	validate := exec.Command(bin, "validate")
	validate.Dir = dir
	out, err := validate.CombinedOutput()
	if err != nil {
		t.Fatalf("terraform validate: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Success") {
		t.Fatalf("unexpected validate output: %s", out)
	}
}
