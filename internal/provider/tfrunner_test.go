package provider

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestTFRunnerWriteConfig(t *testing.T) {
	dir := t.TempDir()
	r := &tfRunner{workDir: dir, execPath: "terraform"}

	// Pre-existing stale .tf + a state file that must survive.
	if err := os.WriteFile(filepath.Join(dir, "old.tf"), []byte("# stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "terraform.tfstate"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	docs := []string{"resource \"aws_vpc\" \"v\" {}", "resource \"aws_subnet\" \"s\" {}"}
	if err := r.writeConfig(docs); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}

	for i := range docs {
		name := filepath.Join(dir, "pyx_00"+string(rune('0'+i))+".tf")
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
		if string(b) != docs[i] {
			t.Errorf("%s = %q want %q", name, b, docs[i])
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "old.tf")); !os.IsNotExist(err) {
		t.Error("stale old.tf should have been removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "terraform.tfstate")); err != nil {
		t.Error("terraform.tfstate must survive a re-write")
	}
}

func TestNewTFRunnerMissingTerraform(t *testing.T) {
	t.Setenv("PATH", "")
	_, err := newTFRunner(t.TempDir())
	if err == nil {
		t.Fatal("expected error when terraform is not on PATH")
	}
}

func TestDiscoverAWSImportCandidatesForNamedResources(t *testing.T) {
	docs := []string{`
resource "aws_iam_role" "beta-pyx-api-role" {
  name = "beta-pyx-api-role"
}

resource "aws_iam_instance_profile" "beta-pyx-api-role" {
  name = "beta-pyx-api-role"
  role = aws_iam_role.beta-pyx-api-role.name
}

resource "aws_iam_role_policy" "beta-pyx-api-role-api-s3-cloudwatch-policy" {
  name   = "api-s3-cloudwatch-policy"
  role   = aws_iam_role.beta-pyx-api-role.id
  policy = "{}"
}

resource "aws_iam_role_policy_attachment" "beta-pyx-api-role-managed-1" {
  role       = aws_iam_role.beta-pyx-api-role.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

resource "aws_cloudwatch_log_group" "_pyx_beta_api_application" {
  name = "/pyx/beta/api/application"
}

resource "aws_autoscaling_group" "beta-api_asg" {
  name                = "beta-api"
  min_size            = 1
}
`}

	got := discoverAWSImportCandidates(context.Background(), docs)
	want := map[importCandidate]bool{
		{Address: "aws_iam_role.beta-pyx-api-role", ID: "beta-pyx-api-role"}:                                                                                  true,
		{Address: "aws_iam_instance_profile.beta-pyx-api-role", ID: "beta-pyx-api-role"}:                                                                      true,
		{Address: "aws_iam_role_policy.beta-pyx-api-role-api-s3-cloudwatch-policy", ID: "beta-pyx-api-role:api-s3-cloudwatch-policy"}:                         true,
		{Address: "aws_iam_role_policy_attachment.beta-pyx-api-role-managed-1", ID: "beta-pyx-api-role/arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"}: true,
		{Address: "aws_cloudwatch_log_group._pyx_beta_api_application", ID: "/pyx/beta/api/application"}:                                                      true,
		{Address: "aws_autoscaling_group.beta-api_asg", ID: "beta-api"}:                                                                                       true,
	}

	for _, candidate := range got {
		delete(want, candidate)
	}
	for missing := range want {
		t.Fatalf("missing import candidate: %#v; got %#v", missing, got)
	}
}

func TestDiscoverAWSImportCandidatesSkipsTargetGroupWithoutAWSCLIResolution(t *testing.T) {
	docs := []string{`
resource "aws_lb_target_group" "beta-api-attach_tg" {
  name = "pyxcloud-missing-target-group-for-unit-test"
}
`}

	if got := discoverAWSImportCandidates(context.Background(), docs); len(got) != 0 {
		t.Fatalf("target group should be skipped when ARN cannot be resolved, got %#v", got)
	}
}

func TestAWSSecurityGroupRuleImportCandidates(t *testing.T) {
	hcl := `
resource "aws_security_group_rule" "beta-api-sg_ingress_0" {
  type              = "ingress"
  security_group_id = aws_security_group.beta-api-sg.id
  protocol          = "tcp"
  from_port         = 8080
  to_port           = 8080
  cidr_blocks       = ["0.0.0.0/0"]
}
`
	got := awsSecurityGroupRuleImportCandidates(hcl, map[string]string{"beta-api-sg": "sg-090dcaa930a166d99"})
	if len(got) != 1 {
		t.Fatalf("expected one candidate, got %#v", got)
	}
	want := importCandidate{
		Address: "aws_security_group_rule.beta-api-sg_ingress_0",
		ID:      "sg-090dcaa930a166d99_ingress_tcp_8080_8080_0.0.0.0/0",
	}
	if got[0] != want {
		t.Fatalf("candidate = %#v, want %#v", got[0], want)
	}
}

func TestAWSSecurityGroupRuleImportCandidatesIPv6(t *testing.T) {
	hcl := `
resource "aws_security_group_rule" "beta-api-sg_ingress_0" {
  type              = "ingress"
  security_group_id = aws_security_group.beta-api-sg.id
  protocol          = "tcp"
  from_port         = 8080
  to_port           = 8080
  ipv6_cidr_blocks  = ["::/0"]
}
`
	got := awsSecurityGroupRuleImportCandidates(hcl, map[string]string{"beta-api-sg": "sg-090dcaa930a166d99"})
	if len(got) != 1 {
		t.Fatalf("expected one candidate, got %#v", got)
	}
	want := importCandidate{
		Address: "aws_security_group_rule.beta-api-sg_ingress_0",
		ID:      "sg-090dcaa930a166d99_ingress_tcp_8080_8080_::/0",
	}
	if got[0] != want {
		t.Fatalf("candidate = %#v, want %#v", got[0], want)
	}
}
