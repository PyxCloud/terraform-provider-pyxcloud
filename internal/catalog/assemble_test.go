package catalog

import (
	"context"
	"testing"
)

// TestAssembleHCLGlobalComponents assembles a topology of catalog-independent
// components (no region/SKU lookup), so it needs no catalog. It asserts every
// component's resources land in one concatenated document.
func TestAssembleHCLGlobalComponents(t *testing.T) {
	topo := Topology{
		IAM: []IAMSpec{{
			Name: "keycloak", Provider: "aws", InstanceProfile: true,
			InlinePolicies: []IAMPolicyDoc{{Name: "s3", Document: `{"Version":"2012-10-17"}`}},
		}},
		KMS:           &KMSSpec{Name: "vault", Provider: "aws", Keys: []KMSKeySpec{{Alias: "unseal"}}},
		Email:         &EmailSpec{Name: "passo", Provider: "aws", Domains: []EmailDomainSpec{{Domain: "passo.build", EnableDKIM: true}}},
		CloudflareDNS: &CloudflareDNSSpec{Name: "passo", ZoneVar: "cf_zone", Records: []DNSRecordSpec{{Type: "CNAME", Name: "mcp", Value: "alb.x.com", Proxied: true}}},
		Observability: &ObservabilitySpec{Name: "backend", Provider: "aws", LogGroups: []LogGroupSpec{{Name: "/pyx/be", RetentionDays: 30}}},
		PrefixList:    []PrefixListSpec{{Name: "office", Provider: "aws", Entries: []string{"1.2.3.4/32"}}},
	}
	hcl, err := AssembleHCL(context.Background(), nil, topo)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	for _, want := range []string{
		"aws_iam_role",
		"aws_iam_instance_profile",
		"aws_kms_key",
		"aws_ses_domain_identity",
		"cloudflare_dns_record",
		"aws_cloudwatch_log_group",
		"aws_ec2_managed_prefix_list",
	} {
		if !contains(hcl, want) {
			t.Errorf("assembled HCL missing %q", want)
		}
	}
}

func TestAssembleHCLEmptyIsError(t *testing.T) {
	if _, err := AssembleHCL(context.Background(), nil, Topology{}); err == nil {
		t.Error("empty topology should error (nothing to render)")
	}
}

// TestAssembleHCLFailsLoudOnBadComponent ensures a component error aborts the whole
// assembly (never a silent skip) and names the component.
func TestAssembleHCLFailsLoudOnBadComponent(t *testing.T) {
	_, err := AssembleHCL(context.Background(), nil, Topology{
		KMS: &KMSSpec{Name: "x", Provider: "aws"}, // no keys → translate error
	})
	if err == nil || !contains(err.Error(), "kms") {
		t.Errorf("expected a loud kms error, got: %v", err)
	}
}
