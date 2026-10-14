package catalog

import (
	"strings"
	"testing"
)

func TestCloudflarePromotion(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateCDN(ctx(), cat, CDNSpec{Name: "cdn1", Region: "Global", Provider: "cloudflare", OriginKind: "load-balancer", OriginName: "lb1"})
	if err != nil {
		t.Fatalf("TranslateCDN(cloudflare): %v", err)
	}
	hcl, err := RenderCDNHCL(plan)
	if err != nil || !strings.Contains(hcl, "cloudflare_dns_record") {
		t.Errorf("RenderCDNHCL(cloudflare): %v\n%s", err, hcl)
	}
	// Compute is honestly unsupported on the edge provider.
	if _, err := TranslateVM(ctx(), cat, VMSpec{Name: "web", Region: "Global", Provider: "cloudflare", CPU: 2, RAM: 4, OS: "ubuntu", Count: 1}); err == nil {
		t.Error("TranslateVM(cloudflare) should error — Cloudflare cannot host VMs")
	}
	if NativelySupported("cache", "cloudflare") {
		t.Error("cache on cloudflare should mitigate")
	}
}
