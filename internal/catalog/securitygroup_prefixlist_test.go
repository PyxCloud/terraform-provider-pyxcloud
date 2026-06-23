package catalog

import (
	"context"
	"strings"
	"testing"
)

// Tests for pd-MIG-SG-DO-FIREWALL: SG-references-SG + prefix-list inlining for
// the digitalocean_firewall (AWS->DO migration), with the AWS peer kept honest.

// TestDOFirewallSourceSGRendersAsTags proves the AWS->DO migration of a
// SG-references-SG rule: DigitalOcean firewalls have no SG-to-SG primitive, so a
// SourceSG rule must render as source_tags/destination_tags (the referenced SG's
// droplets carry that tag) — NOT silently dropped (the prior renderer ignored it).
func TestDOFirewallSourceSGRendersAsTags(t *testing.T) {
	t.Parallel()
	plan, err := TranslateSecurityGroup(context.Background(), MustEmbedded(), SecurityGroupSpec{
		Name: "app", Network: "prod", Region: "Amsterdam", Provider: "digitalocean",
		Rules: []SecurityRule{
			{Direction: "ingress", Protocol: "tcp", FromPort: 8080, ToPort: 8080, SourceSG: "lb"},
			{Direction: "egress", Protocol: "tcp", FromPort: 5432, ToPort: 5432, SourceSG: "db-tier"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl := renderSGDO(plan)
	for _, want := range []string{
		`source_tags = ["lb"]`,
		`destination_tags = ["db-tier"]`,
	} {
		if !strings.Contains(hcl, want) {
			t.Fatalf("DO firewall missing %q\n%s", want, hcl)
		}
	}
	// A SourceSG rule must NOT emit empty address lists.
	if strings.Contains(hcl, "source_addresses = []") || strings.Contains(hcl, "destination_addresses = []") {
		t.Fatalf("SourceSG rule must not emit empty address list:\n%s", hcl)
	}
}

// TestDOFirewallPrefixListInlined proves prefix-list inlining: DO has no managed
// prefix-list primitive, so a rule referencing a prefix-list resolves to inline
// source_addresses (deterministically sorted) on DO.
func TestDOFirewallPrefixListInlined(t *testing.T) {
	t.Parallel()
	plan, err := TranslateSecurityGroup(context.Background(), MustEmbedded(), SecurityGroupSpec{
		Name: "edge", Network: "prod", Region: "Amsterdam", Provider: "digitalocean",
		PrefixLists: map[string][]PrefixEntry{
			"corp": {{CIDR: "10.2.0.0/16"}, {CIDR: "10.1.0.0/16"}},
		},
		Rules: []SecurityRule{
			{Direction: "ingress", Protocol: "tcp", FromPort: 22, ToPort: 22, SourcePrefixList: "corp"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Rules[0].ResolvedPrefixCIDRs; len(got) != 2 || got[0] != "10.1.0.0/16" || got[1] != "10.2.0.0/16" {
		t.Fatalf("prefix-list CIDRs not resolved+sorted: %v", got)
	}
	hcl := renderSGDO(plan)
	if !strings.Contains(hcl, `source_addresses = ["10.1.0.0/16", "10.2.0.0/16"]`) {
		t.Fatalf("DO firewall did not inline prefix-list CIDRs:\n%s", hcl)
	}
	// DO must NOT reference an AWS managed prefix list.
	if strings.Contains(hcl, "aws_ec2_managed_prefix_list") {
		t.Fatalf("DO must inline CIDRs, not reference a managed prefix list:\n%s", hcl)
	}
}

// TestAWSFirewallPrefixListReference is the AWS peer: the same SourcePrefixList
// rule renders as a managed-prefix-list reference (prefix_list_ids), and the
// CIDRs are NOT inlined on AWS (AWS keeps the managed prefix list).
func TestAWSFirewallPrefixListReference(t *testing.T) {
	t.Parallel()
	plan, err := TranslateSecurityGroup(context.Background(), MustEmbedded(), SecurityGroupSpec{
		Name: "edge", Network: "prod", Region: "Dublin", Provider: "aws",
		PrefixLists: map[string][]PrefixEntry{
			"corp": {{CIDR: "10.1.0.0/16"}},
		},
		Rules: []SecurityRule{
			{Direction: "ingress", Protocol: "tcp", FromPort: 22, ToPort: 22, SourcePrefixList: "corp"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Rules[0].ResolvedPrefixCIDRs) != 0 {
		t.Fatalf("AWS must not inline prefix-list CIDRs, got %v", plan.Rules[0].ResolvedPrefixCIDRs)
	}
	hcl := renderSGAWS(plan)
	if !strings.Contains(hcl, "prefix_list_ids   = [aws_ec2_managed_prefix_list.corp.id]") {
		t.Fatalf("AWS rule did not reference managed prefix list:\n%s", hcl)
	}
}

// TestPrefixListReferenceUndefinedIsHardError asserts a rule referencing an
// undefined prefix-list is a hard plan-time error (never a silent empty rule).
func TestPrefixListReferenceUndefinedIsHardError(t *testing.T) {
	t.Parallel()
	_, err := TranslateSecurityGroup(context.Background(), MustEmbedded(), SecurityGroupSpec{
		Name: "edge", Region: "Dublin", Provider: "aws",
		Rules: []SecurityRule{
			{Direction: "ingress", Protocol: "tcp", FromPort: 22, ToPort: 22, SourcePrefixList: "missing"},
		},
	})
	if err == nil {
		t.Fatal("expected hard error for undefined prefix-list reference, got nil")
	}
}
