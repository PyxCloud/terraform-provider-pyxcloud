package catalog

import (
	"strings"
	"testing"
)

// TestRenderSSOBootstrapRequiresEnvironment asserts the bootstrap refuses to
// render without an environment (it drives the public hostname + keystore names).
func TestRenderSSOBootstrapRequiresEnvironment(t *testing.T) {
	t.Parallel()
	if _, err := RenderSSOBootstrapUserData(SSOBootstrapSpec{}); err == nil {
		t.Fatal("want error for missing environment, got nil")
	}
}

// TestRenderSSOBootstrapFaithfulPort asserts the rendered cloud-init carries the
// substance of the hand-written single-sign-on/main.tf keycloak_user_data: the
// pinned Keycloak/JDK, the boot-fetch of the provider/theme/realm bundle, the
// local HTTPS keystore, keycloak.conf with KC_CACHE=local, the systemd unit with
// the JIT-VPN SPI env, and the augmentation `kc.sh build` with the same features.
func TestRenderSSOBootstrapFaithfulPort(t *testing.T) {
	t.Parallel()
	ud, err := RenderSSOBootstrapUserData(SSOBootstrapSpec{Environment: "beta"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	mustContain := []string{
		"#!/bin/bash",
		"keycloak-26.1.4.zip",                // pinned Keycloak
		"OpenJDK21U",                         // pinned JDK 21
		"beta-auth.pyxcloud.io",              // <env>-auth.<domain>
		"pyx-jit-allowlist-*.jar",            // boot-fetch the JIT SPI from the bundle
		"keycloak-realm.json",                // realm import from the bundle
		"KC_CACHE=local",                     // local-cache HA (the 502 fix)
		"JIT_VPN_SG_ID=${var.jit_vpn_sg_id}", // JIT-VPN SPI env, parameterised
		"--features=token-exchange,admin-fine-grained-authz", // augmentation build
		"kc.sh start --verbose --import-realm --optimized",   // optimized start + import
		"systemctl restart keycloak",
	}
	for _, want := range mustContain {
		if !strings.Contains(ud, want) {
			t.Errorf("rendered SSO bootstrap missing %q", want)
		}
	}
}

// TestRenderSSOBootstrapInlinesNoSecretValues is the security invariant: every
// credential is referenced by Terraform variable, never embedded as a literal.
// The hand-written module's literal admin keystore password is allowed (it is
// the ALB<->instance self-signed keystore secret, not a user credential, and is
// identical to the source module); the DB/admin/vault/SMTP secrets must be vars.
func TestRenderSSOBootstrapInlinesNoSecretValues(t *testing.T) {
	t.Parallel()
	ud, err := RenderSSOBootstrapUserData(SSOBootstrapSpec{Environment: "beta"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, ref := range []string{
		"${var.kc_db_password}",
		"${var.kc_admin_password}",
		"${var.kc_vault_oidc_secret}",
		"${var.kc_smtp_password}",
		"${var.runner_public_key}",
	} {
		if !strings.Contains(ud, ref) {
			t.Errorf("expected secret to be referenced by variable %q, but it was not", ref)
		}
	}
}

// TestSSOBootstrapVariableNamesPartitioned asserts the declared variable names
// are partitioned plain vs sensitive (the credential-bearing ones are sensitive
// so the assembler/CLI can mark the emitted `variable {}` blocks accordingly).
func TestSSOBootstrapVariableNamesPartitioned(t *testing.T) {
	t.Parallel()
	plain, sensitive := SSOBootstrapSpec{Environment: "beta"}.SSOBootstrapVariableNames()
	wantSensitive := map[string]bool{
		"kc_db_password": true, "kc_admin_password": true,
		"kc_vault_oidc_secret": true, "kc_smtp_password": true,
	}
	if len(sensitive) != len(wantSensitive) {
		t.Fatalf("want %d sensitive vars, got %d (%v)", len(wantSensitive), len(sensitive), sensitive)
	}
	for _, s := range sensitive {
		if !wantSensitive[s] {
			t.Errorf("unexpected sensitive var %q", s)
		}
	}
	for _, p := range plain {
		if wantSensitive[p] {
			t.Errorf("credential var %q must be sensitive, not plain", p)
		}
	}
}

// TestPlatformBootstrapWiresSSOUserData is the integration proof: the SSO
// bootstrap, when passed through PlatformScaleGroupComponentsWithBootstrap,
// lands on the `sso` scale-group's launch-template UserData (and ONLY that
// service's — the others stay bare until their modules are ported). This is the
// canonical wiring that turns "a scale-group of 1" into "the SSO service".
func TestPlatformBootstrapWiresSSOUserData(t *testing.T) {
	t.Parallel()
	ud, err := RenderSSOBootstrapUserData(SSOBootstrapSpec{Environment: "beta"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	comps := PlatformScaleGroupComponentsWithBootstrap("", "", "", PlatformBootstraps{"sso": ud})
	bySvc := map[string]*AssembleScaleGroup{}
	for _, c := range comps {
		bySvc[c.Name] = c.ScaleGroup
	}
	if sg := bySvc["sso"]; sg == nil || !strings.Contains(sg.UserData, "keycloak-26.1.4.zip") {
		t.Fatalf("sso scale-group did not receive the Keycloak bootstrap user_data")
	}
	for _, bare := range []string{"vpn", "obs", "sast", "backend"} {
		if sg := bySvc[bare]; sg == nil || sg.UserData != "" {
			t.Errorf("service %q should be bare (no bootstrap yet), got user_data len %d", bare, len(bySvc[bare].UserData))
		}
	}
}

// TestPlatformScaleGroupComponentsBackwardCompatible asserts the original
// no-bootstrap constructor still returns 5 bare scale-groups (no behavioural
// change for existing callers / the full-estate dry-run).
func TestPlatformScaleGroupComponentsBackwardCompatible(t *testing.T) {
	t.Parallel()
	comps := PlatformScaleGroupComponents("", "", "")
	if len(comps) != 5 {
		t.Fatalf("want 5 components, got %d", len(comps))
	}
	for _, c := range comps {
		if c.ScaleGroup == nil || c.ScaleGroup.UserData != "" {
			t.Errorf("service %q should be bare via the no-bootstrap constructor", c.Name)
		}
	}
}
