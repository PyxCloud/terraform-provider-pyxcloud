package catalog

import "testing"

func TestTranslateEmailAWS(t *testing.T) {
	plan, err := TranslateEmail(EmailSpec{
		Name:     "passo",
		Provider: "aws",
		Domains:  []EmailDomainSpec{{Domain: "passo.build", EnableDKIM: true}},
	})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	hcl, err := RenderEmailHCL(plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"resource \"aws_ses_domain_identity\"",
		"domain = \"passo.build\"",
		"resource \"aws_ses_domain_dkim\"",
		"domain = aws_ses_domain_identity.",
	} {
		if !contains(hcl, want) {
			t.Errorf("email HCL missing %q\n---\n%s", want, hcl)
		}
	}
}

func TestTranslateEmailNoDKIMomitsBlock(t *testing.T) {
	plan, _ := TranslateEmail(EmailSpec{Name: "p", Provider: "aws", Domains: []EmailDomainSpec{{Domain: "x.com"}}})
	hcl, _ := RenderEmailHCL(plan)
	if contains(hcl, "aws_ses_domain_dkim") {
		t.Errorf("no DKIM requested → block must be omitted:\n%s", hcl)
	}
}

func TestTranslateEmailValidation(t *testing.T) {
	if _, err := TranslateEmail(EmailSpec{Provider: "aws", Domains: []EmailDomainSpec{{Domain: "x"}}}); err == nil {
		t.Error("expected error: name required")
	}
	if _, err := TranslateEmail(EmailSpec{Name: "n", Provider: "aws"}); err == nil {
		t.Error("expected error: need a domain")
	}
	if _, err := TranslateEmail(EmailSpec{Name: "n", Provider: "digitalocean", Domains: []EmailDomainSpec{{Domain: "x"}}}); err == nil {
		t.Error("expected error: unsupported provider")
	}
}
