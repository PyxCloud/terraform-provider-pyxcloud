package catalog

import "testing"

func TestTranslateCloudflareDNS(t *testing.T) {
	plan, err := TranslateCloudflareDNS(CloudflareDNSSpec{
		Name:    "passo",
		ZoneVar: "cf_zone_id",
		Records: []DNSRecordSpec{
			{Type: "CNAME", Name: "mcp", Value: "alb.example.com", Proxied: true},
			{Type: "TXT", Name: "@", Value: "v=spf1 include:_spf.example.com ~all", TTL: 300},
		},
	})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	hcl, err := RenderCloudflareDNSHCL(plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"resource \"cloudflare_dns_record\"",
		"zone_id = var.cf_zone_id",
		"type    = \"CNAME\"",
		"name    = \"mcp\"",
		"proxied = true",
		"ttl     = 1", // proxied → automatic TTL
		"type    = \"TXT\"",
		"ttl     = 300",
	} {
		if !contains(hcl, want) {
			t.Errorf("cloudflare DNS HCL missing %q\n---\n%s", want, hcl)
		}
	}
}

func TestTranslateCloudflareDNSValidation(t *testing.T) {
	if _, err := TranslateCloudflareDNS(CloudflareDNSSpec{ZoneVar: "z", Records: []DNSRecordSpec{{Type: "A", Name: "x", Value: "1.2.3.4"}}}); err == nil {
		t.Error("expected error: name required")
	}
	if _, err := TranslateCloudflareDNS(CloudflareDNSSpec{Name: "n", Records: []DNSRecordSpec{{Type: "A", Name: "x", Value: "1.2.3.4"}}}); err == nil {
		t.Error("expected error: zone_var required")
	}
	if _, err := TranslateCloudflareDNS(CloudflareDNSSpec{Name: "n", ZoneVar: "z"}); err == nil {
		t.Error("expected error: need at least one record")
	}
	if _, err := TranslateCloudflareDNS(CloudflareDNSSpec{Name: "n", ZoneVar: "z", Records: []DNSRecordSpec{{Type: "BOGUS", Name: "x", Value: "y"}}}); err == nil {
		t.Error("expected error: invalid record type")
	}
	if _, err := TranslateCloudflareDNS(CloudflareDNSSpec{Name: "n", ZoneVar: "z", Records: []DNSRecordSpec{{Type: "A", Name: "x", Value: "y", Proxied: true, TTL: 300}}}); err == nil {
		t.Error("expected error: proxied record must use automatic TTL")
	}
}
