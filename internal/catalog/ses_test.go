package catalog

import (
	"context"
	"strings"
	"testing"
)

// ses_test.go — pd-MIG-CUTOVER-F1-05 (BESPOKE GAP-2).
//
// The email component is provider-agnostic post-cutover: native SES on AWS, an
// SMTP-relay config on DigitalOcean (which has no managed transactional-email
// primitive). DO must NOT hard-error anymore.

// TestTranslateEmailAWSSES proves AWS still renders native SES.
func TestTranslateEmailAWSSES(t *testing.T) {
	t.Parallel()
	plan, err := TranslateEmail(context.Background(), MustEmbedded(), EmailSpec{
		Name: "email-sender", Region: "Dublin", Provider: ProviderAWS, Domain: "passo.build",
	})
	if err != nil {
		t.Fatalf("TranslateEmail(aws): %v", err)
	}
	if plan.Relay {
		t.Fatalf("aws plan should not be a relay")
	}
	if plan.ResourceType != "aws_ses_domain_identity" {
		t.Errorf("aws ResourceType = %q, want aws_ses_domain_identity", plan.ResourceType)
	}
	hcl, err := RenderEmailHCL(plan)
	if err != nil {
		t.Fatalf("RenderEmailHCL(aws): %v", err)
	}
	for _, want := range []string{`resource "aws_ses_domain_identity"`, `resource "aws_ses_domain_dkim"`} {
		if !strings.Contains(hcl, want) {
			t.Errorf("aws SES HCL missing %q", want)
		}
	}
}

// TestTranslateEmailDONoHardError is the GAP-2 proof: DigitalOcean does NOT
// hard-error and renders an SMTP-relay config (default = AWS SES SMTP cross-cloud)
// with a credentials REFERENCE and no inline secret.
func TestTranslateEmailDONoHardError(t *testing.T) {
	t.Parallel()
	plan, err := TranslateEmail(context.Background(), MustEmbedded(), EmailSpec{
		Name: "email-sender", Region: "Frankfurt", Provider: ProviderDigitalOcean, Domain: "passo.build",
	})
	if err != nil {
		t.Fatalf("TranslateEmail(do) must NOT hard-error, got: %v", err)
	}
	if !plan.Relay {
		t.Fatalf("do plan must be an SMTP relay")
	}
	if plan.RelayHost != defaultSESSMTPHost {
		t.Errorf("do default RelayHost = %q, want %q", plan.RelayHost, defaultSESSMTPHost)
	}
	if plan.RelayPort != defaultSMTPPort {
		t.Errorf("do default RelayPort = %d, want %d", plan.RelayPort, defaultSMTPPort)
	}
	if plan.CredentialsRef == "" {
		t.Errorf("do plan must carry a credentials reference")
	}

	hcl, err := RenderEmailHCL(plan)
	if err != nil {
		t.Fatalf("RenderEmailHCL(do): %v", err)
	}
	for _, want := range []string{
		`output "email-sender_smtp_relay"`,
		`relay_host      = "email-smtp.eu-west-1.amazonaws.com"`,
		`relay_port      = 587`,
		`starttls        = true`,
		`credentials_ref = "email-sender-smtp-credentials"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("do SMTP-relay HCL missing %q\n---\n%s", want, hcl)
		}
	}
	// No AWS SES resource, no inline secret.
	for _, bad := range []string{"aws_ses_domain_identity", "password", "secret_access_key"} {
		if strings.Contains(hcl, bad) {
			t.Errorf("do SMTP-relay HCL must not contain %q", bad)
		}
	}
}

// TestTranslateEmailDOThirdPartyRelay proves the opt-in 3rd-party relay override
// (e.g. SendGrid) is honoured.
func TestTranslateEmailDOThirdPartyRelay(t *testing.T) {
	t.Parallel()
	plan, err := TranslateEmail(context.Background(), MustEmbedded(), EmailSpec{
		Name: "email-sender", Region: "Frankfurt", Provider: ProviderDigitalOcean, Domain: "passo.build",
		RelayHost: "smtp.sendgrid.net", RelayPort: 2525, CredentialsRef: "vault:secret/data/email/sendgrid",
	})
	if err != nil {
		t.Fatalf("TranslateEmail(do, sendgrid): %v", err)
	}
	if plan.RelayHost != "smtp.sendgrid.net" || plan.RelayPort != 2525 {
		t.Errorf("3rd-party relay override not honoured: host=%q port=%d", plan.RelayHost, plan.RelayPort)
	}
	hcl, err := RenderEmailHCL(plan)
	if err != nil {
		t.Fatalf("RenderEmailHCL(do, sendgrid): %v", err)
	}
	if !strings.Contains(hcl, `relay_host      = "smtp.sendgrid.net"`) {
		t.Errorf("sendgrid relay host missing from HCL:\n%s", hcl)
	}
	if !strings.Contains(hcl, `credentials_ref = "vault:secret/data/email/sendgrid"`) {
		t.Errorf("sendgrid credentials-ref missing from HCL:\n%s", hcl)
	}
}
