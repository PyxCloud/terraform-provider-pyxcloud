package catalog

// Tests for pd-MIG-B5-CDN-CLOUDFLARE: Cloudflare CDN for non-Spaces origins on DO.
// Mirrors the structure of cloudflare_test.go and macro_test.go.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ── TranslateCloudfareCDN unit tests ─────────────────────────────────────────

func TestTranslateCloudfareCDNBasic(t *testing.T) {
	t.Parallel()
	p, err := TranslateCloudfareCDN(context.Background(), CloudflareCDNSpec{
		Name:       "assets",
		ZoneID:     "zone456",
		Host:       "cdn",
		OriginHost: "lb-12345.do.example.com",
	})
	if err != nil {
		t.Fatalf("TranslateCloudfareCDN: %v", err)
	}
	if p.ResourceType != "cloudflare_dns_record" {
		t.Errorf("resource_type = %q, want cloudflare_dns_record", p.ResourceType)
	}
	if p.Host != "cdn" {
		t.Errorf("host = %q, want cdn", p.Host)
	}
	if p.OriginHost != "lb-12345.do.example.com" {
		t.Errorf("origin_host = %q", p.OriginHost)
	}
}

func TestTranslateCloudfareCDNHostDefaultsToName(t *testing.T) {
	t.Parallel()
	p, err := TranslateCloudfareCDN(context.Background(), CloudflareCDNSpec{
		Name:       "media",
		OriginHost: "lb.example.com",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Host != "media" {
		t.Errorf("host should default to name %q, got %q", "media", p.Host)
	}
}

func TestTranslateCloudfareCDNMissingName(t *testing.T) {
	t.Parallel()
	_, err := TranslateCloudfareCDN(context.Background(), CloudflareCDNSpec{OriginHost: "lb.example.com"})
	if err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Errorf("expected name-required error, got %v", err)
	}
}

// ── RenderCloudfareCDNHCL unit tests ─────────────────────────────────────────

func TestRenderCloudfareCDNHCLContents(t *testing.T) {
	t.Parallel()
	p := CloudflareCDNPlan{
		Name:         "assets",
		ZoneID:       "zone456",
		Host:         "cdn",
		OriginHost:   "lb-12345.do.example.com",
		ResourceType: "cloudflare_dns_record",
	}
	hcl, err := RenderCloudfareCDNHCL(p)
	if err != nil {
		t.Fatalf("RenderCloudfareCDNHCL: %v", err)
	}
	for _, want := range []string{
		`resource "cloudflare_dns_record"`,
		`zone_id = "zone456"`,
		`name    = "cdn"`,
		`type    = "CNAME"`,
		`content = "lb-12345.do.example.com"`,
		`proxied = true`,
		`resource "cloudflare_zone_setting"`,
		`setting_id = "always_online"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("cloudflare-cdn HCL missing %q\n---\n%s", want, hcl)
		}
	}
}

func TestRenderCloudfareCDNHCLNoZoneIDEmitsVar(t *testing.T) {
	t.Parallel()
	p := CloudflareCDNPlan{Name: "cdn", OriginHost: "origin.example.com", ResourceType: "cloudflare_dns_record"}
	hcl, err := RenderCloudfareCDNHCL(p)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(hcl, "var.cloudflare_zone_id") {
		t.Errorf("no zone_id should fall back to var.cloudflare_zone_id:\n%s", hcl)
	}
}

// ── TranslateCDN / DO + LB origin integration tests ──────────────────────────

func TestTranslateCDNDOLoadBalancerOriginRoutesToCloudflare(t *testing.T) {
	t.Parallel()
	plan, err := TranslateCDN(context.Background(), MustEmbedded(), CDNSpec{
		Name: "edge", Region: "Frankfurt", Provider: "digitalocean",
		OriginKind: "load-balancer", OriginName: "web-lb",
	})
	if err != nil {
		t.Fatalf("TranslateCDN DO+LB should succeed (Cloudflare route), got: %v", err)
	}
	if !plan.UsesCloudflare {
		t.Errorf("DO + load-balancer origin must set UsesCloudflare = true")
	}
	if plan.ResourceType != "cloudflare_dns_record" {
		t.Errorf("resource_type = %q, want cloudflare_dns_record", plan.ResourceType)
	}
}

func TestTranslateCDNDOSpacesOriginStillNative(t *testing.T) {
	t.Parallel()
	plan, err := TranslateCDN(context.Background(), MustEmbedded(), CDNSpec{
		Name: "assets", Region: "Frankfurt", Provider: "digitalocean",
		OriginKind: "object-storage", OriginName: "assets-bucket",
	})
	if err != nil {
		t.Fatalf("DO + Spaces origin: %v", err)
	}
	if plan.UsesCloudflare {
		t.Errorf("DO + Spaces origin must NOT use Cloudflare (digitalocean_cdn is the native path)")
	}
	if plan.ResourceType != "digitalocean_cdn" {
		t.Errorf("resource_type = %q, want digitalocean_cdn", plan.ResourceType)
	}
}

func TestRenderCDNHCLDOLBOriginIsCloudflare(t *testing.T) {
	t.Parallel()
	plan, err := TranslateCDN(context.Background(), MustEmbedded(), CDNSpec{
		Name: "edge", Region: "Frankfurt", Provider: "digitalocean",
		OriginKind: "load-balancer", OriginName: "api-lb",
	})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	hcl, err := RenderCDNHCL(plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(hcl, `resource "cloudflare_dns_record"`) {
		t.Errorf("DO LB CDN must render as cloudflare_dns_record:\n%s", hcl)
	}
	if !strings.Contains(hcl, `proxied = true`) {
		t.Errorf("Cloudflare CDN record must be proxied:\n%s", hcl)
	}
}

// ── AssembleHCL integration: Cloudflare provider is pinned ───────────────────

func TestAssembleHCLDOCDNLBOriginPinsCloudflareProvider(t *testing.T) {
	t.Parallel()
	cat, _ := NewEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "myenv", Provider: "digitalocean", Region: "Frankfurt",
		Components: []AssembleComponent{
			{Name: "cdn", Type: "cdn-service", CDN: &AssembleCDN{
				OriginKind: "load-balancer",
				OriginName: "api",
			}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL: %v", err)
	}
	all := strings.Join(docs, "\n")
	if !strings.Contains(all, "cloudflare/cloudflare") {
		t.Errorf("DO CDN with LB origin must pin cloudflare/cloudflare provider:\n%s", all)
	}
	if !strings.Contains(all, `resource "cloudflare_dns_record"`) {
		t.Errorf("assembled env missing cloudflare_dns_record:\n%s", all)
	}
}

// ── Linode / unsupported providers still error ────────────────────────────────

func TestTranslateCDNLinodeStillUnsupported(t *testing.T) {
	t.Parallel()
	_, err := TranslateCDN(context.Background(), MustEmbedded(), CDNSpec{
		Name: "edge", Region: "Frankfurt", Provider: "linode",
		OriginKind: "object-storage", OriginName: "bucket",
	})
	var unsup ErrComponentUnsupported
	if !errors.As(err, &unsup) {
		t.Fatalf("Linode CDN should be ErrComponentUnsupported, got %T: %v", err, err)
	}
}
