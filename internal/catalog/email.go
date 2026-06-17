package catalog

import (
	"fmt"
	"strings"
)

// EmailDomainSpec is one sender domain with optional DKIM signing.
type EmailDomainSpec struct {
	Domain     string // e.g. passo.build
	EnableDKIM bool
}

// EmailSpec is the canonical transactional-email component: a set of verified
// sender domains. AWS-complete (SES); other providers are a hard "unsupported"
// error (SPEC §1).
type EmailSpec struct {
	Name     string
	Provider string
	Domains  []EmailDomainSpec
}

// EmailPlan is the deterministic concrete translation.
type EmailPlan struct {
	Provider     string            `json:"provider"`
	CSP          string            `json:"csp"`
	Name         string            `json:"name"`
	Domains      []EmailDomainSpec `json:"domains"`
	ResourceType string            `json:"resource_type"`
}

// TranslateEmail resolves an EmailSpec into a concrete plan. AWS-supported; other
// providers are a hard "unsupported" error.
func TranslateEmail(spec EmailSpec) (EmailPlan, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return EmailPlan{}, fmt.Errorf("email: name is required")
	}
	csp, ok := ProviderToCSP(spec.Provider)
	if !ok {
		return EmailPlan{}, fmt.Errorf("email: unknown provider %q", spec.Provider)
	}
	if len(spec.Domains) == 0 {
		return EmailPlan{}, fmt.Errorf("email: declare at least one domain")
	}
	for _, d := range spec.Domains {
		if strings.TrimSpace(d.Domain) == "" {
			return EmailPlan{}, fmt.Errorf("email: each domain entry needs a domain")
		}
	}
	provider := strings.ToLower(strings.TrimSpace(spec.Provider))
	plan := EmailPlan{Provider: provider, CSP: csp, Name: spec.Name, Domains: spec.Domains}
	switch provider {
	case ProviderAWS:
		plan.ResourceType = "aws_ses_domain_identity"
	default:
		return EmailPlan{}, fmt.Errorf("email: unsupported on provider %q (supported: aws SES). "+
			"Hard plan-time error, never an invented resource", provider)
	}
	return plan, nil
}

// RenderEmailHCL renders a resolved plan.
func RenderEmailHCL(plan EmailPlan) (string, error) {
	if plan.Provider != ProviderAWS {
		return "", fmt.Errorf("email: no renderer for provider %q", plan.Provider)
	}
	var b strings.Builder
	for _, d := range plan.Domains {
		rn := tfName(plan.Name + "-" + d.Domain)
		fmt.Fprintf(&b, "resource \"aws_ses_domain_identity\" %q {\n", rn)
		fmt.Fprintf(&b, "  domain = %q\n", d.Domain)
		b.WriteString("}\n\n")
		if d.EnableDKIM {
			fmt.Fprintf(&b, "resource \"aws_ses_domain_dkim\" %q {\n", rn)
			fmt.Fprintf(&b, "  domain = aws_ses_domain_identity.%s.domain\n", rn)
			b.WriteString("}\n\n")
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n", nil
}
