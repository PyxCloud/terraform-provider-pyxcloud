package catalog

import (
	"context"
	"fmt"
	"strings"
)

// Email is the abstract `email` / `email-service` component: a verified sending
// domain — the canonical form of the per-provider scripts' aws_ses_domain_identity
// / aws_ses_domain_dkim glue.
//
//   - AWS: aws_ses_domain_identity + aws_ses_domain_dkim (+ the DKIM tokens are
//     surfaced for the operator to add as DNS records — e.g. via the cloudflare-dns
//     component).
//   - GCP / DigitalOcean: UNSUPPORTED (no managed transactional-email primitive of
//     this form). Clean plan-time error — use AWS SES or a third-party (SendGrid…).

// EmailSpec is the abstract email-sending-domain description.
type EmailSpec struct {
	Name     string
	Region   string
	Provider string
	Domain   string // the sending domain, e.g. passo.build
}

// EmailPlan is the resolved concrete plan.
type EmailPlan struct {
	Provider     string `json:"provider"`
	CSP          string `json:"csp"`
	RegionName   string `json:"region_name"`
	CSPRegion    string `json:"csp_region"`
	Name         string `json:"name"`
	Domain       string `json:"domain"`
	ResourceType string `json:"resource_type"`
}

// TranslateEmail resolves an EmailSpec. Only AWS (SES) is supported.
func TranslateEmail(ctx context.Context, cat RegionCatalog, spec EmailSpec) (EmailPlan, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return EmailPlan{}, fmt.Errorf("email: name is required")
	}
	if strings.TrimSpace(spec.Domain) == "" {
		return EmailPlan{}, fmt.Errorf("email: domain is required (the sending domain to verify)")
	}
	csp, ok := ProviderToCSP(spec.Provider)
	if !ok {
		return EmailPlan{}, fmt.Errorf("email: unknown provider %q", spec.Provider)
	}
	if !strings.EqualFold(spec.Provider, ProviderAWS) {
		return EmailPlan{}, fmt.Errorf("email: only AWS (SES) is supported; %q has no managed "+
			"transactional-email primitive — use AWS or a third-party provider (hard plan-time error)", spec.Provider)
	}
	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return EmailPlan{}, err
	}
	return EmailPlan{
		Provider: strings.ToLower(spec.Provider), CSP: csp,
		RegionName: row.RegionName, CSPRegion: row.CSPRegion,
		Name: spec.Name, Domain: spec.Domain, ResourceType: "aws_ses_domain_identity",
	}, nil
}

// RenderEmailHCL renders an EmailPlan (AWS SES).
func RenderEmailHCL(p EmailPlan) (string, error) {
	if p.Provider != ProviderAWS {
		return "", fmt.Errorf("email: render unsupported for provider %q", p.Provider)
	}
	var b strings.Builder
	id := tfName(p.Name)
	fmt.Fprintf(&b, "resource \"aws_ses_domain_identity\" %q {\n", id)
	fmt.Fprintf(&b, "  domain = %q\n", p.Domain)
	b.WriteString("}\n\n")
	fmt.Fprintf(&b, "resource \"aws_ses_domain_dkim\" %q {\n", id)
	fmt.Fprintf(&b, "  domain = aws_ses_domain_identity.%s.domain\n", id)
	b.WriteString("}\n\n")
	// Surface the DKIM tokens so the operator can add the CNAME records (e.g. via a
	// cloudflare-dns component) — SES sending is unverified until they exist.
	fmt.Fprintf(&b, "output %q {\n", p.Name+"_dkim_tokens")
	fmt.Fprintf(&b, "  value = aws_ses_domain_dkim.%s.dkim_tokens\n", id)
	b.WriteString("}\n")
	return b.String(), nil
}
