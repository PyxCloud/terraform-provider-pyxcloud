package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestTranslateVPNAccessAWS asserts the JIT VPN door resolves its csp_region from
// the catalog and carries the defaults (port 51820, jit-allowlist table, PITR on).
func TestTranslateVPNAccessAWS(t *testing.T) {
	t.Parallel()
	plan, err := TranslateVPNAccess(context.Background(), MustEmbedded(), VPNAccessSpec{
		Name:         "vpn",
		Region:       "Frankfurt",
		Provider:     "aws",
		KeycloakRole: "beta-keycloak-ec2-role",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSPRegion != "eu-central-1" {
		t.Errorf("csp_region = %q, want eu-central-1", plan.CSPRegion)
	}
	if plan.WireGuardPort != 51820 {
		t.Errorf("wireguard_port = %d, want 51820 (default)", plan.WireGuardPort)
	}
	if plan.AllowlistTable != "jit-allowlist" {
		t.Errorf("allowlist_table = %q, want jit-allowlist (default)", plan.AllowlistTable)
	}
	if !plan.PITR {
		t.Error("pitr should default to true")
	}
	if plan.KeycloakRole != "beta-keycloak-ec2-role" {
		t.Errorf("keycloak_role = %q", plan.KeycloakRole)
	}
	if plan.SGResourceType != "aws_security_group" || plan.TableResourceType != "aws_dynamodb_table" {
		t.Errorf("resource types = %q/%q", plan.SGResourceType, plan.TableResourceType)
	}
}

// TestTranslateVPNAccessOverrides asserts non-default port/table/PITR are carried.
func TestTranslateVPNAccessOverrides(t *testing.T) {
	t.Parallel()
	pitr := false
	plan, err := TranslateVPNAccess(context.Background(), MustEmbedded(), VPNAccessSpec{
		Name: "corp-vpn", Region: "Frankfurt", Provider: "aws",
		KeycloakRole:        "kc-role",
		WireGuardPort:       51821,
		AllowlistTable:      "corp-jit",
		PointInTimeRecovery: &pitr,
		BreakGlassCIDRs:     []string{"203.0.113.0/24"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.WireGuardPort != 51821 || plan.AllowlistTable != "corp-jit" || plan.PITR {
		t.Errorf("overrides not carried: port=%d table=%q pitr=%v", plan.WireGuardPort, plan.AllowlistTable, plan.PITR)
	}
	if len(plan.BreakGlassCIDRs) != 1 || plan.BreakGlassCIDRs[0] != "203.0.113.0/24" {
		t.Errorf("break_glass_cidrs = %v", plan.BreakGlassCIDRs)
	}
}

// TestTranslateVPNAccessNonAWSUnsupported asserts the JIT door is AWS-only and a
// non-AWS provider gets a clean ErrComponentUnsupported (never an invented resource).
func TestTranslateVPNAccessNonAWSUnsupported(t *testing.T) {
	t.Parallel()
	for _, prov := range []string{"gcp", "digitalocean"} {
		_, err := TranslateVPNAccess(context.Background(), MustEmbedded(), VPNAccessSpec{
			Name: "vpn", Region: "Frankfurt", Provider: prov, KeycloakRole: "kc",
		})
		var unsup ErrComponentUnsupported
		if !errors.As(err, &unsup) {
			t.Fatalf("provider %q: expected ErrComponentUnsupported, got %v", prov, err)
		}
		if unsup.Component != TypeVPNAccess {
			t.Errorf("provider %q: component = %q, want %q", prov, unsup.Component, TypeVPNAccess)
		}
	}
}

// TestTranslateVPNAccessRequiresKeycloakRole asserts the door is inert (rejected)
// without a writer role.
func TestTranslateVPNAccessRequiresKeycloakRole(t *testing.T) {
	t.Parallel()
	_, err := TranslateVPNAccess(context.Background(), MustEmbedded(), VPNAccessSpec{
		Name: "vpn", Region: "Frankfurt", Provider: "aws",
	})
	if err == nil || !strings.Contains(err.Error(), "keycloak_role") {
		t.Fatalf("expected a keycloak_role-required error, got %v", err)
	}
}

// TestTranslateVPNAccessRejectsBadCIDR asserts a malformed break-glass CIDR is a
// hard plan-time error.
func TestTranslateVPNAccessRejectsBadCIDR(t *testing.T) {
	t.Parallel()
	_, err := TranslateVPNAccess(context.Background(), MustEmbedded(), VPNAccessSpec{
		Name: "vpn", Region: "Frankfurt", Provider: "aws", KeycloakRole: "kc",
		BreakGlassCIDRs: []string{"not-a-cidr"},
	})
	if err == nil || !strings.Contains(err.Error(), "break_glass_cidr") {
		t.Fatalf("expected an invalid-cidr error, got %v", err)
	}
}

// TestTranslateVPNAccessRejectsBadPort asserts an out-of-range port is rejected.
func TestTranslateVPNAccessRejectsBadPort(t *testing.T) {
	t.Parallel()
	_, err := TranslateVPNAccess(context.Background(), MustEmbedded(), VPNAccessSpec{
		Name: "vpn", Region: "Frankfurt", Provider: "aws", KeycloakRole: "kc",
		WireGuardPort: 70000,
	})
	if err == nil || !strings.Contains(err.Error(), "wireguard_port") {
		t.Fatalf("expected a port-range error, got %v", err)
	}
}

// TestRenderVPNAccessAWS asserts the rendered HCL emits the three coupled JIT-door
// pieces: the wg-jit SG (SPI-owned ingress via ignore_changes), the DynamoDB
// allowlist table, and the Keycloak-role IAM policy + attachment, plus the SG-id
// output the SPI's JIT_VPN_SG_ID env points at.
func TestRenderVPNAccessAWS(t *testing.T) {
	t.Parallel()
	plan, err := TranslateVPNAccess(context.Background(), MustEmbedded(), VPNAccessSpec{
		Name: "vpn", Region: "Frankfurt", Provider: "aws", KeycloakRole: "beta-keycloak-ec2-role",
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderVPNAccessHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "aws_security_group" "vpn_jit"`,
		`name        = "vpn-jit-sg"`,
		`lifecycle { ignore_changes = [ingress] }`, // the SPI owns ingress
		`vpc_id      = data.aws_vpc.default.id`,    // default VPC fallback
		`resource "aws_dynamodb_table" "vpn_jit_allowlist"`,
		`name         = "jit-allowlist"`,
		`hash_key     = "sessionId"`,
		`attribute_name = "ttlEpoch"`,
		`point_in_time_recovery { enabled = true }`,
		`resource "aws_iam_policy" "vpn_jit_policy"`,
		`"ec2:AuthorizeSecurityGroupIngress"`,
		`"ec2:RevokeSecurityGroupIngress"`,
		`"dynamodb:PutItem"`,
		`Resource = aws_dynamodb_table.vpn_jit_allowlist.arn`,
		`resource "aws_iam_role_policy_attachment" "vpn_jit_policy"`,
		`role       = "beta-keycloak-ec2-role"`,
		`output "vpn_jit_sg_id"`,
		`value       = aws_security_group.vpn_jit.id`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("vpn-access HCL missing %q\n%s", want, hcl)
		}
	}
	// At rest the door must be dark: no JIT ingress rule baked in (only egress).
	if strings.Contains(hcl, "0.0.0.0/0") && strings.Contains(hcl, "ingress {") {
		t.Errorf("JIT door must be dark at rest (no public ingress baked in)\n%s", hcl)
	}
}

// TestRenderVPNAccessBreakGlass asserts an explicit break-glass CIDR renders a
// static UDP ingress (admin lockout safety), while leaving JIT ingress to the SPI.
func TestRenderVPNAccessBreakGlass(t *testing.T) {
	t.Parallel()
	plan, err := TranslateVPNAccess(context.Background(), MustEmbedded(), VPNAccessSpec{
		Name: "vpn", Region: "Frankfurt", Provider: "aws", KeycloakRole: "kc",
		BreakGlassCIDRs: []string{"203.0.113.7/32"},
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderVPNAccessHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ingress {",
		`cidr_blocks = ["203.0.113.7/32"]`,
		`description = "break-glass-static"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("break-glass HCL missing %q\n%s", want, hcl)
		}
	}
}

// TestRenderVPNAccessExplicitVPC asserts an explicit VPC name wires the JIT SG to
// the sibling pyx_vpc rather than the default VPC.
func TestRenderVPNAccessExplicitVPC(t *testing.T) {
	t.Parallel()
	plan, err := TranslateVPNAccess(context.Background(), MustEmbedded(), VPNAccessSpec{
		Name: "vpn", Region: "Frankfurt", Provider: "aws", KeycloakRole: "kc", VPC: "corp-net",
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderVPNAccessHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hcl, "vpc_id      = aws_vpc.corp-net.id") {
		t.Errorf("explicit VPC not wired\n%s", hcl)
	}
}

func TestRenderVPNAccessUnsupportedProvider(t *testing.T) {
	t.Parallel()
	_, err := RenderVPNAccessHCL(VPNAccessPlan{Provider: ProviderGCP})
	if err == nil {
		t.Fatal("expected a hard render-time error for an unsupported provider")
	}
}

func TestCanonicalVPNAccessType(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"vpn-access", "jit-access", "vpn-door", "VPN-Access"} {
		if got, ok := CanonicalVPNAccessType(in); !ok || got != TypeVPNAccess {
			t.Errorf("CanonicalVPNAccessType(%q) = %q,%v", in, got, ok)
		}
	}
	if _, ok := CanonicalVPNAccessType("load-balancer"); ok {
		t.Error("load-balancer should not be a vpn-access type")
	}
}
