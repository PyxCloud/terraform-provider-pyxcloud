package catalog

import (
	"context"
	"strings"
	"testing"
)

func TestTranslateNetworkAWS(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateNetwork(context.Background(), cat, NetworkSpec{
		Name:     "production",
		Region:   "Dublin",
		Provider: "aws",
		CIDR:     "10.0.0.0/16",
		Subnets:  []string{"10.0.1.0/24", "10.0.2.0/24", "10.0.3.0/24"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSPRegion != "eu-west-1" {
		t.Errorf("csp_region = %q, want eu-west-1", plan.CSPRegion)
	}
	if plan.ResourceType != "aws_vpc" {
		t.Errorf("resource_type = %q, want aws_vpc", plan.ResourceType)
	}
	if len(plan.Subnets) != 3 {
		t.Fatalf("want 3 subnets, got %d", len(plan.Subnets))
	}
	// Multi-AZ derivation: eu-west-1a, eu-west-1b, eu-west-1c.
	wantZones := []string{"eu-west-1a", "eu-west-1b", "eu-west-1c"}
	for i, s := range plan.Subnets {
		if s.Zone != wantZones[i] {
			t.Errorf("subnet %d zone = %q, want %q", i, s.Zone, wantZones[i])
		}
		if !strings.HasPrefix(s.Name, "production-subnet-") {
			t.Errorf("subnet %d name = %q, want production-subnet-*", i, s.Name)
		}
	}
}

func TestTranslateNetworkGCP(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateNetwork(context.Background(), cat, NetworkSpec{
		Name:     "prod",
		Region:   "Belgium",
		Provider: "gcp",
		CIDR:     "10.0.0.0/16",
		Subnets:  []string{"10.0.10.0/24", "10.0.20.0/24"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSPRegion != "europe-west1" {
		t.Errorf("csp_region = %q, want europe-west1", plan.CSPRegion)
	}
	if plan.ResourceType != "google_compute_network" {
		t.Errorf("resource_type = %q, want google_compute_network", plan.ResourceType)
	}
	wantZones := []string{"europe-west1-a", "europe-west1-b"}
	for i, s := range plan.Subnets {
		if s.Zone != wantZones[i] {
			t.Errorf("subnet %d zone = %q, want %q", i, s.Zone, wantZones[i])
		}
	}
}

func TestTranslateNetworkDO(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	plan, err := TranslateNetwork(context.Background(), cat, NetworkSpec{
		Name:     "edge",
		Region:   "Amsterdam",
		Provider: "digitalocean",
		CIDR:     "10.0.0.0/16",
		Subnets:  []string{"10.0.1.0/24"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSPRegion != "ams3" {
		t.Errorf("csp_region = %q, want ams3", plan.CSPRegion)
	}
	if plan.CSP != "do" {
		t.Errorf("csp = %q, want do", plan.CSP)
	}
	if plan.ResourceType != "digitalocean_vpc" {
		t.Errorf("resource_type = %q, want digitalocean_vpc", plan.ResourceType)
	}
	// DO VPCs are region-scoped: no zones on subnets.
	for i, s := range plan.Subnets {
		if s.Zone != "" {
			t.Errorf("DO subnet %d should have no zone, got %q", i, s.Zone)
		}
	}
}

func TestTranslateNetworkMissingRegionIsHardError(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	// Dublin has no DigitalOcean entry -> plan-time error, never a fallback.
	_, err := TranslateNetwork(context.Background(), cat, NetworkSpec{
		Region: "Dublin", Provider: "digitalocean", CIDR: "10.0.0.0/16",
	})
	if err == nil {
		t.Fatal("expected hard error for Dublin/digitalocean, got nil")
	}
}

func TestTranslateNetworkValidation(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	cases := []struct {
		name string
		spec NetworkSpec
	}{
		{"missing region", NetworkSpec{Provider: "aws", CIDR: "10.0.0.0/16"}},
		{"missing provider", NetworkSpec{Region: "Dublin", CIDR: "10.0.0.0/16"}},
		{"missing cidr", NetworkSpec{Region: "Dublin", Provider: "aws"}},
		{"bad cidr", NetworkSpec{Region: "Dublin", Provider: "aws", CIDR: "nope"}},
		{"bad subnet cidr", NetworkSpec{Region: "Dublin", Provider: "aws", CIDR: "10.0.0.0/16", Subnets: []string{"oops"}}},
		{"subnet outside vpc", NetworkSpec{Region: "Dublin", Provider: "aws", CIDR: "10.0.0.0/16", Subnets: []string{"192.168.1.0/24"}}},
		{"subnet wider than vpc", NetworkSpec{Region: "Dublin", Provider: "aws", CIDR: "10.0.0.0/24", Subnets: []string{"10.0.0.0/16"}}},
	}
	for _, c := range cases {
		if _, err := TranslateNetwork(context.Background(), cat, c.spec); err == nil {
			t.Errorf("%s: expected validation error, got nil", c.name)
		}
	}
}
