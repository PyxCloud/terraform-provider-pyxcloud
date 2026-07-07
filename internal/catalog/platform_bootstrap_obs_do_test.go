package catalog

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenderOBSDOBootstrapUbuntuNoCloudWatch is the core assertion for
// pd-MIG-CUTOVER-F2-02: the observability DigitalOcean bootstrap is Ubuntu-ified
// (apt, not dnf) and drops the AWS-only CloudWatch poller (OBS_USE_AWS), while
// keeping the faithful obs substance (Spaces artifact pull, systemd unit,
// self-signed nginx :443, /healthz on :8080, the mesh poller).
func TestRenderOBSDOBootstrapUbuntuNoCloudWatch(t *testing.T) {
	t.Parallel()
	ud, err := RenderOBSDOBootstrapUserData(OBSDOBootstrapSpec{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// Ubuntu-ified: apt present, dnf absent.
	if !strings.Contains(ud, "apt-get install -y") {
		t.Errorf("obs DO bootstrap must use apt (Ubuntu), got:\n%s", ud)
	}
	if strings.Contains(ud, "dnf ") || strings.Contains(ud, "dnf install") {
		t.Errorf("obs DO bootstrap must NOT use dnf (that is AL2023/AWS):\n%s", ud)
	}

	// CloudWatch poller dropped: no OBS_USE_AWS anywhere.
	if strings.Contains(ud, "OBS_USE_AWS") {
		t.Errorf("obs DO bootstrap must DROP OBS_USE_AWS / the CloudWatch poller (no CloudWatch on DO):\n%s", ud)
	}

	// Faithful obs substance kept.
	mustContain := []string{
		"#!/usr/bin/env bash",
		"s3://pyx-artifacts-fra1/beta/observability.tar.gz", // DO Spaces artifact
		"fra1.digitaloceanspaces.com",                       // Spaces endpoint (S3-compatible)
		"nginx",                                             // self-signed :443 front
		"listen 443 ssl;",
		"/healthz",                                     // health gate
		"127.0.0.1:8080",                               // app port matches AWS
		"observability.service",                        // systemd unit
		"OBS_MESH_MCP_URL=https://mcp.passo.build/mcp", // mesh poller kept
	}
	for _, want := range mustContain {
		if !strings.Contains(ud, want) {
			t.Errorf("obs DO bootstrap missing %q:\n%s", want, ud)
		}
	}
}

// TestRenderOBSDOBootstrapInlinesNoSecretValues is the security invariant: the
// Spaces keys and the Vault AppRole role_id/secret_id are referenced by
// Terraform variable, never embedded as a literal.
func TestRenderOBSDOBootstrapInlinesNoSecretValues(t *testing.T) {
	t.Parallel()
	ud, err := RenderOBSDOBootstrapUserData(OBSDOBootstrapSpec{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, ref := range []string{
		"${var.do_spaces_access_key}",
		"${var.do_spaces_secret_key}",
		"${var.vault_addr}",
		"${var.obs_vault_role_id}",
		"${var.obs_vault_secret_id}",
	} {
		if !strings.Contains(ud, ref) {
			t.Errorf("obs DO bootstrap missing secret variable reference %q (secrets must be injected at render, not inlined):\n%s", ref, ud)
		}
	}
}

// TestRenderOBSDOBootstrapVaultBootFetch asserts the obs DO bootstrap fetches
// the mesh client secret from DO Vault at BOOT time (AppRole login + KV-v2
// read of the observability env leaf), mirroring the AWS module's live
// `aws secretsmanager get-secret-value --secret-id beta/observability-env` +
// jq extraction — instead of a render-time Terraform variable.
func TestRenderOBSDOBootstrapVaultBootFetch(t *testing.T) {
	t.Parallel()
	ud, err := RenderOBSDOBootstrapUserData(OBSDOBootstrapSpec{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	mustContain := []string{
		"auth/approle/login",
		"/v1/secret/data/infra/staging/observability/env",
		"python3",
		"OBS_MESH_CLIENT_SECRET",
	}
	for _, want := range mustContain {
		if !strings.Contains(ud, want) {
			t.Errorf("obs DO bootstrap missing Vault boot-fetch %q:\n%s", want, ud)
		}
	}
	// The old render-time mesh-secret Terraform variable must be gone: the
	// secret now comes from a live Vault read, not a baked-in variable.
	if strings.Contains(ud, "obs_mesh_client_secret") {
		t.Errorf("obs DO bootstrap must NOT reference the retired obs_mesh_client_secret render-time variable:\n%s", ud)
	}
	// jq is no longer apt-installed or invoked as a command (python3 does the
	// JSON parsing per the boot-fetch helper contract); only prose comments may
	// still mention the word "jq" when explaining the swap.
	if strings.Contains(ud, "apt-get install -y curl jq") || strings.Contains(ud, "| jq") {
		t.Errorf("obs DO bootstrap must use python3, not jq, for JSON parsing:\n%s", ud)
	}
}

// TestOBSDOBootstrapWiredIntoScaleGroup asserts the obs DO bootstrap lands in the
// obs scale-group's UserDataByProvider["digitalocean"] slot (and only obs), so a
// DigitalOcean placement descends it to the droplet_autoscale user_data while
// other services/providers are untouched.
func TestOBSDOBootstrapWiredIntoScaleGroup(t *testing.T) {
	t.Parallel()
	byProv, err := PlatformBootstrapsWithOBSDO(nil, OBSDOBootstrapSpec{})
	if err != nil {
		t.Fatalf("PlatformBootstrapsWithOBSDO: %v", err)
	}
	comps := PlatformScaleGroupComponentsWithProviderBootstrap("x86_64", "ubuntu", "", nil, byProv)

	for _, c := range comps {
		if c.ScaleGroup == nil {
			t.Fatalf("component %q has nil ScaleGroup", c.Name)
		}
		got := c.ScaleGroup.UserDataByProvider["digitalocean"]
		if c.Name == "obs" {
			if !strings.Contains(got, "apt-get install -y") || strings.Contains(got, "OBS_USE_AWS") {
				t.Errorf("obs scale-group DO user_data not wired correctly:\n%s", got)
			}
		} else if got != "" {
			t.Errorf("service %q must not receive the obs DO bootstrap, got:\n%s", c.Name, got)
		}
	}
}

// TestOBSDOBootstrapRoundTripDO proves the wired obs DO bootstrap descends,
// through the real assembler, into the obs droplet_autoscale pool's user_data on
// a DigitalOcean placement (apt present, OBS_USE_AWS absent).
func TestOBSDOBootstrapRoundTripDO(t *testing.T) {
	t.Parallel()
	byProv, err := PlatformBootstrapsWithOBSDO(nil, OBSDOBootstrapSpec{})
	if err != nil {
		t.Fatalf("PlatformBootstrapsWithOBSDO: %v", err)
	}
	cat := MustEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name:       "platform",
		Provider:   "digitalocean",
		Region:     "Frankfurt",
		Components: PlatformScaleGroupComponentsWithProviderBootstrap("x86_64", "ubuntu", "1.30", nil, byProv),
	})
	if err != nil {
		t.Fatalf("AssembleHCL platform obs (DO): %v", err)
	}
	all := strings.Join(docs, "\n")

	if !strings.Contains(all, `resource "digitalocean_droplet_autoscale" "obs"`) {
		t.Fatalf("obs droplet_autoscale pool not emitted:\n%s", all)
	}
	if !strings.Contains(all, "apt-get install -y") {
		t.Errorf("obs DO user_data (apt) did not reach the rendered HCL:\n%s", all)
	}
	if strings.Contains(all, "OBS_USE_AWS") {
		t.Errorf("rendered DO HCL must not contain OBS_USE_AWS (CloudWatch dropped):\n%s", all)
	}
}

// Regression: a fmt verb/argument mismatch in any w(...) call leaks literal
// "%!s(MISSING)" / "%!(EXTRA ...)" markers into the rendered script — Go's fmt
// does NOT error on these — and the "(EXTRA" parenthesis is a bash syntax
// error that bricked the obs droplet bootstrap on 2026-07-07. Assert the
// rendered script is marker-free AND passes `bash -n`.
func TestRenderOBSDOBootstrapNoFmtMarkersAndBashParses(t *testing.T) {
	ud, err := RenderOBSDOBootstrapUserData(OBSDOBootstrapSpec{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(ud, "%!") {
		t.Fatalf("rendered user_data contains fmt error markers (%%! ...):\n%s", ud)
	}
	f := filepath.Join(t.TempDir(), "ud.sh")
	if err := os.WriteFile(f, []byte(ud), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if out, err := exec.Command("bash", "-n", f).CombinedOutput(); err != nil {
		t.Fatalf("bash -n failed: %v\n%s", err, out)
	}
}
