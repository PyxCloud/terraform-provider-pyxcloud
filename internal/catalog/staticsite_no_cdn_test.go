package catalog

import (
	"context"
	"strings"
	"testing"
)

func TestStaticSiteExplicitNoCDNPreservesOriginOnly(t *testing.T) {
	p, e := TranslateStaticSite(context.Background(), MustEmbedded(), StaticSiteSpec{Name: "frontend", Region: "Frankfurt", Provider: "digitalocean", CDNDisabled: true})
	if e != nil {
		t.Fatal(e)
	}
	if p.CloudflareCDN != nil || p.UsesCloudflare {
		t.Fatal("CDN reintroduced")
	}
	h, e := RenderStaticSiteHCL(p)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(h, "digitalocean_spaces_bucket") || strings.Contains(h, "cloudflare") {
		t.Fatalf("unexpected resources %s", h)
	}
	host, e := StaticSiteOriginHost(p)
	if e != nil || host != p.ObjectStorage.BucketName+".fra1.digitaloceanspaces.com" {
		t.Fatalf("origin %q %v", host, e)
	}
}
