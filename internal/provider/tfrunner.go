package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

type importCandidate struct {
	Address string
	ID      string
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
	r.adoptExistingAWSResources(ctx, tf, docs)
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

func (r *tfRunner) adoptExistingAWSResources(ctx context.Context, tf *tfexec.Terraform, docs []string) {
	for _, candidate := range discoverAWSImportCandidates(ctx, docs) {
		if candidate.Address == "" || candidate.ID == "" || candidate.ID == "None" {
			continue
		}
		if err := tf.Import(ctx, candidate.Address, candidate.ID); err != nil {
			fmt.Fprintf(os.Stderr, "pyxcloud: skipped terraform import %s %s: %v\n", candidate.Address, candidate.ID, err)
		}
	}
}

func discoverAWSImportCandidates(ctx context.Context, docs []string) []importCandidate {
	var candidates []importCandidate
	seen := map[string]struct{}{}
	add := func(address, id string) {
		id = strings.TrimSpace(id)
		if address == "" || id == "" || id == "None" {
			return
		}
		key := address + "\x00" + id
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, importCandidate{Address: address, ID: id})
	}

	all := strings.Join(docs, "\n")
	for _, m := range resourceNameRE("aws_iam_role").FindAllStringSubmatch(all, -1) {
		add("aws_iam_role."+m[1], m[2])
	}
	for _, m := range resourceNameRE("aws_iam_instance_profile").FindAllStringSubmatch(all, -1) {
		add("aws_iam_instance_profile."+m[1], m[2])
	}
	for _, m := range resourceNameRE("aws_cloudwatch_log_group").FindAllStringSubmatch(all, -1) {
		add("aws_cloudwatch_log_group."+m[1], m[2])
	}
	for _, m := range resourceNameRE("aws_autoscaling_group").FindAllStringSubmatch(all, -1) {
		add("aws_autoscaling_group."+m[1], m[2])
	}
	for _, m := range resourceNameRE("aws_lb_target_group").FindAllStringSubmatch(all, -1) {
		if arn := awsOutput(ctx, "elbv2", "describe-target-groups", "--names", m[2], "--query", "TargetGroups[0].TargetGroupArn", "--output", "text"); arn != "" {
			add("aws_lb_target_group."+m[1], arn)
		}
	}
	for _, m := range iamRolePolicyRE.FindAllStringSubmatch(all, -1) {
		add("aws_iam_role_policy."+m[1], m[3]+":"+m[2])
	}
	for _, m := range iamRolePolicyAttachmentRE.FindAllStringSubmatch(all, -1) {
		add("aws_iam_role_policy_attachment."+m[1], m[2]+"/"+m[3])
	}
	for _, m := range lbListenerRuleRE.FindAllStringSubmatch(all, -1) {
		query := fmt.Sprintf("Rules[?Priority=='%s'].RuleArn | [0]", m[3])
		if arn := awsOutput(ctx, "elbv2", "describe-rules", "--listener-arn", m[2], "--query", query, "--output", "text"); arn != "" {
			add("aws_lb_listener_rule."+m[1], arn)
		}
	}

	return candidates
}

func resourceNameRE(resourceType string) *regexp.Regexp {
	return regexp.MustCompile(`(?s)resource\s+"` + regexp.QuoteMeta(resourceType) + `"\s+"([^"]+)"\s+\{.*?\n\s+name\s+=\s+"([^"]+)"`)
}

var (
	iamRolePolicyRE           = regexp.MustCompile(`(?s)resource\s+"aws_iam_role_policy"\s+"([^"]+)"\s+\{.*?\n\s+name\s+=\s+"([^"]+)".*?\n\s+role\s+=\s+aws_iam_role\.([A-Za-z0-9_-]+)\.id`)
	iamRolePolicyAttachmentRE = regexp.MustCompile(`(?s)resource\s+"aws_iam_role_policy_attachment"\s+"([^"]+)"\s+\{.*?\n\s+role\s+=\s+aws_iam_role\.([A-Za-z0-9_-]+)\.name.*?\n\s+policy_arn\s+=\s+"([^"]+)"`)
	lbListenerRuleRE          = regexp.MustCompile(`(?s)resource\s+"aws_lb_listener_rule"\s+"([^"]+)"\s+\{.*?\n\s+listener_arn\s+=\s+"([^"]+)".*?\n\s+priority\s+=\s+([0-9]+)`)
)

func awsOutput(ctx context.Context, args ...string) string {
	if _, err := exec.LookPath("aws"); err != nil {
		return ""
	}
	cmd := exec.CommandContext(ctx, "aws", args...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
