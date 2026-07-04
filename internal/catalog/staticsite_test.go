package catalog

import (
	"context"
	"strings"
	"testing"
)

// TestStaticSiteDO proves the static-site component descends on DigitalOcean to
// the composed Spaces static-website origin + Cloudflare CDN, reusing the
// existing object-storage + cloudflare-cdn renderers (GAP-1 closure,
// pd-MIG-CUTOVER-F1-01).
func TestStaticSiteDO(t *testing.T) {
	t.Parallel()
	plan, err := TranslateStaticSite(context.Background(), MustEmbedded(), StaticSiteSpec{
		Name: "console", Region: "Frankfurt", Provider: "digitalocean",
		CustomDomain: "app.passo.build",
	})
	if err != nil {
		t.Fatalf("TranslateStaticSite (DO): %v", err)
	}
	if plan.ResourceType != "digitalocean_spaces_bucket" {
		t.Errorf("want top resource digitalocean_spaces_bucket, got %q", plan.ResourceType)
	}
	if !plan.UsesCloudflare {
		t.Errorf("DO static-site must pin Cloudflare (CDN front)")
	}
	if plan.ObjectStorage == nil || plan.CloudflareCDN == nil {
		t.Fatalf("DO static-site must compose object-storage + cloudflare-cdn plans")
	}
	// The origin bucket serves a public static website.
	if !plan.ObjectStorage.Public {
		t.Errorf("static-site origin bucket must be public-read")
	}
	if plan.ObjectStorage.Website == nil || plan.ObjectStorage.Website.IndexDocument != "index.html" {
		t.Errorf("static-site origin must carry index.html website config, got %+v", plan.ObjectStorage.Website)
	}
	// The CDN origin is the Spaces website endpoint; the record host is the subdomain.
	if !strings.Contains(plan.CloudflareCDN.OriginHost, "digitaloceanspaces.com") {
		t.Errorf("CDN origin must be the Spaces website endpoint, got %q", plan.CloudflareCDN.OriginHost)
	}
	if plan.CloudflareCDN.Host != "app" {
		t.Errorf("CDN host should be the domain's leftmost label 'app', got %q", plan.CloudflareCDN.Host)
	}

	hcl, err := RenderStaticSiteHCL(plan)
	if err != nil {
		t.Fatalf("RenderStaticSiteHCL (DO): %v", err)
	}
	// DO Spaces static website origin + Cloudflare CDN front, no AWS leakage.
	for _, want := range []string{
		`resource "digitalocean_spaces_bucket" "console"`,
		`acl           = "public-read"`,
		`static-website origin: index="index.html" error="index.html"`,
		`resource "cloudflare_dns_record" "console-cdn"`,
		`proxied = true`,
		`resource "cloudflare_zone_setting" "console-cdn-always_online"`,
		`digitaloceanspaces.com`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("DO static-site HCL missing %q\n---\n%s", want, hcl)
		}
	}
	if strings.Contains(hcl, "aws_") {
		t.Errorf("DO static-site HCL must not emit AWS resources:\n%s", hcl)
	}
}

// TestStaticSiteAWS proves the static-site component descends to AWS Amplify (the
// source-estate managed static-hosting primitive).
func TestStaticSiteAWS(t *testing.T) {
	t.Parallel()
	plan, err := TranslateStaticSite(context.Background(), MustEmbedded(), StaticSiteSpec{
		Name: "marketing", Region: "Dublin", Provider: "aws",
		CustomDomain: "passo.build", BuildOutputDir: "dist",
	})
	if err != nil {
		t.Fatalf("TranslateStaticSite (AWS): %v", err)
	}
	if plan.ResourceType != "aws_amplify_app" {
		t.Errorf("want top resource aws_amplify_app, got %q", plan.ResourceType)
	}
	if plan.UsesCloudflare {
		t.Errorf("AWS static-site (Amplify) must not pin Cloudflare")
	}
	hcl, err := RenderStaticSiteHCL(plan)
	if err != nil {
		t.Fatalf("RenderStaticSiteHCL (AWS): %v", err)
	}
	for _, want := range []string{
		`resource "aws_amplify_app" "marketing"`,
		`resource "aws_amplify_branch" "marketing"`,
		`baseDirectory: dist`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("AWS static-site HCL missing %q\n---\n%s", want, hcl)
		}
	}
}

// TestStaticSiteThroughAssemble proves the component is wired into AssembleHCL and
// pins the Cloudflare provider on a DO placement.
func TestStaticSiteThroughAssemble(t *testing.T) {
	t.Parallel()
	docs, err := AssembleHCL(context.Background(), MustEmbedded(), AssembleInput{
		Name: "fe", Provider: "digitalocean", Region: "Frankfurt",
		Components: []AssembleComponent{
			{Name: "vibe", Type: "static-site", StaticSite: &AssembleStaticSite{CustomDomain: "vibe.passo.build"}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL static-site (DO): %v", err)
	}
	all := strings.Join(docs, "\n")
	for _, want := range []string{
		`source = "digitalocean/digitalocean"`,
		`source = "cloudflare/cloudflare"`,
		`resource "digitalocean_spaces_bucket" "vibe"`,
		`resource "cloudflare_dns_record" "vibe-cdn"`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("assembled DO static-site missing %q", want)
		}
	}
}
