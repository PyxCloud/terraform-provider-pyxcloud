package catalog

import (
	"fmt"
	"strings"
)

// RenderTLSCertificateHCL renders a TLSCertificatePlan into provider HCL.
// AWS -> aws_acm_certificate (DNS validation); DigitalOcean -> cert-manager +
// Let's Encrypt on DOKS (a ClusterIssuer + a Certificate via kubernetes_manifest).
// Any other provider never reaches here (TranslateTLSCertificate rejects it with
// a clean ErrComponentUnsupported).
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
	var b strings.Builder

	// cert-manager runs IN the DOKS cluster; reference the existing cluster via a
	// data source so the manifests land on the right cluster's kube credentials
	// (the same convention the scheduled-trigger DOKS path uses).
	fmt.Fprintf(&b, "data \"digitalocean_kubernetes_cluster\" %q {\n", name+"_cluster")
	fmt.Fprintf(&b, "  name = %q\n", p.ClusterName)
	b.WriteString("}\n\n")

	// ClusterIssuer (ACME / Let's Encrypt). Cluster-scoped so multiple Certificates
	// can share it. The private-key Secret is created by cert-manager.
	fmt.Fprintf(&b, "resource \"kubernetes_manifest\" %q {\n", name+"_issuer")
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
	b.WriteString("}\n\n")

	// Certificate custom resource — requests + auto-renews the cert from the issuer.
	fmt.Fprintf(&b, "resource \"kubernetes_manifest\" %q {\n", name+"_certificate")
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
	// The Certificate depends on its issuer existing first.
	fmt.Fprintf(&b, "  depends_on = [kubernetes_manifest.%s]\n", name+"_issuer")
	b.WriteString("}\n")
	return b.String()
}
