package catalog

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// tls-certificate is the abstract `tls-certificate` component: "obtain and renew
// a TLS certificate for these domains". Like object-storage and scheduled-trigger
// it has NO sizing catalog — a certificate is region/cluster-scoped and issued
// per-usage, so the only catalog lookup is the region (region_name + provider ->
// csp_region). The component therefore depends on the RegionCatalog only.
//
// Per-provider mapping (pd-MIG-TLS-CERTMANAGER — replace ACM):
//
//   - AWS: aws_acm_certificate with DNS validation — the managed certificate being
//     migrated AWAY from. ACM issues + auto-renews the cert; validation is via DNS
//     records the operator publishes (no per-cert price).
//   - DigitalOcean: cert-manager + Let's Encrypt (ACME) on a DOKS cluster. DO has
//     no managed-ACM equivalent in Terraform (digitalocean_certificate is a manual
//     upload/LE-for-LB only, not a renewing in-cluster issuer), so the canonical,
//     plan-time-expressible replacement is the CNCF cert-manager Issuer/Certificate
//     pair driving Let's Encrypt — exactly the ACM replacement this task asks for.
//
// The cert-manager path emits a ClusterIssuer (ACME, Let's Encrypt) + a
// Certificate custom resource via kubernetes_manifest, reusing the SAME
// kubernetes/DOKS-cluster data-source convention the scheduled-trigger DOKS path
// already uses — no new cluster-wiring vocabulary is forked.

// Canonical tls-certificate type tokens. `tls-certificate` is canonical;
// `certificate`, `cert-manager` and `managed-certificate` are accepted aliases
// (all name the same component).
const (
	TypeTLSCertificate     = "tls-certificate"
	TypeCertificate        = "certificate"
	TypeCertManager        = "cert-manager"
	TypeManagedCertificate = "managed-certificate"
)

// Let's Encrypt ACME directory endpoints. Staging is the default-safe choice for
// a first round-trip (it does not consume the strict production rate limits);
// production is opt-in.
const (
	letsEncryptProdACME    = "https://acme-v02.api.letsencrypt.org/directory"
	letsEncryptStagingACME = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

// TLSCertificateSpec is the abstract description of a TLS certificate.
// Provider-neutral.
type TLSCertificateSpec struct {
	Name     string // component name, e.g. "app-tls"
	Region   string // abstract pyx region_name
	Provider string // aws | digitalocean | ...

	// Domains is the certificate's subject domains. The first is the common name;
	// the rest are subject-alternative-names. At least one is required. Wildcards
	// ("*.example.com") are allowed (Let's Encrypt requires DNS-01 for wildcards).
	Domains []string

	// Email is the ACME account contact e-mail (Let's Encrypt). Required for the
	// DigitalOcean cert-manager path (ACME registration); ignored on AWS.
	Email string

	// Production opts into the Let's Encrypt PRODUCTION ACME directory. Defaults to
	// false (staging) so a first round-trip never burns the production rate limit.
	Production bool

	// ClusterName is the existing DOKS cluster cert-manager runs on (DigitalOcean).
	// Required for DO; ignored on AWS.
	ClusterName string

	// Namespace is the Kubernetes namespace for the Certificate resource (DOKS).
	// Empty -> "default". (The ClusterIssuer is cluster-scoped.)
	Namespace string

	// DNSChallenge selects DNS-01 ACME validation (required for wildcard domains)
	// instead of the default HTTP-01. On DO the solver is the digitalocean DNS
	// provider; DNS-01 needs a DO API token Secret referenced by the issuer.
	DNSChallenge bool
}

// TLSCertificatePlan is the deterministic, catalog-resolved concrete translation
// of a TLSCertificateSpec for one provider. STRUCTURED plan (not rendered .tf) —
// the provider owns rendering and state, consistent with the other components.
type TLSCertificatePlan struct {
	Provider   string `json:"provider"`    // aws | digitalocean
	CSP        string `json:"csp"`         // catalog token: aws | do
	RegionName string `json:"region_name"` // abstract pyx region
	CSPRegion  string `json:"csp_region"`  // concrete provider region (catalog-resolved)

	Name    string   `json:"name"`
	Domains []string `json:"domains"` // CN first, then SANs (sorted SANs for determinism)

	// ── cert-manager / Let's Encrypt (DigitalOcean) ──
	Email         string `json:"email,omitempty"`
	ACMEServer    string `json:"acme_server,omitempty"` // resolved LE directory (staging|prod)
	Production    bool   `json:"production,omitempty"`
	ClusterName   string `json:"cluster_name,omitempty"`
	Namespace     string `json:"namespace,omitempty"`
	IssuerName    string `json:"issuer_name,omitempty"`    // derived ClusterIssuer name
	ChallengeKind string `json:"challenge_kind,omitempty"` // http-01 | dns-01

	ResourceType string `json:"resource_type"` // top provider resource
}

// TranslateTLSCertificate resolves a TLSCertificateSpec into a concrete
// TLSCertificatePlan using the catalog. Deterministic and catalog-driven: the
// csp_region comes from the region catalog (never invented). Any missing catalog
// data — or a provider with no expressible managed-certificate primitive —
// surfaces as a hard plan-time error (never a silent fallback), per SPEC §4.
func TranslateTLSCertificate(ctx context.Context, cat RegionCatalog, spec TLSCertificateSpec) (TLSCertificatePlan, error) {
	if err := validateTLSCertificateSpec(spec); err != nil {
		return TLSCertificatePlan{}, err
	}
	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return TLSCertificatePlan{}, err
	}
	provider := lc(spec.Provider)
	name := canonicalName(spec.Name, "pyxcloud-tls")

	domains := normaliseDomains(spec.Domains)

	plan := TLSCertificatePlan{
		Provider:   provider,
		CSP:        row.CSP,
		RegionName: row.RegionName,
		CSPRegion:  row.CSPRegion,
		Name:       name,
		Domains:    domains,
	}

	switch provider {
	case ProviderAWS:
		// ACM with DNS validation — the resource being migrated away from. ACM
		// auto-renews; no ACME e-mail/cluster needed.
		plan.ResourceType = "aws_acm_certificate"
	case ProviderDigitalOcean:
		// cert-manager + Let's Encrypt on an existing DOKS cluster.
		cluster := strings.TrimSpace(spec.ClusterName)
		if cluster == "" {
			return TLSCertificatePlan{}, fmt.Errorf(
				"tls-certificate: digitalocean replaces ACM with cert-manager + Let's Encrypt on a " +
					"DOKS cluster (digitalocean_certificate is a manual upload / LB-only resource, not a " +
					"renewing issuer) — cluster_name is required. This is a hard plan-time error, never a " +
					"silent fallback")
		}
		if strings.TrimSpace(spec.Email) == "" {
			return TLSCertificatePlan{}, fmt.Errorf(
				"tls-certificate: digitalocean cert-manager registers an ACME account with Let's Encrypt — " +
					"email (the ACME contact) is required")
		}
		// Wildcard domains can ONLY be issued via DNS-01 (Let's Encrypt rule). If a
		// wildcard is present but DNS-01 was not selected, that is a hard error
		// rather than a cert that silently fails to issue at apply time.
		if hasWildcard(domains) && !spec.DNSChallenge {
			return TLSCertificatePlan{}, fmt.Errorf(
				"tls-certificate: a wildcard domain (%s) requires DNS-01 validation with Let's Encrypt "+
					"(HTTP-01 cannot validate wildcards) — set dns_challenge = true (uses the digitalocean "+
					"DNS solver). Hard plan-time error, never a silently-failing certificate", firstWildcard(domains))
		}
		ns := strings.TrimSpace(spec.Namespace)
		if ns == "" {
			ns = "default"
		}
		acme := letsEncryptStagingACME
		if spec.Production {
			acme = letsEncryptProdACME
		}
		challenge := "http-01"
		if spec.DNSChallenge {
			challenge = "dns-01"
		}
		plan.Email = strings.TrimSpace(spec.Email)
		plan.ACMEServer = acme
		plan.Production = spec.Production
		plan.ClusterName = cluster
		plan.Namespace = ns
		plan.ChallengeKind = challenge
		// Issuer name encodes the environment so staging and prod issuers can coexist.
		env := "staging"
		if spec.Production {
			env = "prod"
		}
		plan.IssuerName = "letsencrypt-" + env
		plan.ResourceType = "kubernetes_manifest"
	default:
		return TLSCertificatePlan{}, ErrComponentUnsupported{
			Component: TypeTLSCertificate, Provider: provider, CSP: row.CSP, CSPRegion: row.CSPRegion,
			Alternative: "tls-certificate is supported on aws (ACM: aws_acm_certificate) and digitalocean " +
				"(cert-manager + Let's Encrypt on DOKS); for other providers run cert-manager on a " +
				"managed-kubernetes cluster",
		}
	}
	return plan, nil
}

