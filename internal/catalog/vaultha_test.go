package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestTranslateVaultHAAWS asserts the AWS Secrets Manager + KMS peer.
func TestTranslateVaultHAAWS(t *testing.T) {
	t.Parallel()
	plan, err := TranslateVaultHA(context.Background(), MustEmbedded(), VaultHASpec{
		Name: "app-vault", Region: "Frankfurt", Provider: "aws",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSPRegion != "eu-central-1" {
		t.Errorf("csp_region = %q, want eu-central-1", plan.CSPRegion)
	}
	if plan.ResourceType != "aws_secretsmanager_secret" {
		t.Errorf("resource_type = %q, want aws_secretsmanager_secret", plan.ResourceType)
	}
}

// TestTranslateVaultHADO asserts the DO Vault HA Raft plan: defaulted replicas/ns/
// version/auth-methods, helm CORE, kubernetes_manifest type.
func TestTranslateVaultHADO(t *testing.T) {
	t.Parallel()
	plan, err := TranslateVaultHA(context.Background(), MustEmbedded(), VaultHASpec{
		Name: "app-vault", Region: "Frankfurt", Provider: "digitalocean",
		ClusterName: "prod-doks", TransitUnseal: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "kubernetes_manifest" {
		t.Errorf("resource_type = %q, want kubernetes_manifest", plan.ResourceType)
	}
	if !plan.RendersHelm {
		t.Error("DO Vault-HA must render the CORE helm chart (RendersHelm)")
	}
	if plan.Replicas != defaultVaultReplicas {
		t.Errorf("replicas = %d, want %d (default odd quorum)", plan.Replicas, defaultVaultReplicas)
	}
	if plan.Namespace != defaultVaultNS {
		t.Errorf("namespace = %q, want %q", plan.Namespace, defaultVaultNS)
	}
	if plan.ChartVersion != vaultDefaultVersion {
		t.Errorf("chart_version = %q, want %q", plan.ChartVersion, vaultDefaultVersion)
	}
	if plan.TransitKeyName != defaultTransitKey {
		t.Errorf("transit_key = %q, want %q", plan.TransitKeyName, defaultTransitKey)
	}
	// default auth methods: kubernetes + approle
	if len(plan.AuthMethods) != 2 {
		t.Errorf("auth_methods = %v, want 2 defaults", plan.AuthMethods)
	}
}

// TestTranslateVaultHADORejectsNonHAReplicas asserts a single-node or even quorum
// is a hard error (no false HA promise).
func TestTranslateVaultHADORejectsNonHAReplicas(t *testing.T) {
	t.Parallel()
	if _, err := TranslateVaultHA(context.Background(), MustEmbedded(), VaultHASpec{
		Name: "x", Region: "Frankfurt", Provider: "digitalocean", ClusterName: "c", Replicas: 1,
	}); err == nil || !strings.Contains(err.Error(), "highly available") {
		t.Fatalf("replicas=1 must be a hard error, got %v", err)
	}
	if _, err := TranslateVaultHA(context.Background(), MustEmbedded(), VaultHASpec{
		Name: "x", Region: "Frankfurt", Provider: "digitalocean", ClusterName: "c", Replicas: 4,
	}); err == nil || !strings.Contains(err.Error(), "even Raft quorum") {
		t.Fatalf("replicas=4 must be a hard error, got %v", err)
	}
}

// TestTranslateVaultHADORequiresCluster asserts the DO hard plan-time error.
func TestTranslateVaultHADORequiresCluster(t *testing.T) {
	t.Parallel()
	_, err := TranslateVaultHA(context.Background(), MustEmbedded(), VaultHASpec{
		Name: "x", Region: "Frankfurt", Provider: "digitalocean",
	})
	if err == nil || !strings.Contains(err.Error(), "cluster_name is required") {
		t.Fatalf("missing cluster must be a hard error, got %v", err)
	}
}

// TestTranslateVaultHABadAuthMethod asserts an unknown auth method errors.
func TestTranslateVaultHABadAuthMethod(t *testing.T) {
	t.Parallel()
	_, err := TranslateVaultHA(context.Background(), MustEmbedded(), VaultHASpec{
		Name: "x", Region: "Frankfurt", Provider: "digitalocean", ClusterName: "c",
		AuthMethods: []string{"ldap"},
	})
	if err == nil || !strings.Contains(err.Error(), "auth_method") {
		t.Fatalf("unknown auth method must be a hard error, got %v", err)
	}
}

func TestTranslateVaultHAUnsupportedProvider(t *testing.T) {
	t.Parallel()
	_, err := TranslateVaultHA(context.Background(), MustEmbedded(), VaultHASpec{
		Name: "x", Region: "Frankfurt", Provider: "gcp",
	})
	var un ErrComponentUnsupported
	if !errors.As(err, &un) {
		t.Fatalf("expected ErrComponentUnsupported, got %T: %v", err, err)
	}
}

func TestCanonicalVaultHAType(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"vault-ha", "vault", "vault-cluster", " VAULT "} {
		got, ok := CanonicalVaultHAType(in)
		if !ok || got != TypeVaultHA {
			t.Errorf("%q -> %q,%v want vault-ha,true", in, got, ok)
		}
	}
	if _, ok := CanonicalVaultHAType("virtual-machine"); ok {
		t.Error("virtual-machine is not a vault-ha type")
	}
}

// ── RENDER TESTS ─────────────────────────────────────────────────────────────

func TestRenderVaultHAAWS(t *testing.T) {
	t.Parallel()
	plan, err := TranslateVaultHA(context.Background(), MustEmbedded(), VaultHASpec{
		Name: "app-vault", Region: "Frankfurt", Provider: "aws",
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderVaultHAHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "aws_kms_key" "app-vault"`,
		`enable_key_rotation     = true`,
		`resource "aws_kms_alias" "app-vault"`,
		`resource "aws_secretsmanager_secret" "app-vault"`,
		`kms_key_id = aws_kms_key.app-vault.arn`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("aws vault-ha HCL missing %q:\n%s", want, hcl)
		}
	}
	if !IsASCII(hcl) {
		t.Errorf("rendered HCL not ASCII:\n%s", hcl)
	}
}

func TestRenderVaultHADO(t *testing.T) {
	t.Parallel()
	plan, err := TranslateVaultHA(context.Background(), MustEmbedded(), VaultHASpec{
		Name: "app-vault", Region: "Frankfurt", Provider: "digitalocean",
		ClusterName: "prod-doks", TransitUnseal: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderVaultHAHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`data "digitalocean_kubernetes_cluster" "app-vault_cluster"`,
		// CORE: the official Vault Helm chart in HA Raft mode
		`resource "helm_release" "app-vault_operator"`,
		`chart      = "vault"`,
		`{ name = "server.ha.raft.enabled", value = "true" }`,
		`{ name = "server.ha.replicas", value = "3" }`,
		// Transit auto-unseal seal stanza
		`seal \"transit\"`,
		`storage \"raft\"`,
		// EXTRA: VaultConnection + VaultAuthGlobal CRs
		`kind       = "VaultConnection"`,
		`kind       = "VaultAuthGlobal"`,
		`depends_on = [helm_release.app-vault_operator]`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("do vault-ha operator HCL missing %q:\n%s", want, hcl)
		}
	}
	// Operator pattern: never hand-roll the Vault StatefulSet/Deployment.
	for _, gone := range []string{`kind       = "StatefulSet"`, `kind       = "Deployment"`} {
		if strings.Contains(hcl, gone) {
			t.Errorf("operator pattern must not hand-roll %q:\n%s", gone, hcl)
		}
	}
}

func TestRenderVaultHANoTransit(t *testing.T) {
	t.Parallel()
	plan, err := TranslateVaultHA(context.Background(), MustEmbedded(), VaultHASpec{
		Name: "app-vault", Region: "Frankfurt", Provider: "digitalocean",
		ClusterName: "prod-doks", TransitUnseal: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderVaultHAHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hcl, `seal \"transit\"`) {
		t.Errorf("transit_unseal=false must NOT emit a transit seal stanza:\n%s", hcl)
	}
	if !strings.Contains(hcl, `storage \"raft\"`) {
		t.Errorf("HA Raft storage must still be present:\n%s", hcl)
	}
}
