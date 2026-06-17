package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hashicorp/terraform-exec/tfexec"
)

// tfRunner executes the backend-translated concrete terraform in a work dir,
// inheriting the calling process environment so the cloud providers resolve
// credentials from the standard env-var chain (AWS_ACCESS_KEY_ID / AWS_PROFILE /
// AWS_REGION, GOOGLE_APPLICATION_CREDENTIALS, DIGITALOCEAN_TOKEN, …) — exactly how
// the per-provider terraform scripts authenticate today. This is Mode A of
// DEPLOY-GATE.md: no accountBinding, no backend-side credentials, no raw secrets
// over the API; the cloud's own IAM (via the ambient env) is the authorization.
//
// State lives in workDir (local backend), so the dir must be stable across the
// resource's plan/apply/refresh/destroy lifecycle — the resource keeps it in state.
type tfRunner struct {
	workDir  string
	execPath string
}

// newTFRunner locates the terraform binary on PATH and prepares the work dir.
func newTFRunner(workDir string) (*tfRunner, error) {
	execPath, err := exec.LookPath("terraform")
	if err != nil {
		return nil, fmt.Errorf("terraform binary not found on PATH: the pyxcloud_environment resource runs the "+
			"backend-translated terraform locally with your provider env credentials, so a terraform executable is "+
			"required (install it or add it to PATH): %w", err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating work dir %q: %w", workDir, err)
	}
	return &tfRunner{workDir: workDir, execPath: execPath}, nil
}

// writeConfig writes each translated HCL document as NN.tf in the work dir,
// replacing any prior generated files so re-applies reflect the current plan.
func (r *tfRunner) writeConfig(docs []string) error {
	// Clear previously generated .tf files (keep terraform.tfstate and .terraform/).
	entries, _ := os.ReadDir(r.workDir)
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".tf") {
			_ = os.Remove(filepath.Join(r.workDir, e.Name()))
		}
	}
	for i, doc := range docs {
		name := filepath.Join(r.workDir, fmt.Sprintf("pyx_%03d.tf", i))
		if err := os.WriteFile(name, []byte(doc), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", name, err)
		}
	}
	return nil
}

func (r *tfRunner) tf() (*tfexec.Terraform, error) {
	tf, err := tfexec.NewTerraform(r.workDir, r.execPath)
	if err != nil {
		return nil, err
	}
	// tfexec inherits the parent process environment by default, so AWS_* / GOOGLE_* /
	// DIGITALOCEAN_TOKEN flow through to the cloud providers. We do NOT call SetEnv
	// (which would replace, not augment, the env). Surface terraform's own logs.
	tf.SetStdout(os.Stderr)
	tf.SetStderr(os.Stderr)
	return tf, nil
}

// apply writes the config, inits, and applies. Returns the terraform outputs as a
// flat string map (JSON-encoded values).
func (r *tfRunner) apply(ctx context.Context, docs []string) (map[string]string, error) {
	if err := r.writeConfig(docs); err != nil {
		return nil, err
	}
	tf, err := r.tf()
	if err != nil {
		return nil, err
	}
	if err := tf.Init(ctx, tfexec.Upgrade(false)); err != nil {
		return nil, fmt.Errorf("terraform init: %w", err)
	}
	if err := tf.Apply(ctx); err != nil {
		return nil, fmt.Errorf("terraform apply: %w", err)
	}
	return r.outputs(ctx, tf)
}

// refresh reads current outputs without changing infrastructure (best-effort).
func (r *tfRunner) refresh(ctx context.Context) (map[string]string, error) {
	tf, err := r.tf()
	if err != nil {
		return nil, err
	}
	// If the work dir was never initialized (e.g. a fresh machine), there is nothing
	// to read; treat as empty rather than erroring the whole Read.
	if _, statErr := os.Stat(filepath.Join(r.workDir, ".terraform")); statErr != nil {
		return map[string]string{}, nil
	}
	return r.outputs(ctx, tf)
}

// destroy tears the environment down.
func (r *tfRunner) destroy(ctx context.Context) error {
	tf, err := r.tf()
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(filepath.Join(r.workDir, ".terraform")); statErr != nil {
		// Never initialized → nothing provisioned.
		return nil
	}
	if err := tf.Init(ctx, tfexec.Upgrade(false)); err != nil {
		return fmt.Errorf("terraform init (destroy): %w", err)
	}
	if err := tf.Destroy(ctx); err != nil {
		return fmt.Errorf("terraform destroy: %w", err)
	}
	return nil
}

func (r *tfRunner) outputs(ctx context.Context, tf *tfexec.Terraform) (map[string]string, error) {
	raw, err := tf.Output(ctx)
	if err != nil {
		return nil, fmt.Errorf("terraform output: %w", err)
	}
	out := make(map[string]string, len(raw))
	for k, meta := range raw {
		// meta.Value is JSON; for a string value strip the quotes, else keep JSON.
		var s string
		if json.Unmarshal(meta.Value, &s) == nil {
			out[k] = s
		} else {
			out[k] = string(meta.Value)
		}
	}
	return out, nil
}
