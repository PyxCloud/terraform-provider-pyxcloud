package catalog

import (
	"context"
	"strings"
	"testing"
)

func TestTranslateSecurityGroupAWS(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateSecurityGroup(context.Background(), cat, SecurityGroupSpec{
		Name:     "web",
		Network:  "production",
		Region:   "Dublin",
		Provider: "aws",
		Expose:   []int{443, 80}, // unsorted on purpose -> deterministic sort
		Rules: []SecurityRule{
			{Direction: "ingress", Protocol: "tcp", FromPort: 22, ToPort: 22, CIDRs: []string{"10.0.0.0/16"}},
			{Direction: "egress", Protocol: "all", CIDRs: []string{"0.0.0.0/0"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSPRegion != "eu-west-1" {
		t.Errorf("csp_region = %q, want eu-west-1", plan.CSPRegion)
	}
	if plan.ResourceType != "aws_security_group" {
		t.Errorf("resource_type = %q, want aws_security_group", plan.ResourceType)
	}
	// 2 expose (80,443) + 2 explicit = 4 rules; expose sorted ascending first.
	if len(plan.Rules) != 4 {
		t.Fatalf("want 4 rules, got %d", len(plan.Rules))
	}
	if plan.Rules[0].FromPort != 80 || plan.Rules[1].FromPort != 443 {
		t.Errorf("expose ports not sorted ascending: %d, %d", plan.Rules[0].FromPort, plan.Rules[1].FromPort)
	}
	// Expose opens IPv4 + IPv6 from anywhere.
	if len(plan.Rules[0].CIDRs) != 2 {
		t.Errorf("expose rule should open v4+v6, got %v", plan.Rules[0].CIDRs)
	}
}

func TestTranslateSecurityGroupGCP(t *testing.T) {
	t.Parallel()
	plan, err := TranslateSecurityGroup(context.Background(), MustEmbedded(), SecurityGroupSpec{
		Name: "fw", Region: "Belgium", Provider: "gcp", Expose: []int{80},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSPRegion != "europe-west1" {
		t.Errorf("csp_region = %q, want europe-west1", plan.CSPRegion)
	}
	if plan.ResourceType != "google_compute_firewall" {
		t.Errorf("resource_type = %q, want google_compute_firewall", plan.ResourceType)
	}
}

func TestTranslateSecurityGroupDO(t *testing.T) {
	t.Parallel()
	plan, err := TranslateSecurityGroup(context.Background(), MustEmbedded(), SecurityGroupSpec{
		Name: "edge-fw", Region: "Amsterdam", Provider: "digitalocean", Expose: []int{443},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSP != "do" {
		t.Errorf("csp = %q, want do", plan.CSP)
	}
	if plan.ResourceType != "digitalocean_firewall" {
		t.Errorf("resource_type = %q, want digitalocean_firewall", plan.ResourceType)
	}
}

// TestSecurityGroupDescriptionASCIIOnly is the regression guard for the real
// incident: AWS rejects non-ASCII security-group descriptions. The translated
// (and rendered) description MUST be ASCII-only.
func TestSecurityGroupDescriptionASCIIOnly(t *testing.T) {
	t.Parallel()
	plan, err := TranslateSecurityGroup(context.Background(), MustEmbedded(), SecurityGroupSpec{
		Name:        "web",
		Region:      "Dublin",
		Provider:    "aws",
		Description: "Frankfurt édge — naïve café ☕ wÿrd 日本語",
		Expose:      []int{80},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !IsASCII(plan.Description) {
		t.Fatalf("translated description is not ASCII: %q", plan.Description)
	}
	// And the rendered HCL must be ASCII-only too.
	hcl, err := RenderSGHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !IsASCII(hcl) {
		t.Fatalf("rendered HCL is not ASCII:\n%s", hcl)
	}
	// Non-ASCII runes are stripped, ASCII content survives.
	if !strings.Contains(plan.Description, "Frankfurt") || strings.ContainsAny(plan.Description, "é日") {
		t.Errorf("unexpected sanitised description: %q", plan.Description)
	}
}

func TestSecurityGroupMissingRegionIsHardError(t *testing.T) {
	t.Parallel()
	// Dublin has no DigitalOcean entry -> plan-time error, never a fallback.
	_, err := TranslateSecurityGroup(context.Background(), MustEmbedded(), SecurityGroupSpec{
		Region: "Dublin", Provider: "digitalocean", Expose: []int{80},
	})
	if err == nil {
		t.Fatal("expected hard error for Dublin/digitalocean, got nil")
	}
}

func TestSecurityGroupValidation(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	cases := []struct {
		name string
		spec SecurityGroupSpec
	}{
		{"missing region", SecurityGroupSpec{Provider: "aws", Expose: []int{80}}},
		{"missing provider", SecurityGroupSpec{Region: "Dublin", Expose: []int{80}}},
		{"unknown provider", SecurityGroupSpec{Region: "Dublin", Provider: "vultr", Expose: []int{80}}},
		{"empty rules and expose", SecurityGroupSpec{Region: "Dublin", Provider: "aws"}},
		{"expose port out of range", SecurityGroupSpec{Region: "Dublin", Provider: "aws", Expose: []int{70000}}},
		{"bad direction", SecurityGroupSpec{Region: "Dublin", Provider: "aws", Rules: []SecurityRule{{Direction: "sideways", Protocol: "tcp", FromPort: 1, ToPort: 1, CIDRs: []string{"0.0.0.0/0"}}}}},
		{"bad protocol", SecurityGroupSpec{Region: "Dublin", Provider: "aws", Rules: []SecurityRule{{Direction: "ingress", Protocol: "sctp", FromPort: 1, ToPort: 1, CIDRs: []string{"0.0.0.0/0"}}}}},
		{"both cidr and sg", SecurityGroupSpec{Region: "Dublin", Provider: "aws", Rules: []SecurityRule{{Direction: "ingress", Protocol: "tcp", FromPort: 1, ToPort: 1, CIDRs: []string{"0.0.0.0/0"}, SourceSG: "peer"}}}},
		{"neither cidr nor sg", SecurityGroupSpec{Region: "Dublin", Provider: "aws", Rules: []SecurityRule{{Direction: "ingress", Protocol: "tcp", FromPort: 1, ToPort: 1}}}},
		{"bad cidr", SecurityGroupSpec{Region: "Dublin", Provider: "aws", Rules: []SecurityRule{{Direction: "ingress", Protocol: "tcp", FromPort: 1, ToPort: 1, CIDRs: []string{"nope"}}}}},
		{"to<from", SecurityGroupSpec{Region: "Dublin", Provider: "aws", Rules: []SecurityRule{{Direction: "ingress", Protocol: "tcp", FromPort: 100, ToPort: 50, CIDRs: []string{"0.0.0.0/0"}}}}},
	}
	for _, c := range cases {
		if _, err := TranslateSecurityGroup(context.Background(), cat, c.spec); err == nil {
			t.Errorf("%s: expected validation error, got nil", c.name)
		}
	}
}

// TestSecurityGroupRuleLimitAWS asserts exceeding the per-direction rule cap is
// a hard plan-time error (never a silent trim).
func TestSecurityGroupRuleLimitAWS(t *testing.T) {
	t.Parallel()
	rules := make([]SecurityRule, 0, awsRulesPerDirectionMax+1)
	for i := 0; i <= awsRulesPerDirectionMax; i++ {
		rules = append(rules, SecurityRule{
			Direction: "ingress", Protocol: "tcp", FromPort: 1000 + i, ToPort: 1000 + i,
			CIDRs: []string{"10.0.0.0/16"},
		})
	}
	_, err := TranslateSecurityGroup(context.Background(), MustEmbedded(), SecurityGroupSpec{
		Name: "x", Region: "Dublin", Provider: "aws", Rules: rules,
	})
	if err == nil {
		t.Fatal("expected rule-limit error for >60 ingress rules on AWS, got nil")
	}
	if !strings.Contains(err.Error(), "exceed") {
		t.Errorf("expected a limit-exceeded error, got %v", err)
	}
}

func TestSourceSGRuleTranslates(t *testing.T) {
	t.Parallel()
	plan, err := TranslateSecurityGroup(context.Background(), MustEmbedded(), SecurityGroupSpec{
		Name: "app", Region: "Dublin", Provider: "aws",
		Rules: []SecurityRule{
			{Direction: "ingress", Protocol: "tcp", FromPort: 8080, ToPort: 8080, SourceSG: "lb"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Rules) != 1 || plan.Rules[0].SourceSG != "lb" {
		t.Fatalf("source_sg rule not preserved: %+v", plan.Rules)
	}
}

// TestExternalSourceSGIDRuleRendersLiteral covers the door-closure feature: a rule
// scoped to an external SG id (e.g. a shared ALB SG from remote-state) must
// translate and render to a literal source_security_group_id — NOT a resource ref.
func TestExternalSourceSGIDRuleRendersLiteral(t *testing.T) {
	t.Parallel()
	plan, err := TranslateSecurityGroup(context.Background(), MustEmbedded(), SecurityGroupSpec{
		Name: "app", Region: "Dublin", Provider: "aws",
		Rules: []SecurityRule{
			{Direction: "ingress", Protocol: "tcp", FromPort: 8080, ToPort: 8080, ExternalSourceSGID: "sg-0abc123"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Rules) != 1 || plan.Rules[0].ExternalSourceSGID != "sg-0abc123" {
		t.Fatalf("external_source_sg_id rule not preserved: %+v", plan.Rules)
	}
	hcl := renderSGAWS(plan)
	if !strings.Contains(hcl, `source_security_group_id = "sg-0abc123"`) {
		t.Fatalf("expected literal source_security_group_id, got:\n%s", hcl)
	}
	if strings.Contains(hcl, "aws_security_group.sg-0abc123") {
		t.Fatalf("external sg id must render as a literal, not a resource ref:\n%s", hcl)
	}
}

// TestExternalSourceSGIDValidation asserts the three rejection paths are hard
// errors: a second scope alongside it, a non-sg- value, and a non-AWS provider.
func TestExternalSourceSGIDValidation(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	cases := []struct {
		name string
		spec SecurityGroupSpec
	}{
		{"external + cidr (two scopes)", SecurityGroupSpec{Region: "Dublin", Provider: "aws", Rules: []SecurityRule{{Direction: "ingress", Protocol: "tcp", FromPort: 1, ToPort: 1, CIDRs: []string{"0.0.0.0/0"}, ExternalSourceSGID: "sg-1"}}}},
		{"external bad format", SecurityGroupSpec{Region: "Dublin", Provider: "aws", Rules: []SecurityRule{{Direction: "ingress", Protocol: "tcp", FromPort: 1, ToPort: 1, ExternalSourceSGID: "lb-1"}}}},
		{"external on non-aws", SecurityGroupSpec{Region: "Dublin", Provider: "gcp", Rules: []SecurityRule{{Direction: "ingress", Protocol: "tcp", FromPort: 1, ToPort: 1, ExternalSourceSGID: "sg-1"}}}},
	}
	for _, c := range cases {
		if _, err := TranslateSecurityGroup(context.Background(), cat, c.spec); err == nil {
			t.Errorf("%s: expected validation error, got nil", c.name)
		}
	}
}
