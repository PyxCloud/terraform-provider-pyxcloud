package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestTranslateWorkloadIdentityAWS asserts the AWS IAM-role peer: catalog-resolved
// region, default assume service, aws_iam_role type.
func TestTranslateWorkloadIdentityAWS(t *testing.T) {
	t.Parallel()
	plan, err := TranslateWorkloadIdentity(context.Background(), MustEmbedded(), WorkloadIdentitySpec{
		Name: "app-wid", Region: "Frankfurt", Provider: "aws",
		InlinePolicies: []IAMPolicy{{Name: "read", Document: `{"Version":"2012-10-17"}`}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSPRegion != "eu-central-1" {
		t.Errorf("csp_region = %q, want eu-central-1", plan.CSPRegion)
	}
	if plan.ResourceType != "aws_iam_role" {
		t.Errorf("resource_type = %q, want aws_iam_role", plan.ResourceType)
	}
	if plan.AssumeService != "ec2.amazonaws.com" {
		t.Errorf("assume_service = %q, want ec2.amazonaws.com (instance-role default)", plan.AssumeService)
	}
}

// TestTranslateWorkloadIdentityDOAppRole asserts the DO Vault AppRole plan:
// defaulted namespace/role/ttl, approle mode, kubernetes_manifest type.
func TestTranslateWorkloadIdentityDOAppRole(t *testing.T) {
	t.Parallel()
	plan, err := TranslateWorkloadIdentity(context.Background(), MustEmbedded(), WorkloadIdentitySpec{
		Name: "app-wid", Region: "Frankfurt", Provider: "digitalocean", ClusterName: "prod-doks",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "kubernetes_manifest" {
		t.Errorf("resource_type = %q, want kubernetes_manifest", plan.ResourceType)
	}
	if plan.DeliveryMode != WIDeliveryAppRole {
		t.Errorf("delivery_mode = %q, want %q (default)", plan.DeliveryMode, WIDeliveryAppRole)
	}
	if plan.Namespace != defaultWorkloadIdentityNS {
		t.Errorf("namespace = %q, want %q", plan.Namespace, defaultWorkloadIdentityNS)
	}
	if plan.VaultRole != plan.Name {
		t.Errorf("vault_role = %q, want %q (default to name)", plan.VaultRole, plan.Name)
	}
	if plan.TokenTTL != "1h" {
		t.Errorf("token_ttl = %q, want 1h (short-lived default)", plan.TokenTTL)
	}
}

// TestTranslateWorkloadIdentityDOKubernetes asserts the k8s delivery mode resolves.
func TestTranslateWorkloadIdentityDOKubernetes(t *testing.T) {
	t.Parallel()
	plan, err := TranslateWorkloadIdentity(context.Background(), MustEmbedded(), WorkloadIdentitySpec{
		Name: "app-wid", Region: "Frankfurt", Provider: "digitalocean",
		ClusterName: "prod-doks", DeliveryMode: "kubernetes", VaultRole: "myrole", TokenTTL: "30m",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.DeliveryMode != WIDeliveryKubernetes {
		t.Errorf("delivery_mode = %q, want kubernetes", plan.DeliveryMode)
	}
	if plan.VaultRole != "myrole" || plan.TokenTTL != "30m" {
		t.Errorf("overrides lost: role=%q ttl=%q", plan.VaultRole, plan.TokenTTL)
	}
}

// TestTranslateWorkloadIdentityDORequiresCluster asserts the DO hard plan-time
// error (no silent fallback to static keys) when cluster_name is missing.
func TestTranslateWorkloadIdentityDORequiresCluster(t *testing.T) {
	t.Parallel()
	_, err := TranslateWorkloadIdentity(context.Background(), MustEmbedded(), WorkloadIdentitySpec{
		Name: "x", Region: "Frankfurt", Provider: "digitalocean",
	})
	if err == nil || !strings.Contains(err.Error(), "cluster_name is required") {
		t.Fatalf("missing cluster must be a hard error, got %v", err)
	}
}

// TestTranslateWorkloadIdentityDORejectsManagedARNs asserts AWS managed ARNs are a
// hard error on DO (never silently dropped).
func TestTranslateWorkloadIdentityDORejectsManagedARNs(t *testing.T) {
	t.Parallel()
	_, err := TranslateWorkloadIdentity(context.Background(), MustEmbedded(), WorkloadIdentitySpec{
		Name: "x", Region: "Frankfurt", Provider: "digitalocean", ClusterName: "c",
		ManagedPolicyARNs: []string{"arn:aws:iam::aws:policy/ReadOnlyAccess"},
	})
	if err == nil || !strings.Contains(err.Error(), "managed_policy_arns") {
		t.Fatalf("managed ARNs on DO must be a hard error, got %v", err)
	}
}

// TestTranslateWorkloadIdentityBadMode asserts an unknown delivery mode errors.
func TestTranslateWorkloadIdentityBadMode(t *testing.T) {
	t.Parallel()
	_, err := TranslateWorkloadIdentity(context.Background(), MustEmbedded(), WorkloadIdentitySpec{
		Name: "x", Region: "Frankfurt", Provider: "digitalocean", ClusterName: "c", DeliveryMode: "imds",
	})
	if err == nil || !strings.Contains(err.Error(), "delivery_mode") {
		t.Fatalf("unknown delivery mode must be a hard error, got %v", err)
	}
}

// TestTranslateWorkloadIdentityUnsupportedProvider asserts an unsupported provider
// surfaces ErrComponentUnsupported.
func TestTranslateWorkloadIdentityUnsupportedProvider(t *testing.T) {
	t.Parallel()
	_, err := TranslateWorkloadIdentity(context.Background(), MustEmbedded(), WorkloadIdentitySpec{
		Name: "x", Region: "Frankfurt", Provider: "gcp",
	})
	var un ErrComponentUnsupported
	if !errors.As(err, &un) {
		t.Fatalf("expected ErrComponentUnsupported, got %T: %v", err, err)
	}
}

func TestTranslateWorkloadIdentityRegionNotFound(t *testing.T) {
	t.Parallel()
	_, err := TranslateWorkloadIdentity(context.Background(), MustEmbedded(), WorkloadIdentitySpec{
		Name: "x", Region: "Atlantis", Provider: "aws",
	})
	var nf ErrRegionNotFound
	if !errors.As(err, &nf) {
		t.Fatalf("expected ErrRegionNotFound, got %T: %v", err, err)
	}
}

func TestCanonicalWorkloadIdentityType(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"workload-identity", "instance-identity", "workload-id", " WORKLOAD-ID "} {
		got, ok := CanonicalWorkloadIdentityType(in)
		if !ok || got != TypeWorkloadIdentity {
			t.Errorf("%q -> %q,%v want workload-identity,true", in, got, ok)
		}
	}
	if _, ok := CanonicalWorkloadIdentityType("virtual-machine"); ok {
		t.Error("virtual-machine is not a workload-identity type")
	}
}

// ── RENDER TESTS ─────────────────────────────────────────────────────────────

func TestRenderWorkloadIdentityAWS(t *testing.T) {
	t.Parallel()
	plan, err := TranslateWorkloadIdentity(context.Background(), MustEmbedded(), WorkloadIdentitySpec{
		Name: "app-wid", Region: "Frankfurt", Provider: "aws",
		InlinePolicies:    []IAMPolicy{{Name: "read", Document: `{"Version":"2012-10-17","Statement":[]}`}},
		ManagedPolicyARNs: []string{"arn:aws:iam::aws:policy/ReadOnlyAccess"},
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderWorkloadIdentityHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "aws_iam_role" "app-wid"`,
		`Principal = { Service = "ec2.amazonaws.com" }`,
		`resource "aws_iam_role_policy" "app-wid-read"`,
		`resource "aws_iam_role_policy_attachment" "app-wid-managed-1"`,
		// the instance profile is what makes the role a workload identity
		`resource "aws_iam_instance_profile" "app-wid"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("aws workload-identity HCL missing %q:\n%s", want, hcl)
		}
	}
	if !IsASCII(hcl) {
		t.Errorf("rendered HCL not ASCII:\n%s", hcl)
	}
}

func TestRenderWorkloadIdentityDOAppRole(t *testing.T) {
	t.Parallel()
	plan, err := TranslateWorkloadIdentity(context.Background(), MustEmbedded(), WorkloadIdentitySpec{
		Name: "app-wid", Region: "Frankfurt", Provider: "digitalocean", ClusterName: "prod-doks",
		InlinePolicies: []IAMPolicy{{Name: "read", Document: "path \"secret/data/app/*\" { capabilities = [\"read\"] }"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderWorkloadIdentityHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`data "digitalocean_kubernetes_cluster" "app-wid_cluster"`,
		// EXTRA: Vault policy + AppRole auth role CRs (no helm_release here — CORE is vault-ha)
		`kind       = "VaultPolicy"`,
		`kind       = "VaultAuth"`,
		`method = "approle"`,
		`mount  = "auth/approle"`,
		// the auth role depends on the Vault config operator (CORE owned by vault-ha)
		`depends_on = [helm_release.vault_operator]`,
		// AppRole mode emits the droplet cloud-init user_data as an output
		`output "app-wid_user_data"`,
		`vault write -field=token auth/approle/login`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("do approle workload-identity HCL missing %q:\n%s", want, hcl)
		}
	}
	// AppRole mode must NOT emit a ServiceAccount.
	if strings.Contains(hcl, `kind       = "ServiceAccount"`) {
		t.Errorf("approle mode must not emit a ServiceAccount:\n%s", hcl)
	}
	if !IsASCII(hcl) {
		t.Errorf("rendered HCL not ASCII:\n%s", hcl)
	}
}

func TestRenderWorkloadIdentityDOKubernetes(t *testing.T) {
	t.Parallel()
	plan, err := TranslateWorkloadIdentity(context.Background(), MustEmbedded(), WorkloadIdentitySpec{
		Name: "app-wid", Region: "Frankfurt", Provider: "digitalocean", ClusterName: "prod-doks",
		DeliveryMode: "kubernetes",
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderWorkloadIdentityHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`kind       = "ServiceAccount"`,
		`name      = "app-wid-sa"`,
		`method = "kubernetes"`,
		`mount  = "auth/kubernetes"`,
		`tokenExpirationSeconds = 3600`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("do kubernetes workload-identity HCL missing %q:\n%s", want, hcl)
		}
	}
	// k8s mode must NOT emit the droplet user_data output.
	if strings.Contains(hcl, `output "app-wid_user_data"`) {
		t.Errorf("kubernetes mode must not emit droplet user_data:\n%s", hcl)
	}
}

func TestTTLSeconds(t *testing.T) {
	t.Parallel()
	cases := map[string]int{"1h": 3600, "30m": 1800, "90s": 90, "": 3600, "bogus": 3600, "0h": 3600}
	for in, want := range cases {
		if got := ttlSeconds(in); got != want {
			t.Errorf("ttlSeconds(%q) = %d, want %d", in, got, want)
		}
	}
}
