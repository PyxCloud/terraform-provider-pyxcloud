package catalog

import (
	"context"
	"strings"
	"testing"
)

func TestTranslateCloudflareDNS(t *testing.T) {
	p, err := TranslateCloudflareDNS(context.Background(), CloudflareDNSSpec{
		Name:   "web",
		ZoneID: "zone123",
		Records: []DNSRecord{
			{Name: "api.example.com", Type: "A", Content: "203.0.113.10", Proxied: true},
			{Name: "www", Type: "cname", Content: "api.example.com"},
		},
	})
	if err != nil {
		t.Fatalf("TranslateCloudflareDNS: %v", err)
	}
	hcl, err := RenderCloudflareDNSHCL(p)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"resource \"cloudflare_dns_record\" \"web-1\"",
		"zone_id = \"zone123\"",
		"type    = \"A\"",
		"proxied = true",
		"type    = \"CNAME\"", // lower-case input upper-cased
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("cloudflare HCL missing %q\n---\n%s", want, hcl)
		}
	}
}

func TestTranslateCloudflareDNSValidation(t *testing.T) {
	_, err := TranslateCloudflareDNS(context.Background(), CloudflareDNSSpec{
		Name: "x", Records: []DNSRecord{{Name: "a", Type: "WEIRD", Content: "v"}},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported type") {
		t.Errorf("expected unsupported type error, got %v", err)
	}
}

func TestAssembleHCLDNSOnlyNoVPC(t *testing.T) {
	cat, _ := NewEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "edge", Provider: "aws", Region: "Dublin",
		Components: []AssembleComponent{
			{Name: "dns", Type: "dns", DNS: &AssembleDNS{
				ZoneID:  "z1",
				Records: []DNSRecord{{Name: "api", Type: "A", Content: "203.0.113.1"}},
			}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL dns-only: %v", err)
	}
	all := strings.Join(docs, "\n")
	if strings.Contains(all, "aws_vpc") {
		t.Errorf("dns-only env must NOT synthesise a VPC:\n%s", all)
	}
	if !strings.Contains(all, "cloudflare/cloudflare") {
		t.Errorf("dns env must pin the cloudflare provider source:\n%s", all)
	}
	if !strings.Contains(all, "resource \"cloudflare_dns_record\"") {
		t.Errorf("dns env missing cloudflare_dns_record:\n%s", all)
	}
}
