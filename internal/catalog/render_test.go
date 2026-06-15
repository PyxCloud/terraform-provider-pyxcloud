package catalog

import (
	"context"
	"strings"
	"testing"
)

func renderFor(t *testing.T, provider, region string, subnets []string) string {
	t.Helper()
	plan, err := TranslateNetwork(context.Background(), MustEmbedded(), NetworkSpec{
		Name: "production", Region: region, Provider: provider,
		CIDR: "10.0.0.0/16", Subnets: subnets,
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	return hcl
}

func TestRenderAWS(t *testing.T) {
	t.Parallel()
	hcl := renderFor(t, "aws", "Dublin", []string{"10.0.1.0/24", "10.0.2.0/24"})
	for _, want := range []string{
		`resource "aws_vpc"`, `cidr_block = "10.0.0.0/16"`,
		`resource "aws_subnet"`, `availability_zone = "eu-west-1a"`,
		`availability_zone = "eu-west-1b"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("AWS HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestRenderGCP(t *testing.T) {
	t.Parallel()
	hcl := renderFor(t, "gcp", "Belgium", []string{"10.0.1.0/24"})
	for _, want := range []string{
		`resource "google_compute_network"`, `auto_create_subnetworks = false`,
		`resource "google_compute_subnetwork"`, `region        = "europe-west1"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("GCP HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestRenderDO(t *testing.T) {
	t.Parallel()
	hcl := renderFor(t, "digitalocean", "Amsterdam", []string{"10.0.1.0/24"})
	for _, want := range []string{
		`resource "digitalocean_vpc"`, `region   = "ams3"`, `ip_range = "10.0.1.0/24"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("DO HCL missing %q\n%s", want, hcl)
		}
	}
}
