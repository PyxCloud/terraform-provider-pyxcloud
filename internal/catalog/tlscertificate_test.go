package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestTranslateTLSCertificateAWS asserts the ACM plan: catalog-resolved region,
// CN + sorted SANs, DNS validation, aws_acm_certificate type.
func TestTranslateTLSCertificateAWS(t *testing.T) {
	t.Parallel()
	plan, err := TranslateTLSCertificate(context.Background(), MustEmbedded(), TLSCertificateSpec{
		Name: "app-tls", Region: "Frankfurt", Provider: "aws",
		Domains: []string{"app.example.com", "www.example.com", "api.example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSPRegion != "eu-central-1" {
		t.Errorf("csp_region = %q, want eu-central-1", plan.CSPRegion)
	}
	if plan.ResourceType != "aws_acm_certificate" {
		t.Errorf("resource_type = %q, want aws_acm_certificate", plan.ResourceType)
	}
	// CN first, SANs sorted.
	if plan.Domains[0] != "app.example.com" {
		t.Errorf("CN = %q, want app.example.com", plan.Domains[0])
	}
	if plan.Domains[1] != "api.example.com" || plan.Domains[2] != "www.example.com" {
		t.Errorf("SANs not sorted: %v", plan.Domains[1:])
	}
}

// TestTranslateTLSCertificateDO asserts the cert-manager + Let's Encrypt plan on
// DOKS: staging by default, http-01 by default, derived issuer name.
func TestTranslateTLSCertificateDO(t *testing.T) {
	t.Parallel()
	plan, err := TranslateTLSCertificate(context.Background(), MustEmbedded(), TLSCertificateSpec{
		Name: "app-tls", Region: "Frankfurt", Provider: "digitalocean",
		Domains: []string{"app.example.com"}, Email: "ops@example.com",
		ClusterName: "prod-doks",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "kubernetes_manifest" {
		t.Errorf("resource_type = %q, want kubernetes_manifest", plan.ResourceType)
	}
	if plan.ACMEServer != letsEncryptStagingACME {
		t.Errorf("default ACME should be STAGING (rate-limit safe), got %q", plan.ACMEServer)
	}
	if plan.ChallengeKind != "http-01" {
		t.Errorf("default challenge = %q, want http-01", plan.ChallengeKind)
	}
	if plan.IssuerName != "letsencrypt-staging" {
		t.Errorf("issuer = %q, want letsencrypt-staging", plan.IssuerName)
	}
}

// TestTranslateTLSCertificateDOProduction asserts production opts into the prod
// ACME directory and the prod issuer name.
func TestTranslateTLSCertificateDOProduction(t *testing.T) {
	t.Parallel()
	plan, err := TranslateTLSCertificate(context.Background(), MustEmbedded(), TLSCertificateSpec{
		Name: "app-tls", Region: "Frankfurt", Provider: "digitalocean",
		Domains: []string{"app.example.com"}, Email: "ops@example.com",
		ClusterName: "prod-doks", Production: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ACMEServer != letsEncryptProdACME {
		t.Errorf("production ACME = %q, want prod directory", plan.ACMEServer)
	}
	if plan.IssuerName != "letsencrypt-prod" {
		t.Errorf("issuer = %q, want letsencrypt-prod", plan.IssuerName)
	}
}

// TestTranslateTLSCertificateDORequiresClusterAndEmail asserts the DO path's hard
// plan-time errors (no silent fallback) for the missing cluster / email cases.
func TestTranslateTLSCertificateDORequiresClusterAndEmail(t *testing.T) {
	t.Parallel()
	// Missing cluster.
	_, err := TranslateTLSCertificate(context.Background(), MustEmbedded(), TLSCertificateSpec{
		Name: "x", Region: "Frankfurt", Provider: "digitalocean",
		Domains: []string{"app.example.com"}, Email: "ops@example.com",
	})
	if err == nil || !strings.Contains(err.Error(), "cluster_name is required") {
		t.Fatalf("missing cluster must be a hard error, got %v", err)
	}
	// Missing email.
	_, err = TranslateTLSCertificate(context.Background(), MustEmbedded(), TLSCertificateSpec{
		Name: "x", Region: "Frankfurt", Provider: "digitalocean",
		Domains: []string{"app.example.com"}, ClusterName: "prod-doks",
	})
	if err == nil || !strings.Contains(err.Error(), "email") {
		t.Fatalf("missing email must be a hard error, got %v", err)
	}
}

// TestTranslateTLSCertificateWildcardNeedsDNS01 asserts a wildcard domain without
// DNS-01 is a hard error, and that DNS-01 makes it resolve.
func TestTranslateTLSCertificateWildcardNeedsDNS01(t *testing.T) {
	t.Parallel()
	base := TLSCertificateSpec{
		Name: "x", Region: "Frankfurt", Provider: "digitalocean",
		Domains: []string{"*.example.com"}, Email: "ops@example.com", ClusterName: "prod-doks",
	}
	if _, err := TranslateTLSCertificate(context.Background(), MustEmbedded(), base); err == nil ||
		!strings.Contains(err.Error(), "DNS-01") {
		t.Fatalf("wildcard without DNS-01 must be a hard error, got nil/other")
	}
	base.DNSChallenge = true
	plan, err := TranslateTLSCertificate(context.Background(), MustEmbedded(), base)
	if err != nil {
		t.Fatalf("wildcard with DNS-01 should resolve: %v", err)
	}
	if plan.ChallengeKind != "dns-01" {
		t.Errorf("challenge = %q, want dns-01", plan.ChallengeKind)
	}
}

// TestTranslateTLSCertificateRegionNotFound asserts an unresolvable region errors.
func TestTranslateTLSCertificateRegionNotFound(t *testing.T) {
	t.Parallel()
	_, err := TranslateTLSCertificate(context.Background(), MustEmbedded(), TLSCertificateSpec{
		Name: "x", Region: "Atlantis", Provider: "aws", Domains: []string{"a.example.com"},
	})
	var nf ErrRegionNotFound
	if !errors.As(err, &nf) {
		t.Fatalf("expected ErrRegionNotFound, got %T: %v", err, err)
	}
}

// TestTranslateTLSCertificateUnsupportedProvider asserts an unsupported provider
// surfaces ErrComponentUnsupported.
func TestTranslateTLSCertificateUnsupportedProvider(t *testing.T) {
	t.Parallel()
	_, err := TranslateTLSCertificate(context.Background(), MustEmbedded(), TLSCertificateSpec{
		Name: "x", Region: "Frankfurt", Provider: "gcp", Domains: []string{"a.example.com"},
	})
	var un ErrComponentUnsupported
	if !errors.As(err, &un) {
		t.Fatalf("expected ErrComponentUnsupported, got %T: %v", err, err)
	}
}

func TestTLSCertificateValidation(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	cases := []TLSCertificateSpec{
		{Provider: "aws", Domains: []string{"a.example.com"}},                // missing region
		{Region: "Frankfurt", Domains: []string{"a.example.com"}},            // missing provider
		{Region: "Frankfurt", Provider: "vultr", Domains: []string{"a.com"}}, // unknown provider
		{Region: "Frankfurt", Provider: "aws"},                               // no domains
	}
	for i, c := range cases {
		if _, err := TranslateTLSCertificate(context.Background(), cat, c); err == nil {
			t.Errorf("case %d: expected validation error, got nil", i)
		}
	}
}

func TestCanonicalTLSCertificateType(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"tls-certificate", "certificate", "cert-manager", "managed-certificate", " CERT-MANAGER "} {
		got, ok := CanonicalTLSCertificateType(in)
		if !ok || got != TypeTLSCertificate {
			t.Errorf("%q -> %q,%v want tls-certificate,true", in, got, ok)
		}
	}
	if _, ok := CanonicalTLSCertificateType("virtual-machine"); ok {
		t.Error("virtual-machine is not a tls-certificate type")
	}
}

// ── RENDER TESTS ─────────────────────────────────────────────────────────────

func TestRenderTLSCertificateAWS(t *testing.T) {
	t.Parallel()
	plan, err := TranslateTLSCertificate(context.Background(), MustEmbedded(), TLSCertificateSpec{
		Name: "app-tls", Region: "Frankfurt", Provider: "aws",
		Domains: []string{"app.example.com", "www.example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderTLSCertificateHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "aws_acm_certificate" "app-tls"`,
		`domain_name       = "app.example.com"`,
		`subject_alternative_names = ["www.example.com"]`,
		`validation_method = "DNS"`,
		`create_before_destroy = true`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("aws ACM HCL missing %q:\n%s", want, hcl)
		}
	}
	if !IsASCII(hcl) {
		t.Errorf("rendered HCL not ASCII:\n%s", hcl)
	}
}

func TestRenderTLSCertificateDOHTTP01(t *testing.T) {
	t.Parallel()
	plan, err := TranslateTLSCertificate(context.Background(), MustEmbedded(), TLSCertificateSpec{
		Name: "app-tls", Region: "Frankfurt", Provider: "digitalocean",
		Domains: []string{"app.example.com"}, Email: "ops@example.com", ClusterName: "prod-doks",
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderTLSCertificateHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`data "digitalocean_kubernetes_cluster" "app-tls_cluster"`,
		// CORE: the cert-manager operator via its upstream Helm chart (self-contained)
		`resource "helm_release" "app-tls_certmanager_operator"`,
		`chart      = "cert-manager"`,
		`{ name = "installCRDs", value = "true" }`,
		// EXTRA: our ClusterIssuer + Certificate custom resources
		`kind       = "ClusterIssuer"`,
		`server = "` + letsEncryptStagingACME + `"`,
		`email  = "ops@example.com"`,
		`http01 = {`,
		`class = "nginx"`,
		`kind       = "Certificate"`,
		`secretName = "app-tls-tls"`,
		`name = "letsencrypt-staging"`,
		`dnsNames = ["app.example.com"]`,
		// the Certificate depends on the operator (its CRD) AND its issuer
		`depends_on = [helm_release.app-tls_certmanager_operator, kubernetes_manifest.app-tls_issuer]`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("do cert-manager HCL missing %q:\n%s", want, hcl)
		}
	}
}

func TestRenderTLSCertificateDODNS01Wildcard(t *testing.T) {
	t.Parallel()
	plan, err := TranslateTLSCertificate(context.Background(), MustEmbedded(), TLSCertificateSpec{
		Name: "wild", Region: "Frankfurt", Provider: "digitalocean",
		Domains: []string{"*.example.com", "example.com"}, Email: "ops@example.com",
		ClusterName: "prod-doks", DNSChallenge: true, Production: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderTLSCertificateHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`dns01 = {`,
		`digitalocean = {`,
		`name = "digitalocean-dns"`,
		`server = "` + letsEncryptProdACME + `"`,
		`name = "letsencrypt-prod"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("do DNS-01 HCL missing %q:\n%s", want, hcl)
		}
	}
}

func TestRenderTLSCertificateUnsupportedProvider(t *testing.T) {
	t.Parallel()
	if _, err := RenderTLSCertificateHCL(TLSCertificatePlan{Provider: "gcp"}); err == nil {
		t.Fatal("expected render error for unsupported provider, got nil")
	}
}
