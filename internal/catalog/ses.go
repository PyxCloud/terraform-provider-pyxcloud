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
//   - DigitalOcean / any non-AWS: DO has NO managed transactional-email primitive.
//     Rather than hard-error, the component renders an SMTP-RELAY CONFIG — the
//     endpoint + a credentials-REFERENCE (never inline secrets) that the DO compute
//     uses to send transactional email. By default the relay points at AWS SES's
//     SMTP endpoint (SES is region-global and reachable cross-cloud with IAM SMTP
//     creds), so the same verified sending domain keeps working post-cutover. A
//     third-party relay (SendGrid / Postmark / Mailgun) is opt-in via RelayHost.
//     See docs/cutover/EMAIL-PATH.md for the decision + DNS (SPF/DKIM/DMARC) notes.

// Default AWS SES SMTP relay endpoint used when a DO (non-AWS) email component does
// not override RelayHost. Region-global service; eu-west-1 mirrors the prod estate.
const defaultSESSMTPHost = "email-smtp.eu-west-1.amazonaws.com"
const defaultSMTPPort = 587 // STARTTLS submission

// EmailSpec is the abstract email-sending-domain description.
type EmailSpec struct {
	Name     string
	Region   string
	Provider string
	Domain   string // the sending domain, e.g. passo.build

	// SMTP-relay overrides (only meaningful on a non-AWS placement). All optional;
	// when empty the relay defaults to the AWS SES SMTP endpoint (cross-cloud).
	RelayHost    string // e.g. smtp.sendgrid.net / smtp.postmarkapp.com — overrides the SES default
	RelayPort    int    // SMTP submission port (defaults to 587 / STARTTLS)
	CredentialsRef string // reference to the secret holding SMTP user+password (e.g. a Vault path / secret name) — NEVER an inline secret
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

	// SMTP-relay plan (populated on non-AWS placements; the DO email path).
	Relay          bool   `json:"relay"`
	RelayHost      string `json:"relay_host,omitempty"`
	RelayPort      int    `json:"relay_port,omitempty"`
	CredentialsRef string `json:"credentials_ref,omitempty"`
}

// TranslateEmail resolves an EmailSpec.
//
//   - AWS renders a native SES verified sending domain (aws_ses_domain_identity +
//     aws_ses_domain_dkim).
//   - Any non-AWS provider (DigitalOcean) renders an SMTP-RELAY CONFIG instead of
//     hard-erroring: the compute sends via an external SMTP endpoint (AWS SES SMTP
//     by default, or a 3rd-party relay) using a credentials REFERENCE.
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
	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return EmailPlan{}, err
	}

	plan := EmailPlan{
		Provider: strings.ToLower(spec.Provider), CSP: csp,
		RegionName: row.RegionName, CSPRegion: row.CSPRegion,
		Name: spec.Name, Domain: spec.Domain,
	}

	if strings.EqualFold(spec.Provider, ProviderAWS) {
		// Native SES on AWS.
		plan.ResourceType = "aws_ses_domain_identity"
		return plan, nil
	}

	// Non-AWS (DigitalOcean): SMTP-relay config. DO has no managed email primitive,
	// so we keep AWS SES cross-cloud by default (reachable over SMTP with IAM creds)
	// and allow an opt-in 3rd-party relay. No hard error.
	plan.Relay = true
	plan.ResourceType = "smtp_relay_config"
	plan.RelayHost = strings.TrimSpace(spec.RelayHost)
	if plan.RelayHost == "" {
		plan.RelayHost = defaultSESSMTPHost
	}
	plan.RelayPort = spec.RelayPort
	if plan.RelayPort == 0 {
		plan.RelayPort = defaultSMTPPort
	}
	plan.CredentialsRef = strings.TrimSpace(spec.CredentialsRef)
	if plan.CredentialsRef == "" {
		// A stable, secret-manager-resolved reference — NOT a secret. On DO the
		// secrets-manager component aliases to Vault-HA, so this is a Vault path.
		plan.CredentialsRef = spec.Name + "-smtp-credentials"
	}
	return plan, nil
}

// RenderEmailHCL renders an EmailPlan.
//
//   - AWS: aws_ses_domain_identity + aws_ses_domain_dkim (+ DKIM-token output).
//   - Non-AWS (relay): a terraform `locals` + `output` describing the SMTP-relay
//     config (host / port / STARTTLS / credentials-ref) the compute consumes —
//     NO secret material is ever emitted, only a reference.
func RenderEmailHCL(p EmailPlan) (string, error) {
	if p.Relay {
		return renderEmailRelayHCL(p)
	}
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

// renderEmailRelayHCL renders the DO (non-AWS) SMTP-relay config. It emits a
// `locals` block (the relay wiring the compute reads) plus an `output` for the
// operator/pipeline. It emits NO managed cloud resource and NO secret — only the
// endpoint and a credentials REFERENCE (resolved at deploy time from the secrets
// manager / Vault). This makes DO a clean, plannable render (no hard error) while
// keeping the verified sending domain (passo.build) working cross-cloud.
func renderEmailRelayHCL(p EmailPlan) (string, error) {
	var b strings.Builder
	id := tfName(p.Name)
	fmt.Fprintf(&b, "# email %q: SMTP-relay config (no managed DO email primitive — see docs/cutover/EMAIL-PATH.md).\n", p.Name)
	fmt.Fprintf(&b, "# Transactional email (invites / passkey / notifications) is sent by the compute\n")
	fmt.Fprintf(&b, "# via this SMTP relay. Default relay = AWS SES SMTP (cross-cloud); credentials are\n")
	fmt.Fprintf(&b, "# a REFERENCE resolved from the secrets manager (Vault-HA on DO) — never inline.\n")
	fmt.Fprintf(&b, "locals {\n")
	fmt.Fprintf(&b, "  %s_smtp = {\n", id)
	fmt.Fprintf(&b, "    sending_domain  = %q\n", p.Domain)
	fmt.Fprintf(&b, "    relay_host      = %q\n", p.RelayHost)
	fmt.Fprintf(&b, "    relay_port      = %d\n", p.RelayPort)
	fmt.Fprintf(&b, "    starttls        = true\n")
	fmt.Fprintf(&b, "    credentials_ref = %q\n", p.CredentialsRef)
	fmt.Fprintf(&b, "  }\n")
	fmt.Fprintf(&b, "}\n\n")
	fmt.Fprintf(&b, "output %q {\n", p.Name+"_smtp_relay")
	fmt.Fprintf(&b, "  description = \"SMTP-relay config for the DO compute (host/port/creds-ref, no secrets)\"\n")
	fmt.Fprintf(&b, "  value       = local.%s_smtp\n", id)
	fmt.Fprintf(&b, "}\n")
	return b.String(), nil
}
