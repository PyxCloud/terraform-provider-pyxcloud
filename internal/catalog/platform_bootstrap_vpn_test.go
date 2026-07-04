package catalog

import (
	"strings"
	"testing"
)

// TestRenderVPNBootstrapFaithfulReArch asserts the rendered DigitalOcean
// cloud-init carries the substance of the hand-written PyxCloud/internal-vpn
// wireguard/user_data.sh, RE-ARCHED to DO primitives: apt (not dnf), a
// block-storage volume state dir (not SSM), a DO-API DNS refresh (not
// `ec2 describe-instances`), and a DO Cloud Firewall prune (not
// `revoke-security-group-ingress`).
func TestRenderVPNBootstrapFaithfulReArch(t *testing.T) {
	t.Parallel()
	ud, err := RenderVPNBootstrapUserData(VPNBootstrapSpec{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	mustContain := []string{
		"#!/bin/bash",
		"apt-get install -y wireguard wireguard-tools dnsmasq", // Ubuntu apt (not dnf)
		"ListenPort = 51820",                                   // WireGuard port
		"Address = 10.8.0.1/24",                                // tunnel address
		"/mnt/wireguard-state",                                 // DO block-storage state dir (replaces SSM)
		"api.digitalocean.com/v2/droplets?tag_name=",           // DO-API DNS refresh
		"api.digitalocean.com/v2/firewalls/",                   // DO Cloud Firewall prune
		"wg-quick@wg0",
		"systemctl enable --now dnsmasq",
		"wg-internal-dns-refresh.timer",
		"wg-prune-inactive.timer",
	}
	for _, want := range mustContain {
		if !strings.Contains(ud, want) {
			t.Errorf("rendered VPN DO bootstrap missing %q", want)
		}
	}
	// It must NOT reference AWS control-plane primitives (the re-arch removed them).
	for _, forbidden := range []string{
		"aws ssm get-parameter",
		"ec2 describe-instances",
		"revoke-security-group-ingress",
		"associate-address",
		"dnf install",
	} {
		if strings.Contains(ud, forbidden) {
			t.Errorf("rendered VPN DO bootstrap must not carry AWS primitive %q", forbidden)
		}
	}
}

// TestRenderVPNBootstrapInjectsIdentityFromVars is the cutover-identity invariant:
// the server private key + the persisted peer block are INJECTED from Terraform
// variables (wired to AWS SSM during cutover) so the DO server is the same
// WireGuard identity as the warm AWS box — and never inlined as a literal.
func TestRenderVPNBootstrapInjectsIdentityFromVars(t *testing.T) {
	t.Parallel()
	ud, err := RenderVPNBootstrapUserData(VPNBootstrapSpec{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, ref := range []string{
		"${var.wg_server_private_key}", // seeded from AWS SSM /wireguard/server-private-key
		"${var.wg_peers_blob}",         // seeded from AWS SSM /wireguard/peers
		"${var.do_api_token}",          // DO API token for refresh + firewall
		"${var.do_wg_jit_firewall_id}", // DO Cloud Firewall id for the prune
	} {
		if !strings.Contains(ud, ref) {
			t.Errorf("expected value to be referenced by variable %q, but it was not", ref)
		}
	}
}

// TestRenderVPNBootstrapDeterministic asserts byte-stable output (the DNS host
// map is iterated in sorted order), so the rendered launch template does not
// churn Terraform state across identical renders.
func TestRenderVPNBootstrapDeterministic(t *testing.T) {
	t.Parallel()
	spec := VPNBootstrapSpec{}
	a, err := RenderVPNBootstrapUserData(spec)
	if err != nil {
		t.Fatalf("render a: %v", err)
	}
	b, err := RenderVPNBootstrapUserData(spec)
	if err != nil {
		t.Fatalf("render b: %v", err)
	}
	if a != b {
		t.Error("VPN DO bootstrap render is not deterministic")
	}
}

// TestVPNBootstrapVariableNames asserts the secret vars are reported as sensitive
// so the assembler emits `sensitive = true` declarations for them.
func TestVPNBootstrapVariableNames(t *testing.T) {
	t.Parallel()
	plain, sensitive := VPNBootstrapSpec{}.VPNBootstrapVariableNames()
	joinS := strings.Join(sensitive, ",")
	for _, want := range []string{"wg_server_private_key", "wg_peers_blob", "do_api_token"} {
		if !strings.Contains(joinS, want) {
			t.Errorf("expected %q to be a sensitive variable, sensitive=%v", want, sensitive)
		}
	}
	if len(plain) == 0 {
		t.Error("expected at least one plain variable (the DO firewall id)")
	}
}

// TestPlatformBootstrapWiresVPNUserDataByProvider is the integration proof: the
// VPN DO bootstrap threaded via PlatformScaleGroupComponentsWithBootstraps lands
// in the "vpn" scale-group's UserDataByProvider["digitalocean"], where the
// scale-group translator prefers it on a DigitalOcean render.
func TestPlatformBootstrapWiresVPNUserDataByProvider(t *testing.T) {
	t.Parallel()
	vpnUD, err := RenderVPNBootstrapUserData(VPNBootstrapSpec{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	comps := PlatformScaleGroupComponentsWithBootstraps(
		"x86_64", "ubuntu", "",
		nil,
		PlatformBootstrapsByProvider{
			"vpn": {"digitalocean": vpnUD},
		},
	)
	var found bool
	for _, c := range comps {
		if c.Name != "vpn" {
			continue
		}
		found = true
		if c.ScaleGroup == nil {
			t.Fatal("vpn component has no scale-group")
		}
		got := c.ScaleGroup.UserDataByProvider["digitalocean"]
		if got != vpnUD {
			t.Errorf("vpn UserDataByProvider[digitalocean] not wired; got %d bytes", len(got))
		}
	}
	if !found {
		t.Fatal("no vpn scale-group component produced")
	}
}
