package provider

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
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

func TestAWSSecurityGroupRuleImportCandidatesMultipleCIDRSources(t *testing.T) {
	hcl := `
resource "aws_security_group_rule" "beta-api-sg_ingress_0" {
  type              = "ingress"
  security_group_id = aws_security_group.beta-api-sg.id
  protocol          = "tcp"
  from_port         = 8080
  to_port           = 8080
  cidr_blocks       = ["0.0.0.0/0"]
  ipv6_cidr_blocks  = ["::/0"]
}
`
	got := awsSecurityGroupRuleImportCandidates(hcl, map[string]string{"beta-api-sg": "sg-090dcaa930a166d99"})
	want := map[importCandidate]bool{
		{Address: "aws_security_group_rule.beta-api-sg_ingress_0", ID: "sg-090dcaa930a166d99_ingress_tcp_8080_8080_0.0.0.0/0"}: true,
		{Address: "aws_security_group_rule.beta-api-sg_ingress_0", ID: "sg-090dcaa930a166d99_ingress_tcp_8080_8080_::/0"}:      true,
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d candidates, got %#v", len(want), got)
	}
	for _, candidate := range got {
		if !want[candidate] {
			t.Fatalf("unexpected candidate %#v; got %#v", candidate, got)
		}
	}
}

// TestAWSSecurityGroupRuleImportCandidatesExternalPeer covers the #63 feature shape:
// a rule scoped to an external SG id literal must be ADOPTED (importable), not skipped
// — otherwise apply re-creates it and hits InvalidPermission.Duplicate.
func TestAWSSecurityGroupRuleImportCandidatesExternalPeer(t *testing.T) {
	hcl := `
resource "aws_security_group_rule" "beta-api-sg_ingress_1" {
  type              = "ingress"
  security_group_id = aws_security_group.beta-api-sg.id
  protocol          = "tcp"
  from_port         = 8080
  to_port           = 8080
  source_security_group_id = "sg-0bda7f6dc31d1c7c0"
}
`
	got := awsSecurityGroupRuleImportCandidates(hcl, map[string]string{"beta-api-sg": "sg-090dcaa930a166d99"})
	want := importCandidate{
		Address: "aws_security_group_rule.beta-api-sg_ingress_1",
		ID:      "sg-090dcaa930a166d99_ingress_tcp_8080_8080_sg-0bda7f6dc31d1c7c0",
	}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("external-peer candidate = %#v, want one %#v", got, want)
	}
}

// TestAWSSecurityGroupRuleImportCandidatesInPlanPeer covers an in-plan source SG
// reference (aws_security_group.<name>.id) resolved via the sgIDs map.
func TestAWSSecurityGroupRuleImportCandidatesInPlanPeer(t *testing.T) {
	hcl := `
resource "aws_security_group_rule" "app-sg_ingress_1" {
  type              = "ingress"
  security_group_id = aws_security_group.app-sg.id
  protocol          = "tcp"
  from_port         = 8080
  to_port           = 8080
  source_security_group_id = aws_security_group.lb.id
}
`
	got := awsSecurityGroupRuleImportCandidates(hcl, map[string]string{"app-sg": "sg-app", "lb": "sg-lb"})
	want := importCandidate{
		Address: "aws_security_group_rule.app-sg_ingress_1",
		ID:      "sg-app_ingress_tcp_8080_8080_sg-lb",
	}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("in-plan-peer candidate = %#v, want one %#v", got, want)
	}
}

// TestSGRuleKeyNormalization: the all/-1 protocol drops ports (AWS reports -1/-1 while
// HCL says 0/0) and the "sg:" source prefix is stripped, so desired (HCL) and actual
// (AWS) keys compare equal.
func TestSGRuleKeyNormalization(t *testing.T) {
	if a, b := sgRuleKey("egress", "-1", "0", "0", "0.0.0.0/0"), sgRuleKey("egress", "-1", "-1", "-1", "0.0.0.0/0"); a != b {
		t.Fatalf("all-proto egress keys must match across 0/0 vs -1/-1: %q != %q", a, b)
	}
	if a, b := sgRuleKey("ingress", "tcp", "8080", "8080", "sg:sg-abc"), sgRuleKey("ingress", "tcp", "8080", "8080", "sg-abc"); a != b {
		t.Fatalf("peer source key must be sg:-prefix-insensitive: %q != %q", a, b)
	}
	// A real tcp rule must keep its ports distinct.
	if a, b := sgRuleKey("ingress", "tcp", "8080", "8080", "0.0.0.0/0"), sgRuleKey("ingress", "tcp", "443", "443", "0.0.0.0/0"); a == b {
		t.Fatal("distinct tcp ports must produce distinct keys")
	}
}

