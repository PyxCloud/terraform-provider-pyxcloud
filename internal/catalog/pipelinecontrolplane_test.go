package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestTranslatePipelineControlPlaneAWS asserts the pyx-lambda control-plane plan:
// region resolved, defaults applied, and the closed set of control-plane resource
// types enumerated in render order.
func TestTranslatePipelineControlPlaneAWS(t *testing.T) {
	t.Parallel()
	plan, err := TranslatePipelineControlPlane(context.Background(), MustEmbedded(), PipelineControlPlaneSpec{
		Name: "pyx-ci", Region: "Frankfurt", Provider: "aws", PipelineName: "ci",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSPRegion != "eu-central-1" {
		t.Errorf("csp_region = %q, want eu-central-1", plan.CSPRegion)
	}
	if plan.RunnerMemoryMB != defaultRunnerMemoryMB || plan.RunnerTimeoutSecs != defaultRunnerTimeoutSecs {
		t.Errorf("runner defaults not applied: mem=%d timeout=%d", plan.RunnerMemoryMB, plan.RunnerTimeoutSecs)
	}
	if plan.FargateCPU != defaultFargateCPU || plan.CodeBuildCompute != defaultCodeBuildCompute {
		t.Errorf("fargate/codebuild defaults not applied: %q %q", plan.FargateCPU, plan.CodeBuildCompute)
	}
	want := []string{
		"aws_iam_role", "aws_iam_role", "aws_iam_role", "aws_iam_role",
		"aws_lambda_function", "aws_ecs_cluster", "aws_codebuild_project", "aws_sfn_state_machine",
	}
	if strings.Join(plan.ResourceTypes, ",") != strings.Join(want, ",") {
		t.Errorf("resource_types = %v, want %v", plan.ResourceTypes, want)
	}
}

// TestRenderPipelineControlPlaneAWS asserts the rendered HCL carries the whole
// control-plane closed set and the out-of-band ASL variable when none is supplied.
func TestRenderPipelineControlPlaneAWS(t *testing.T) {
	t.Parallel()
	plan, err := TranslatePipelineControlPlane(context.Background(), MustEmbedded(), PipelineControlPlaneSpec{
		Name: "pyx-ci", Region: "Frankfurt", Provider: "aws", PipelineName: "ci",
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderPipelineControlPlaneHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "aws_lambda_function" "pyx_ci_runner"`,
		`resource "aws_ecs_cluster" "pyx_ci"`,
		`capacity_providers = ["FARGATE", "FARGATE_SPOT"]`,
		`resource "aws_codebuild_project" "pyx_ci"`,
		`privileged_mode = true`,
		`resource "aws_sfn_state_machine" "pyx_ci"`,
		`resource "aws_iam_role" "pyx_ci_sfn"`,
		`"lambda:InvokeFunction"`,
		`variable "pyx_ci_asl"`, // ASL supplied out-of-band by pyx-pipeline-ir
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("AWS control-plane HCL missing %q\n%s", want, hcl)
		}
	}
	// No GitHub OIDC by default.
	if strings.Contains(hcl, "aws_iam_openid_connect_provider") {
		t.Error("OIDC provider must NOT be emitted unless github_oidc is set")
	}
}

// TestPipelineControlPlaneInlineASL asserts a supplied ASL is embedded inline (no
// out-of-band variable) — the dogfood path that wires the real compiled aws/ci.json.
func TestPipelineControlPlaneInlineASL(t *testing.T) {
	t.Parallel()
	asl := `{"StartAt":"a","States":{"a":{"Type":"Pass","End":true}}}`
	plan, err := TranslatePipelineControlPlane(context.Background(), MustEmbedded(), PipelineControlPlaneSpec{
		Name: "ci", Region: "Frankfurt", Provider: "aws", StateMachineDefinition: asl,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.HasStateMachineDefinition {
		t.Fatal("expected HasStateMachineDefinition=true")
	}
	hcl, err := RenderPipelineControlPlaneHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hcl, `variable "ci_asl"`) {
		t.Error("inline ASL must not declare an out-of-band variable")
	}
	// The ASL is embedded as a %q-escaped HCL string, wrapped in replace() to inject
	// the runner Lambda ARN in place of the compiler's ${PyxRunnerLambdaArn} placeholder.
	if !strings.Contains(hcl, `\"StartAt\":\"a\"`) {
		t.Errorf("inline ASL not embedded\n%s", hcl)
	}
	if !strings.Contains(hcl, `replace(`) || !strings.Contains(hcl, `aws_lambda_function.ci_runner.arn`) {
		t.Errorf("inline ASL must be wrapped in replace() injecting the runner ARN\n%s", hcl)
	}
}

// TestPipelineControlPlaneOIDC asserts the GitHub OIDC identity + repo-scoped CI
// role are emitted (and trust is never wildcarded).
func TestPipelineControlPlaneOIDC(t *testing.T) {
	t.Parallel()
	plan, err := TranslatePipelineControlPlane(context.Background(), MustEmbedded(), PipelineControlPlaneSpec{
		Name: "ci", Region: "Frankfurt", Provider: "aws",
		GitHubOIDC: true, GitHubOwnerRepo: "PyxCloud/terraform-provider-pyxcloud",
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderPipelineControlPlaneHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "aws_iam_openid_connect_provider" "ci_github"`,
		`url             = "https://token.actions.githubusercontent.com"`,
		`"token.actions.githubusercontent.com:sub" = "repo:PyxCloud/terraform-provider-pyxcloud:*"`,
		`"states:StartExecution"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("OIDC HCL missing %q\n%s", want, hcl)
		}
	}
}

// TestPipelineControlPlaneOIDCRequiresRepo asserts a hard plan-time error when OIDC
// is enabled without a repo scope — never a wildcarded trust.
func TestPipelineControlPlaneOIDCRequiresRepo(t *testing.T) {
	t.Parallel()
	_, err := TranslatePipelineControlPlane(context.Background(), MustEmbedded(), PipelineControlPlaneSpec{
		Name: "ci", Region: "Frankfurt", Provider: "aws", GitHubOIDC: true,
	})
	if err == nil || !strings.Contains(err.Error(), "github_owner_repo") {
		t.Errorf("expected github_owner_repo required error, got %v", err)
	}
}

// TestPipelineControlPlaneRunnerTimeoutCap asserts the Lambda 15-minute cap is
// enforced deterministically at plan time.
func TestPipelineControlPlaneRunnerTimeoutCap(t *testing.T) {
	t.Parallel()
	_, err := TranslatePipelineControlPlane(context.Background(), MustEmbedded(), PipelineControlPlaneSpec{
		Name: "ci", Region: "Frankfurt", Provider: "aws", RunnerTimeoutSecs: 1800,
	})
	if err == nil || !strings.Contains(err.Error(), "900") {
		t.Errorf("expected runner timeout cap error, got %v", err)
	}
}

// TestPipelineControlPlaneUnsupportedProvider asserts the pyx-lambda backend is
// AWS-specific: every other provider surfaces a clean ErrComponentUnsupported.
func TestPipelineControlPlaneUnsupportedProvider(t *testing.T) {
	t.Parallel()
	for _, prov := range []string{"gcp", "digitalocean", "azure"} {
		_, err := TranslatePipelineControlPlane(context.Background(), MustEmbedded(), PipelineControlPlaneSpec{
			Name: "ci", Region: "Frankfurt", Provider: prov,
		})
		var unsup ErrComponentUnsupported
		if !errors.As(err, &unsup) {
			t.Fatalf("provider %q: expected ErrComponentUnsupported, got %v", prov, err)
		}
		if unsup.Component != TypePipelineControlPlane {
			t.Errorf("component = %q, want %q", unsup.Component, TypePipelineControlPlane)
		}
	}
}

func TestCanonicalPipelineControlPlaneType(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"pipeline-control-plane", "pyx-lambda-control-plane", "pipeline-runner", "Pyx-Lambda-Control-Plane"} {
		if got, ok := CanonicalPipelineControlPlaneType(in); !ok || got != TypePipelineControlPlane {
			t.Errorf("CanonicalPipelineControlPlaneType(%q) = %q,%v", in, got, ok)
		}
	}
	if _, ok := CanonicalPipelineControlPlaneType("virtual-machine"); ok {
		t.Error("vm should not be a pipeline-control-plane type")
	}
}
