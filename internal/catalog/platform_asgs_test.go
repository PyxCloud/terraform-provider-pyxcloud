package catalog

import (
	"context"
	"strings"
	"testing"
)

// TestPlatformServicesCanonicalShape asserts the 5 platform services are each a
// canonical scale-group of 1 with self-heal (min=desired=1) — the abstract source
// of truth that replaces the bespoke per-cloud ASGs (pd-MIG-PORT-ASGS-CANONICAL).
func TestPlatformServicesCanonicalShape(t *testing.T) {
	t.Parallel()
	svcs := PlatformServices()
	wantNames := []string{"sso", "vpn", "obs", "sast", "backend", "mcp"}
	if len(svcs) != len(wantNames) {
		t.Fatalf("want %d platform services, got %d", len(wantNames), len(svcs))
	}
	for i, want := range wantNames {
		if svcs[i].Name != want {
			t.Errorf("service[%d] = %q, want %q (deterministic order)", i, svcs[i].Name, want)
		}
		if svcs[i].MinDesired != 1 {
			t.Errorf("service %q min/desired = %d, want 1 (self-heal floor)", svcs[i].Name, svcs[i].MinDesired)
		}
		if svcs[i].CPU < 1 || svcs[i].RAM < 1 {
			t.Errorf("service %q has invalid sizing cpu=%d ram=%d", svcs[i].Name, svcs[i].CPU, svcs[i].RAM)
		}
	}
}

// TestPlatformScaleGroupComponentsAreScaleGroupsOfOne asserts the helper emits
// scale-group components with min=max=desired=1.
func TestPlatformScaleGroupComponentsAreScaleGroupsOfOne(t *testing.T) {
	t.Parallel()
	comps := PlatformScaleGroupComponents("", "", "")
	if len(comps) != 6 {
		t.Fatalf("want 6 components, got %d", len(comps))
	}
	for _, c := range comps {
		if c.Type != "virtual-machine-scale-group" {
			t.Errorf("component %q type = %q, want virtual-machine-scale-group", c.Name, c.Type)
		}
		if c.ScaleGroup == nil {
			t.Fatalf("component %q has nil ScaleGroup", c.Name)
		}
		sg := c.ScaleGroup
		if sg.Min != 1 || sg.Max != 1 || sg.Desired != 1 {
			t.Errorf("component %q bounds = %d/%d/%d, want 1/1/1", c.Name, sg.Min, sg.Max, sg.Desired)
		}
	}
}

// TestPlatformASGsRoundTripDO is the plan-only round-trip proof: the 5 platform
// services, expressed as canonical scale-groups, descend to VALID DigitalOcean
// resources — each becomes a DOKS node-pool with auto_scale and the self-heal
// min_nodes=1 floor. This is the canonical scale-group -> DOKS mapping the task
// asks for, exercised through the real assembler (no backend, plan-only).
func TestPlatformASGsRoundTripDO(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name:       "platform",
		Provider:   "digitalocean",
		Region:     "Frankfurt",
		Components: PlatformScaleGroupComponents("x86_64", "ubuntu", "1.30"),
	})
	if err != nil {
		t.Fatalf("AssembleHCL platform ASGs (DO): %v", err)
	}
	all := strings.Join(docs, "\n")

	// Provider source pinned (DO is a non-default namespace).
	if !strings.Contains(all, `source = "digitalocean/digitalocean"`) {
		t.Errorf("missing digitalocean provider source pin:\n%s", all)
	}
	// Each of the 5 services -> a DOKS cluster with an auto-scaling, self-healing
	// node-pool (min_nodes=1 = the ASG-of-1 self-heal floor).
	for _, svc := range []string{"sso", "vpn", "obs", "sast", "backend", "mcp"} {
		if !strings.Contains(all, `resource "digitalocean_kubernetes_cluster" "`+svc+`"`) {
			t.Errorf("platform service %q did not emit a DOKS cluster:\n%s", svc, all)
		}
	}
	for _, want := range []string{
		`auto_scale = true`,
		`min_nodes  = 1`, // self-heal floor on every node-pool
		`node_count = 1`, // scale-group of 1
		`version = "1.30"`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("platform DOKS HCL missing %q\n%s", want, all)
		}
	}
	// DO has no native VM ASG: no AWS launch-template / autoscaling-group must leak.
	if strings.Contains(all, "aws_autoscaling_group") || strings.Contains(all, "aws_launch_template") {
		t.Errorf("DO platform ASGs must not emit AWS ASG resources:\n%s", all)
	}
}

// TestPlatformASGsRoundTripAWS proves the SAME canonical topology descends to
// valid AWS resources too (aws_autoscaling_group), confirming the mapping is
// provider-agnostic — the abstract source, two concrete renderings.
func TestPlatformASGsRoundTripAWS(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name:       "platform",
		Provider:   "aws",
		Region:     "Dublin",
		Components: PlatformScaleGroupComponents("x86_64", "ubuntu", ""),
	})
	if err != nil {
		t.Fatalf("AssembleHCL platform ASGs (AWS): %v", err)
	}
	all := strings.Join(docs, "\n")
	// Each service renders an AWS autoscaling group of 1.
	n := strings.Count(all, `resource "aws_autoscaling_group"`)
	if n != 6 {
		t.Errorf("want 6 aws_autoscaling_group resources, got %d\n%s", n, all)
	}
	for _, want := range []string{
		`min_size            = 1`,
		`desired_capacity    = 1`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("AWS platform ASG HCL missing %q\n%s", want, all)
		}
	}
}
