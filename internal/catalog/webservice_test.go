package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestTranslateWebServiceDO(t *testing.T) {
	t.Parallel()
	plan, err := TranslateWebService(context.Background(), MustEmbedded(), WebServiceSpec{
		Name: "mcp-server", Region: "Frankfurt", Provider: "digitalocean",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "digitalocean_app" {
		t.Errorf("resource_type = %q, want digitalocean_app", plan.ResourceType)
	}
	if plan.CSPRegion != "fra1" {
		t.Errorf("csp_region = %q, want fra1", plan.CSPRegion)
	}
	// Always-on defaults: git source, 1 instance, basic-xxs, port 8080.
	if plan.SourceKind != "git" || plan.InstanceCount != 1 || plan.InstanceSize != "basic-xxs" || plan.HTTPPort != 8080 {
		t.Errorf("unexpected defaults: %+v", plan)
	}
}

// TestTranslateWebServiceUnsupportedProvider asserts aws/gcp surface a clean
// ErrComponentUnsupported (App Platform is DO-only in this wave), never a fallback.
func TestTranslateWebServiceUnsupportedProvider(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"aws", "gcp"} {
		_, err := TranslateWebService(context.Background(), MustEmbedded(), WebServiceSpec{
			Name: "svc", Region: "Frankfurt", Provider: provider,
		})
		var unsupported ErrComponentUnsupported
		if !errors.As(err, &unsupported) {
			t.Errorf("provider %q: want ErrComponentUnsupported, got %v", provider, err)
		}
	}
}

// TestWebServiceValidation covers invalid specs rejected at translate time.
func TestWebServiceValidation(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	cases := []struct {
		name string
		spec WebServiceSpec
	}{
		{"missing region", WebServiceSpec{Provider: "digitalocean"}},
		{"missing provider", WebServiceSpec{Region: "Frankfurt"}},
		{"bad source kind", WebServiceSpec{Region: "Frankfurt", Provider: "digitalocean", SourceKind: "svn"}},
		{"image without repository", WebServiceSpec{Region: "Frankfurt", Provider: "digitalocean", SourceKind: "image"}},
		{"negative instance count", WebServiceSpec{Region: "Frankfurt", Provider: "digitalocean", InstanceCount: -2}},
	}
	for _, c := range cases {
		if _, err := TranslateWebService(context.Background(), cat, c.spec); err == nil {
			t.Errorf("%s: expected error, got nil", c.name)
		}
	}
}

// TestRenderWebServiceDOGit asserts the App Platform service render for a git
// source, with env (sorted), a health check and a custom domain.
func TestRenderWebServiceDOGit(t *testing.T) {
	t.Parallel()
	plan, err := TranslateWebService(context.Background(), MustEmbedded(), WebServiceSpec{
		Name: "mcp-server", Region: "Frankfurt", Provider: "digitalocean",
		HTTPPort: 8787, InstanceCount: 2, HealthCheckPath: "/health",
		Env:          map[string]string{"ZED": "1", "ALPHA": "2"},
		CustomDomain: "mcp.passo.build",
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderWebServiceHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "digitalocean_app" "mcp-server"`,
		`service {`,
		`instance_size_slug = "basic-xxs"`,
		`instance_count     = 2`,
		`http_port          = 8787`,
		`repo_clone_url = var.mcp-server_repo_url`,
		`health_check {`,
		`http_path = "/health"`,
		`name = "mcp.passo.build"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("web-service HCL missing %q:\n%s", want, hcl)
		}
	}
	// Env must render in sorted key order (ALPHA before ZED) for a stable plan.
	if strings.Index(hcl, `key   = "ALPHA"`) > strings.Index(hcl, `key   = "ZED"`) {
		t.Errorf("env not sorted:\n%s", hcl)
	}
	if !IsASCII(hcl) {
		t.Errorf("web-service HCL not ASCII:\n%s", hcl)
	}
}

// TestRenderWebServiceDOImage asserts the image-source render path.
func TestRenderWebServiceDOImage(t *testing.T) {
	t.Parallel()
	plan, err := TranslateWebService(context.Background(), MustEmbedded(), WebServiceSpec{
		Name: "inaudito", Region: "Frankfurt", Provider: "digitalocean",
		SourceKind: "image", ImageRegistryType: "DOCR", ImageRepository: "inaudito", ImageTag: "v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderWebServiceHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`image {`,
		`registry_type = "DOCR"`,
		`repository    = "inaudito"`,
		`tag           = "v1"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("web-service image HCL missing %q:\n%s", want, hcl)
		}
	}
	if strings.Contains(hcl, "git {") {
		t.Errorf("image source must not emit a git block:\n%s", hcl)
	}
}

// TestRenderWebServiceUnsupportedProvider is defence-in-depth for a hand-built plan.
func TestRenderWebServiceUnsupportedProvider(t *testing.T) {
	t.Parallel()
	if _, err := RenderWebServiceHCL(WebServicePlan{Provider: "aws"}); err == nil {
		t.Fatal("expected render error for non-DO provider, got nil")
	}
}

func TestCanonicalWebServiceType(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"web-service", "app-service", "app-platform-service"} {
		if got, ok := CanonicalWebServiceType(in); !ok || got != TypeWebService {
			t.Errorf("CanonicalWebServiceType(%q) = %q,%v; want web-service,true", in, got, ok)
		}
	}
	if _, ok := CanonicalWebServiceType("container-service"); ok {
		t.Error("container-service must NOT resolve to web-service (it is a k8s alias)")
	}
}