// TestSGRulePruneRevokesOnlyOrphans is the lifecycle fix: after a port is moved from a
// public expose to an ALB-scoped peer rule, the prune diff must revoke ONLY the
// orphaned 0.0.0.0/0 + ::/0 ingress — keeping the peer ingress and the default egress.
func TestSGRulePruneRevokesOnlyOrphans(t *testing.T) {
	hcl := `
resource "aws_security_group" "app-sg" {
  name        = "app-sg"
}

resource "aws_security_group_rule" "app-sg_egress_0" {
  type              = "egress"
  security_group_id = aws_security_group.app-sg.id
  protocol          = "-1"
  from_port         = 0
  to_port           = 0
  cidr_blocks       = ["0.0.0.0/0"]
  ipv6_cidr_blocks  = ["::/0"]
}

resource "aws_security_group_rule" "app-sg_ingress_1" {
  type              = "ingress"
  security_group_id = aws_security_group.app-sg.id
  protocol          = "tcp"
  from_port         = 8080
  to_port           = 8080
  source_security_group_id = "sg-alb"
}
`
	desired := desiredSGRuleKeys(parseSGRules(hcl, map[string]string{"app-sg": "sg-app"}), "sg-app")
	actual := []actualSGRule{
		{id: "sgr-peer", egress: false, key: sgRuleKey("ingress", "tcp", "8080", "8080", "sg:sg-alb")}, // keep (desired)
		{id: "sgr-egress4", egress: true, key: sgRuleKey("egress", "-1", "-1", "-1", "0.0.0.0/0")},     // keep (egress)
		{id: "sgr-egress6", egress: true, key: sgRuleKey("egress", "-1", "-1", "-1", "::/0")},          // keep (egress)
		{id: "sgr-pub4", egress: false, key: sgRuleKey("ingress", "tcp", "8080", "8080", "0.0.0.0/0")}, // ORPHAN
		{id: "sgr-pub6", egress: false, key: sgRuleKey("ingress", "tcp", "8080", "8080", "::/0")},      // ORPHAN
	}
	got := map[string]bool{}
	for _, o := range rulesToRevoke(desired, actual) {
		got[o.id] = true
	}
	if len(got) != 2 || !got["sgr-pub4"] || !got["sgr-pub6"] {
		t.Fatalf("expected to revoke only sgr-pub4 + sgr-pub6, got %#v", got)
	}
	// Safety: an SG with no parsed desired rules yields an empty set (prune skips it).
	if len(desiredSGRuleKeys(parseSGRules(hcl, map[string]string{"app-sg": "sg-app"}), "sg-unknown")) != 0 {
		t.Fatal("desired keys for an unmanaged SG must be empty")
	}
}

func TestDiscoverAWSImportCandidatesAdoptsSecurityGroupsOutsideDefaultVPC(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test creates a POSIX shell fake aws executable")
	}
	binDir := t.TempDir()
	awsPath := filepath.Join(binDir, "aws")
	script := `#!/bin/sh
case "$*" in
  *"describe-security-groups"*)
    printf 'sg-090dcaa930a166d99'
    ;;
  *)
    exit 1
    ;;
esac
`
	if err := os.WriteFile(awsPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	docs := []string{`
resource "aws_security_group" "beta-api-sg" {
  name = "beta-api-sg"
}

resource "aws_security_group_rule" "beta-api-sg_ingress_0" {
  type              = "ingress"
  security_group_id = aws_security_group.beta-api-sg.id
  protocol          = "tcp"
  from_port         = 8080
  to_port           = 8080
  cidr_blocks       = ["0.0.0.0/0"]
}
`}

	got := discoverAWSImportCandidates(context.Background(), docs)
	want := map[importCandidate]bool{
		{Address: "aws_security_group.beta-api-sg", ID: "sg-090dcaa930a166d99"}:                                                true,
		{Address: "aws_security_group_rule.beta-api-sg_ingress_0", ID: "sg-090dcaa930a166d99_ingress_tcp_8080_8080_0.0.0.0/0"}: true,
	}
	for _, candidate := range got {
		delete(want, candidate)
	}
	for missing := range want {
		t.Fatalf("missing import candidate: %#v; got %#v", missing, got)
	}
}

func TestTFRunnerPlan(t *testing.T) {
	dir := t.TempDir()
	r, err := newTFRunner(dir)
	if err != nil {
		t.Skipf("skipping TestTFRunnerPlan since terraform is not on PATH or setup failed: %v", err)
	}

	docs := []string{
		`resource "terraform_data" "foo" {
  input = "bar"
}`,
	}

	ctx := context.Background()
	hasChanges, rawPlan, parsedPlan, err := r.plan(ctx, docs)
	if err != nil {
		t.Fatalf("plan failed: %v", err)
	}

	if !hasChanges {
		t.Error("expected plan to have changes")
	}

	if rawPlan == "" {
		t.Error("expected non-empty raw plan output")
	}

	if parsedPlan == nil {
		t.Fatal("expected non-nil parsed plan representation")
	}

	found := false
	for _, rc := range parsedPlan.ResourceChanges {
		if rc.Address == "terraform_data.foo" {
			found = true
			if len(rc.Change.Actions) != 1 || rc.Change.Actions[0] != "create" {
				t.Errorf("expected create action, got %v", rc.Change.Actions)
			}
		}
	}
	if !found {
		t.Error("expected to find resource change for terraform_data.foo")
	}
}

