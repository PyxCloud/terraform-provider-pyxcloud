package catalog

import (
	"strings"
	"testing"
)

// TestRenderSastDOBootstrapRequiresEnvironment asserts the bootstrap refuses to
// render without an environment (it drives the Spaces bucket + pool name).
func TestRenderSastDOBootstrapRequiresEnvironment(t *testing.T) {
	t.Parallel()
	if _, err := RenderSastDOBootstrapUserData(SastDOBootstrapSpec{}); err == nil {
		t.Fatal("want error for missing environment, got nil")
	}
}

// TestRenderSastDOBootstrapFaithfulPort asserts the rendered cloud-init carries
// the substance of the AWS sast_runner_user_data with the three DO swaps: the DO
// registry image + docker login, the Spaces job-queue poll (same scan-jobs key
// layout) via the S3-compatible endpoint, Semgrep + OSV, results back to Spaces,
// and the self-scale-down via the DO droplet_autoscale API.
func TestRenderSastDOBootstrapFaithfulPort(t *testing.T) {
	t.Parallel()
	ud, err := RenderSastDOBootstrapUserData(SastDOBootstrapSpec{Environment: "beta"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	mustContain := []string{
		"#!/bin/bash",
		"apt-get install -y unzip curl jq docker.io",                          // docker install
		"registry.digitalocean.com/pyx-registry/pyx-sast:latest",             // DO registry image
		"docker login registry.digitalocean.com",                             // DO registry login
		"docker pull \"$REGISTRY_IMAGE\"",                                     // pull image
		"pyx-sast-jobs-fra1",                                                  // default Spaces bucket
		"https://fra1.digitaloceanspaces.com",                                // Spaces S3-compatible endpoint
		"--endpoint-url",                                                      // aws CLI against Spaces
		"scan-jobs/$JOB_ID/input/repo.zip",                                   // same input key layout as AWS
		"scan-jobs/$JOB_ID/output/semgrep_output.json",                       // same output key layout
		"/usr/local/bin/semgrep",                                             // Semgrep
		"/usr/local/bin/osv-scanner",                                         // OSV
		"https://api.digitalocean.com/v2/droplets/autoscale",                 // DO autoscale API (self-scale)
		"beta-sast",                                                          // default pool name (<env>-sast)
	}
	for _, want := range mustContain {
		if !strings.Contains(ud, want) {
			t.Errorf("rendered SAST DO bootstrap missing %q", want)
		}
	}
	// It must NOT actually invoke the AWS dispatch/registry primitives — this is
	// the whole point of the port. (The awscli installer URL and the explanatory
	// comment naming the AWS equivalent are fine; we forbid the live invocations.)
	for _, forbidden := range []string{
		"aws autoscaling set-desired-capacity --auto-scaling-group-name",
		"ecr get-login-password",
		"s3://" + "$" + "BUCKET/scan-jobs/$JOB_ID/input/repo.zip\" \"/tmp/$JOB_ID/repo.zip\" --region", // AWS --region arg
	} {
		if strings.Contains(ud, forbidden) {
			t.Errorf("rendered SAST DO bootstrap must not invoke AWS primitive %q", forbidden)
		}
	}
}

// TestRenderSastDOBootstrapInlinesNoSecretValues is the security invariant:
// every credential is referenced by Terraform variable, never inlined.
func TestRenderSastDOBootstrapInlinesNoSecretValues(t *testing.T) {
	t.Parallel()
	ud, err := RenderSastDOBootstrapUserData(SastDOBootstrapSpec{Environment: "beta"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, ref := range []string{
		"${var.do_spaces_access_key}",
		"${var.do_spaces_secret_key}",
		"${var.do_registry_token}",
		"${var.do_api_token}",
	} {
		if !strings.Contains(ud, ref) {
			t.Errorf("expected secret to be referenced by variable %q, but it was not", ref)
		}
	}
}

// TestSastDOScaleDownFloorIsOne asserts the DO limitation is enforced: a zero (or
// negative) self-scale-down target is clamped to 1, because a DO
// droplet_autoscale pool cannot scale to zero like an AWS ASG.
func TestSastDOScaleDownFloorIsOne(t *testing.T) {
	t.Parallel()
	ud, err := RenderSastDOBootstrapUserData(SastDOBootstrapSpec{Environment: "beta", ScaleDownTo: 0})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(ud, "SCALE_DOWN_TO=1") {
		t.Errorf("expected zero scale-down target to be clamped to the DO floor of 1, got:\n%s", ud)
	}
}

// TestSastDOBootstrapVariableNamesSensitive asserts all four credentials are
// partitioned as sensitive so the assembler marks the emitted variable blocks.
func TestSastDOBootstrapVariableNamesSensitive(t *testing.T) {
	t.Parallel()
	plain, sensitive := SastDOBootstrapSpec{Environment: "beta"}.SastDOBootstrapVariableNames()
	if len(plain) != 0 {
		t.Errorf("expected no plain vars, got %v", plain)
	}
	want := map[string]bool{
		"do_spaces_access_key": true, "do_spaces_secret_key": true,
		"do_registry_token": true, "do_api_token": true,
	}
	if len(sensitive) != len(want) {
		t.Fatalf("expected %d sensitive vars, got %v", len(want), sensitive)
	}
	for _, s := range sensitive {
		if !want[s] {
			t.Errorf("unexpected sensitive var %q", s)
		}
	}
}

// TestPlatformProviderBootstrapWiresSastDO asserts the wiring point threads a
// per-provider bootstrap onto the sast scale-group's UserDataByProvider without
// touching the other services or the generic UserData.
func TestPlatformProviderBootstrapWiresSastDO(t *testing.T) {
	t.Parallel()
	doUD, err := RenderSastDOBootstrapUserData(SastDOBootstrapSpec{Environment: "beta"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	comps := PlatformScaleGroupComponentsWithProviderBootstrap("", "", "", nil,
		PlatformBootstrapsByProvider{"sast": {"digitalocean": doUD}})
	var found bool
	for _, c := range comps {
		if c.Name != "sast" {
			if c.ScaleGroup != nil && len(c.ScaleGroup.UserDataByProvider) != 0 {
				t.Errorf("service %q unexpectedly carries a per-provider bootstrap", c.Name)
			}
			continue
		}
		found = true
		if c.ScaleGroup == nil {
			t.Fatal("sast component has no scale-group")
		}
		got := c.ScaleGroup.UserDataByProvider["digitalocean"]
		if !strings.Contains(got, "registry.digitalocean.com/pyx-registry/pyx-sast:latest") {
			t.Errorf("sast digitalocean bootstrap not wired; got %q", got)
		}
	}
	if !found {
		t.Fatal("no sast component emitted")
	}
}
