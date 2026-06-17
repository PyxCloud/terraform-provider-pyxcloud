package provider

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/catalog"
	"github.com/hashicorp/terraform-exec/tfexec"
)

// TestAssembledHCLValidatesWithTerraform proves the generated terraform is valid
// against the REAL aws provider schema: assemble an AWS global-component topology
// via catalog.AssembleHCL, prepend the provider header, then `terraform init
// -backend=false` + `terraform validate`. No cloud credentials are touched
// (validate is offline w.r.t. the cloud). Skips if terraform isn't on PATH or the
// provider can't be fetched (no network) — never a silent pass.
func TestAssembledHCLValidatesWithTerraform(t *testing.T) {
	execPath, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("terraform not on PATH; skipping round-trip validate")
	}

	topo := catalog.Topology{
		IAM: []catalog.IAMSpec{{
			Name: "keycloak", Provider: "aws", InstanceProfile: true,
			InlinePolicies:    []catalog.IAMPolicyDoc{{Name: "ssm", Document: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ssm:GetParameter","Resource":"*"}]}`}},
			ManagedPolicyARNs: []string{"arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"},
		}},
		KMS:           &catalog.KMSSpec{Name: "vault", Provider: "aws", Keys: []catalog.KMSKeySpec{{Alias: "unseal", EnableRotation: true}}},
		Email:         &catalog.EmailSpec{Name: "passo", Provider: "aws", Domains: []catalog.EmailDomainSpec{{Domain: "passo.build", EnableDKIM: true}}},
		Observability: &catalog.ObservabilitySpec{Name: "be", Provider: "aws", LogGroups: []catalog.LogGroupSpec{{Name: "/pyx/be", RetentionDays: 30}}, Alarms: []catalog.AlarmSpec{{Name: "cpu", Namespace: "AWS/EC2", MetricName: "CPUUtilization", ComparisonOperator: "gt", Threshold: 80, PeriodSeconds: 300, EvaluationPeriods: 2}}},
		PrefixList:    []catalog.PrefixListSpec{{Name: "office", Provider: "aws", Entries: []string{"87.120.111.232/32"}}},
	}
	hcl, err := catalog.AssembleHCL(context.Background(), nil, topo)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "header.tf"), []byte(providerHeader("aws")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(hcl), 0o644); err != nil {
		t.Fatal(err)
	}

	tf, err := tfexec.NewTerraform(dir, execPath)
	if err != nil {
		t.Fatalf("tfexec: %v", err)
	}
	ctx := context.Background()
	if err := tf.Init(ctx, tfexec.Backend(false)); err != nil {
		t.Skipf("terraform init could not fetch providers (no network?): %v", err)
	}
	out, err := tf.Validate(ctx)
	if err != nil {
		t.Fatalf("terraform validate errored: %v", err)
	}
	if !out.Valid {
		t.Fatalf("generated terraform did NOT validate against the aws provider schema: %+v", out.Diagnostics)
	}
}
