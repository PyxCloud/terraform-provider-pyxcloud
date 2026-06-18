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
		`data "aws_vpc" "default" { default = true }`,
		`data "aws_subnets" "default"`,
		`values = [data.aws_vpc.default.id]`,
		`data "aws_subnet" "production_1"`,
		`id = tolist(data.aws_subnets.default.ids)[0]`,
		`data "aws_subnet" "production_2"`,
		`id = tolist(data.aws_subnets.default.ids)[1]`,
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

func renderSGFor(t *testing.T, provider, region string, spec SecurityGroupSpec) string {
	t.Helper()
	spec.Region = region
	spec.Provider = provider
	plan, err := TranslateSecurityGroup(context.Background(), MustEmbedded(), spec)
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderSGHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	return hcl
}

func TestRenderSGAWS(t *testing.T) {
	t.Parallel()
	hcl := renderSGFor(t, "aws", "Dublin", SecurityGroupSpec{
		Name: "web", Network: "production", Description: "web tier",
		Expose: []int{80, 443},
		Rules: []SecurityRule{
			{Direction: "ingress", Protocol: "tcp", FromPort: 8080, ToPort: 8080, SourceSG: "lb"},
		},
	})
	for _, want := range []string{
		`resource "aws_security_group" "web"`,
		`description = "web tier"`,
		`vpc_id      = data.aws_vpc.default.id`,
		`resource "aws_security_group_rule"`,
		`from_port         = 80`,
		`from_port         = 443`,
		`cidr_blocks       = ["0.0.0.0/0"]`,
		`ipv6_cidr_blocks  = ["::/0"]`,
		`source_security_group_id = aws_security_group.lb.id`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("AWS SG HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestRenderSGGCP(t *testing.T) {
	t.Parallel()
	hcl := renderSGFor(t, "gcp", "Belgium", SecurityGroupSpec{
		Name: "fw", Network: "production", Expose: []int{80},
		Rules: []SecurityRule{
			{Direction: "egress", Protocol: "all", CIDRs: []string{"0.0.0.0/0"}},
		},
	})
	for _, want := range []string{
		`resource "google_compute_firewall" "fw_ingress"`,
		`direction   = "INGRESS"`,
		`resource "google_compute_firewall" "fw_egress"`,
		`direction   = "EGRESS"`,
		`source_ranges = ["0.0.0.0/0", "::/0"]`,
		`destination_ranges = ["0.0.0.0/0"]`,
		`ports    = ["80"]`,
		`network     = google_compute_network.production.id`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("GCP firewall HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestRenderSGDO(t *testing.T) {
	t.Parallel()
	hcl := renderSGFor(t, "digitalocean", "Amsterdam", SecurityGroupSpec{
		Name: "edge-fw", Expose: []int{443},
		Rules: []SecurityRule{
			{Direction: "egress", Protocol: "tcp", FromPort: 0, ToPort: 65535, CIDRs: []string{"0.0.0.0/0"}},
		},
	})
	for _, want := range []string{
		`resource "digitalocean_firewall" "edge-fw"`,
		`inbound_rule {`,
		`outbound_rule {`,
		`port_range = "443"`,
		`port_range = "0-65535"`,
		`source_addresses = ["0.0.0.0/0", "::/0"]`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("DO firewall HCL missing %q\n%s", want, hcl)
		}
	}
}