// normaliseDomains trims/lowers domains, keeps the FIRST as the common name, and
// sorts the remaining SANs for a deterministic plan. Duplicates are removed.
func normaliseDomains(in []string) []string {
	seen := map[string]bool{}
	var cn string
	var sans []string
	for i, d := range in {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		if i == 0 || cn == "" {
			cn = d
			continue
		}
		sans = append(sans, d)
	}
	sort.Strings(sans)
	if cn == "" {
		return sans
	}
	return append([]string{cn}, sans...)
}

func hasWildcard(domains []string) bool {
	for _, d := range domains {
		if strings.HasPrefix(d, "*.") {
			return true
		}
	}
	return false
}

func firstWildcard(domains []string) string {
	for _, d := range domains {
		if strings.HasPrefix(d, "*.") {
			return d
		}
	}
	return ""
}

func validateTLSCertificateSpec(spec TLSCertificateSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("tls-certificate: region (abstract pyx region_name) is required")
	}
	if strings.TrimSpace(spec.Provider) == "" {
		return fmt.Errorf("tls-certificate: provider is required (aws | digitalocean)")
	}
	if _, ok := ProviderToCSP(spec.Provider); !ok {
		return fmt.Errorf("tls-certificate: unknown provider %q (aws | digitalocean)", spec.Provider)
	}
	if len(normaliseDomains(spec.Domains)) == 0 {
		return fmt.Errorf("tls-certificate: at least one domain is required")
	}
	return nil
}

// CanonicalTLSCertificateType maps an accepted type token to the canonical
// tls-certificate token, reporting whether it is recognised.
func CanonicalTLSCertificateType(t string) (string, bool) {
	switch lc(t) {
	case TypeTLSCertificate, TypeCertificate, TypeCertManager, TypeManagedCertificate:
		return TypeTLSCertificate, true
	default:
		return "", false
	}
}
