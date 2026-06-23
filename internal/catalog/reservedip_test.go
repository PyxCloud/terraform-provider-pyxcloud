package catalog

import (
	"context"
	"strings"
	"testing"
)

// TestTranslateReservedIPDO asserts the DO migration target for the VPN stable
// endpoint: catalog csp_region, attach target carried, and the
// digitalocean_reserved_ip resource type.
func TestTranslateReservedIPDO(t *testing.T) {
	t.Parallel()
	plan, err := TranslateReservedIP(context.Background(), MustEmbedded(), ReservedIPSpec{
		Name:     "vpn-endpoint",
		Region:   "Frankfurt",
		Provider: "digitalocean",
		AttachTo: "vpn",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSPRegion != "fra1" {
		t.Errorf("csp_region = %q, want fra1", plan.CSPRegion)
	}
	if plan.ResourceType != "digitalocean_reserved_ip" {
		t.Errorf("resource_type = %q, want digitalocean_reserved_ip", plan.ResourceType)
	}
	if plan.AttachTo != "vpn" {
		t.Errorf("attach_to = %q, want vpn", plan.AttachTo)
	}
	if plan.LogicalName != "vpn-endpoint" {
		t.Errorf("logical_name = %q, want vpn-endpoint", plan.LogicalName)
	}
}

func TestTranslateReservedIPAWS(t *testing.T) {
	t.Parallel()
	plan, err := TranslateReservedIP(context.Background(), MustEmbedded(), ReservedIPSpec{
		Name: "vpn-endpoint", Region: "Frankfurt", Provider: "aws",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "aws_eip" {
		t.Errorf("resource_type = %q, want aws_eip", plan.ResourceType)
	}
	if plan.CSPRegion != "eu-central-1" {
		t.Errorf("csp_region = %q, want eu-central-1", plan.CSPRegion)
	}
}

func TestTranslateReservedIPGCP(t *testing.T) {
	t.Parallel()
	plan, err := TranslateReservedIP(context.Background(), MustEmbedded(), ReservedIPSpec{
		Name: "vpn-endpoint", Region: "Frankfurt", Provider: "gcp",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "google_compute_address" {
		t.Errorf("resource_type = %q, want google_compute_address", plan.ResourceType)
	}
}

// TestRenderReservedIPDO asserts the DO HCL reserves a region IP and binds it to
// the droplet when attached (the re-attachable stable VPN endpoint).
func TestRenderReservedIPDO(t *testing.T) {
	t.Parallel()
	plan, err := TranslateReservedIP(context.Background(), MustEmbedded(), ReservedIPSpec{
		Name: "vpn-endpoint", Region: "Frankfurt", Provider: "digitalocean", AttachTo: "vpn",
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderReservedIPHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "digitalocean_reserved_ip" "vpn-endpoint"`,
		`region = "fra1"`,
		"droplet_id = digitalocean_droplet.vpn.id",
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("DO reserved-ip HCL missing %q\n%s", want, hcl)
		}
	}
}

// TestRenderReservedIPDOUnattached asserts an unattached reserved IP is valid and
// emits no droplet binding.
func TestRenderReservedIPDOUnattached(t *testing.T) {
	t.Parallel()
	plan, err := TranslateReservedIP(context.Background(), MustEmbedded(), ReservedIPSpec{
		Name: "spare", Region: "Frankfurt", Provider: "digitalocean",
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderReservedIPHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hcl, `resource "digitalocean_reserved_ip" "spare"`) {
		t.Errorf("missing reserved_ip resource\n%s", hcl)
	}
	if strings.Contains(hcl, "droplet_id") {
		t.Errorf("unattached reserved IP must NOT bind a droplet\n%s", hcl)
	}
}

func TestRenderReservedIPAWSAttached(t *testing.T) {
	t.Parallel()
	plan, err := TranslateReservedIP(context.Background(), MustEmbedded(), ReservedIPSpec{
		Name: "vpn-endpoint", Region: "Frankfurt", Provider: "aws", AttachTo: "vpn",
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderReservedIPHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "aws_eip" "vpn-endpoint"`,
		`domain = "vpc"`,
		"instance = aws_instance.vpn.id",
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("AWS EIP HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestRenderReservedIPUnsupportedProvider(t *testing.T) {
	t.Parallel()
	_, err := RenderReservedIPHCL(ReservedIPPlan{Provider: ProviderLinode})
	if err == nil {
		t.Fatal("expected a hard render-time error for an unsupported provider")
	}
}

func TestCanonicalReservedIPType(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"reserved-ip", "static-ip", "elastic-ip", "Elastic-IP"} {
		if got, ok := CanonicalReservedIPType(in); !ok || got != TypeReservedIP {
			t.Errorf("CanonicalReservedIPType(%q) = %q,%v", in, got, ok)
		}
	}
	if _, ok := CanonicalReservedIPType("load-balancer"); ok {
		t.Error("load-balancer should not be a reserved-ip type")
	}
}
