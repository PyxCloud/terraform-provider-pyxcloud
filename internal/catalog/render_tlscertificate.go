package catalog

import (
	"fmt"
	"strings"
)

// RenderTLSCertificateHCL renders a TLSCertificatePlan into provider HCL.
// AWS -> aws_acm_certificate (DNS validation); DigitalOcean -> the OPERATOR
// pattern: the cert-manager operator as an upstream helm_release (CORE) plus a
// ClusterIssuer + a Certificate custom resource via kubernetes_manifest (EXTRA),
// making the component self-contained. Any other provider never reaches here
// (TranslateTLSCertificate rejects it with a clean ErrComponentUnsupported).
func RenderTLSCertificateHCL(plan TLSCertificatePlan) (string, error) {
	switch plan.Provider {
	case ProviderAWS:
		return renderTLSCertificateAWS(plan), nil
	case ProviderDigitalOcean:
		return renderTLSCertificateDO(plan), nil
	default:
		return "", fmt.Errorf("render: unsupported provider %q for tls-certificate", plan.Provider)
	}
}

func renderTLSCertificateAWS(p TLSCertificatePlan) string {
	name := tfName(p.Name)
	var b strings.Builder

	cn := ""
	var sans []string
	if len(p.Domains) > 0 {
		cn = p.Domains[0]
		sans = p.Domains[1:]
	}

	fmt.Fprintf(&b, "resource \"aws_acm_certificate\" %q {\n", name)
	fmt.Fprintf(&b, "  domain_name       = %q\n", cn)
	if len(sans) > 0 {
		fmt.Fprintf(&b, "  subject_alternative_names = [%s]\n", quoteList(sans))
	}
	b.WriteString("  validation_method = \"DNS\"\n")
	fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", p.Name)
	b.WriteString("  lifecycle {\n    create_before_destroy = true\n  }\n")
	b.WriteString("}\n")
	return b.String()
}

func renderTLSCertificateDO(p TLSCertificatePlan) string {
	name := tfName(p.Name)
	clusterData := name + "_cluster"
	operator := name + "_certmanager_operator"

	// ── CORE: the cert-manager operator (controller + CRDs) via the Jetstack chart.
	// installCRDs=true so the cert-manager.io CRDs our ClusterIssuer/Certificate
	// resources reference exist before they are applied. This makes the component
	// self-contained per the operator pattern — see operator.go.
	core := []HelmReleaseSpec{{
		TFName:          operator,
		ReleaseName:     "cert-manager",
		Repository:      certManagerRepo,
		Chart:           certManagerChart,
		Version:         certManagerChartVersion,
		Namespace:       certManagerNamespace,
		CreateNamespace: true,
		Set:             []HelmSet{{Name: "installCRDs", Value: "true"}},
		ClusterDataRef:  clusterData,
	}}

	// ── EXTRA: our ClusterIssuer + Certificate custom resources.
	issuerCR := ManifestCR{
		TFName:    name + "_issuer",
		Manifest:  renderTLSIssuerManifest(p),
		DependsOn: []string{"helm_release." + operator},
	}
	certCR := ManifestCR{
		TFName:   name + "_certificate",
		Manifest: renderTLSCertManifest(p),
		// The Certificate depends on both the operator (its CRD) and its issuer.
		DependsOn: []string{"helm_release." + operator, "kubernetes_manifest." + name + "_issuer"},
	}

	return renderOperatorComponent(clusterData, p.ClusterName, core, []ManifestCR{issuerCR, certCR})
}

// renderTLSIssuerManifest renders the EXTRA ClusterIssuer CR body (ACME /
// Let's Encrypt). Cluster-scoped so multiple Certificates can share it. The
// private-key Secret is created by cert-manager.
func renderTLSIssuerManifest(p TLSCertificatePlan) string {
	var b strings.Builder
	b.WriteString("  manifest = {\n")
	b.WriteString("    apiVersion = \"cert-manager.io/v1\"\n")
	b.WriteString("    kind       = \"ClusterIssuer\"\n")
	b.WriteString("    metadata = {\n")
	fmt.Fprintf(&b, "      name = %q\n", p.IssuerName)
	b.WriteString("    }\n")
	b.WriteString("    spec = {\n")
	b.WriteString("      acme = {\n")
	fmt.Fprintf(&b, "        server = %q\n", p.ACMEServer)
	fmt.Fprintf(&b, "        email  = %q\n", p.Email)
	b.WriteString("        privateKeySecretRef = {\n")
	fmt.Fprintf(&b, "          name = %q\n", p.IssuerName+"-account-key")
	b.WriteString("        }\n")
	b.WriteString("        solvers = [{\n")
	if p.ChallengeKind == "dns-01" {
		// DNS-01 via the DigitalOcean DNS solver — required for wildcards. The DO API
		// token is supplied out-of-band as a Secret cert-manager reads (never in state).
		b.WriteString("          dns01 = {\n")
		b.WriteString("            digitalocean = {\n")
		b.WriteString("              tokenSecretRef = {\n")
		b.WriteString("                name = \"digitalocean-dns\"\n")
		b.WriteString("                key  = \"access-token\"\n")
		b.WriteString("              }\n")
		b.WriteString("            }\n")
		b.WriteString("          }\n")
	} else {
		// HTTP-01 via the ingress-nginx class (the common DOKS ingress).
		b.WriteString("          http01 = {\n")
		b.WriteString("            ingress = {\n")
		b.WriteString("              class = \"nginx\"\n")
		b.WriteString("            }\n")
		b.WriteString("          }\n")
	}
	b.WriteString("        }]\n")
	b.WriteString("      }\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	return b.String()
}

// renderTLSCertManifest renders the EXTRA Certificate CR body — requests +
// auto-renews the cert from the ClusterIssuer.
func renderTLSCertManifest(p TLSCertificatePlan) string {
	var b strings.Builder
	b.WriteString("  manifest = {\n")
	b.WriteString("    apiVersion = \"cert-manager.io/v1\"\n")
	b.WriteString("    kind       = \"Certificate\"\n")
	b.WriteString("    metadata = {\n")
	fmt.Fprintf(&b, "      name      = %q\n", p.Name)
	fmt.Fprintf(&b, "      namespace = %q\n", p.Namespace)
	b.WriteString("    }\n")
	b.WriteString("    spec = {\n")
	fmt.Fprintf(&b, "      secretName = %q\n", p.Name+"-tls")
	b.WriteString("      issuerRef = {\n")
	fmt.Fprintf(&b, "        name = %q\n", p.IssuerName)
	b.WriteString("        kind = \"ClusterIssuer\"\n")
	b.WriteString("      }\n")
	fmt.Fprintf(&b, "      dnsNames = [%s]\n", quoteList(p.Domains))
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	return b.String()
}
