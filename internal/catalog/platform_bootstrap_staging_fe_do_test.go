package catalog

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenderStagingFEDOBootstrapNoNextBuild is the core assertion for
// pd-STAGING-FE-SHIM: the shim is nginx ONLY — there is no `next build`/
// `npm install`/`node` anywhere in the bootstrap, which is precisely the OOM
// risk this shim exists to avoid (it must never repeat the fragile obs-box
// shim's behaviour).
func TestRenderStagingFEDOBootstrapNoNextBuild(t *testing.T) {
	t.Parallel()
	ud, err := RenderStagingFEDOBootstrapUserData(StagingFEDOBootstrapSpec{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	forbidden := []string{"next build", "next start", "npm install", "npm ci", "yarn install", "node_modules"}
	for _, bad := range forbidden {
		if strings.Contains(ud, bad) {
			t.Errorf("staging-fe bootstrap must NOT contain %q (nginx-only, no Next.js build):\n%s", bad, ud)
		}
	}
	mustContain := []string{
		"#!/usr/bin/env bash",
		"apt-get install -y nginx openssl curl python3",
		"listen 443 ssl;",
		"nginx -t",
		"systemctl enable nginx",
		"systemctl restart nginx",
	}
	for _, want := range mustContain {
		if !strings.Contains(ud, want) {
			t.Errorf("staging-fe bootstrap missing %q:\n%s", want, ud)
		}
	}
}

// TestRenderStagingFEDOBootstrapProxiesToAmplify asserts the nginx config
// proxies to the Amplify staging branch with the correct SNI/Host, injects the
// Authorization header from the boot-fetched Vault secret, and rewrites
// redirects/cookies back to the public hostname.
func TestRenderStagingFEDOBootstrapProxiesToAmplify(t *testing.T) {
	t.Parallel()
	ud, err := RenderStagingFEDOBootstrapUserData(StagingFEDOBootstrapSpec{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	mustContain := []string{
		"https://staging.de9vejckwo4b9.amplifyapp.com", // Amplify staging branch upstream
		"proxy_ssl_server_name on;",                    // SNI to the upstream, not passthrough
		"proxy_ssl_name staging.de9vejckwo4b9.amplifyapp.com;",
		"proxy_set_header Host staging.de9vejckwo4b9.amplifyapp.com;",
		`proxy_set_header Authorization "Basic $AMPLIFY_BASIC_AUTH";`, // injected credential
		"server_name staging.passo.build staging-console.passo.build;",
		"proxy_redirect https://staging.de9vejckwo4b9.amplifyapp.com/ https://staging.passo.build/;",
		"proxy_cookie_domain staging.de9vejckwo4b9.amplifyapp.com staging.passo.build;",
		"resolver 1.1.1.1 8.8.8.8 valid=60s;", // Amplify IPs aren't stable; must not be cached
	}
	for _, want := range mustContain {
		if !strings.Contains(ud, want) {
			t.Errorf("staging-fe bootstrap missing %q:\n%s", want, ud)
		}
	}
}

// TestRenderStagingFEDOBootstrapVaultBootFetchNoSecretLeak is the security
// invariant: the Amplify Basic-Auth credential is fetched at BOOT time via
// Vault AppRole (mirroring platform_bootstrap_obs_do.go's mesh-secret fetch),
// never inlined as a literal and never a render-time Terraform variable
// carrying the credential value itself (only the AppRole role_id/secret_id
// variable NAMES are referenced).
func TestRenderStagingFEDOBootstrapVaultBootFetchNoSecretLeak(t *testing.T) {
	t.Parallel()
	ud, err := RenderStagingFEDOBootstrapUserData(StagingFEDOBootstrapSpec{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	mustContain := []string{
		"auth/approle/login",
		"/v1/secret/data/infra/staging/staging-fe/amplify-basic-auth",
		"${var.vault_addr}",
		"${var.staging_fe_vault_role_id}",
		"${var.staging_fe_vault_secret_id}",
		"AMPLIFY_BASIC_AUTH",
		"unset AMPLIFY_BASIC_AUTH", // scrubbed from the boot shell's environment after use
	}
	for _, want := range mustContain {
		if !strings.Contains(ud, want) {
			t.Errorf("staging-fe bootstrap missing Vault boot-fetch %q:\n%s", want, ud)
		}
	}
	// No literal credential-looking assignment: the ONLY source of the header
	// value must be the Vault read, never a hardcoded "Basic <something>".
	if strings.Contains(ud, `Authorization: Basic dXN`) || strings.Contains(ud, "amplify-basic-auth=") {
		t.Errorf("staging-fe bootstrap must not inline a literal Basic-Auth credential:\n%s", ud)
	}
}

// TestRenderStagingFEDOBootstrapWatchdog asserts the systemd watchdog (defense
// in depth alongside the droplet-autoscale self-heal floor) is wired: a timer
// firing every minute that restarts nginx when it stops answering locally.
func TestRenderStagingFEDOBootstrapWatchdog(t *testing.T) {
	t.Parallel()
	ud, err := RenderStagingFEDOBootstrapUserData(StagingFEDOBootstrapSpec{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	mustContain := []string{
		"/usr/local/bin/staging-fe-watchdog",
		"staging-fe-watchdog.service",
		"staging-fe-watchdog.timer",
		"OnUnitActiveSec=1min",
		"systemctl restart nginx",
		"systemctl enable --now staging-fe-watchdog.timer",
	}
	for _, want := range mustContain {
		if !strings.Contains(ud, want) {
			t.Errorf("staging-fe bootstrap missing watchdog %q:\n%s", want, ud)
		}
	}
}

// TestRenderStagingFEDOBootstrapNoFmtMarkersAndBashParses guards against the
// exact regression class that bricked the obs droplet bootstrap on 2026-07-07
// (a fmt verb/argument mismatch leaking "%!s(MISSING)"/"%!(EXTRA ...)" markers,
// which Go's fmt does not error on but which are a bash syntax error).
func TestRenderStagingFEDOBootstrapNoFmtMarkersAndBashParses(t *testing.T) {
	t.Parallel()
	ud, err := RenderStagingFEDOBootstrapUserData(StagingFEDOBootstrapSpec{})
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

// TestStagingFEServiceWiredIntoDOBaseline asserts the staging-fe droplet is a
// distinct, self-heal-of-1 baseline service — decoupled from obs — and that
// its firewall is edge-only (source_tags = ["pyx-edge"] for 443), never
// 0.0.0.0/0, unlike the shared baseline firewall every other service uses.
func TestStagingFEServiceWiredIntoDOBaseline(t *testing.T) {
	t.Parallel()
	found := false
	for _, s := range DOBaselineServices() {
		if s.Name == "staging-fe" {
			found = true
			if s.Tag != "pyx-staging-fe" {
				t.Errorf("staging-fe tag = %q, want pyx-staging-fe", s.Tag)
			}
		}
	}
	if !found {
		t.Fatal("staging-fe not found in DOBaselineServices()")
	}

	ctx := context.Background()
	cat := MustEmbedded()
	secrets := DOBaselineSecrets{
		SpacesAccessKey:  "ak",
		SpacesSecretKey:  "sk",
		BoardDatabaseURL: "postgres://u:p@h:5432/mesh_app",
		EmbedTokenSecret: "embed",
	}
	docs, err := AssembleDOBaseline(ctx, cat, DOBaselineInput("Frankfurt", "", "", ""), secrets, DOBaselineOptions{})
	if err != nil {
		t.Fatalf("AssembleDOBaseline: %v", err)
	}
	all := strings.Join(docs, "\n")

	if !strings.Contains(all, `resource "digitalocean_droplet_autoscale" "staging-fe"`) {
		t.Fatalf("staging-fe droplet_autoscale pool not emitted:\n%s", all)
	}
	if !strings.Contains(all, `resource "digitalocean_firewall" "passo-do-baseline-staging-fe-sg"`) {
		t.Fatalf("dedicated staging-fe firewall not emitted:\n%s", all)
	}
	if !strings.Contains(all, `source_tags   = ["pyx-edge"]`) && !strings.Contains(all, `source_tags = ["pyx-edge"]`) {
		t.Fatalf("staging-fe firewall must scope :443 to source_tags = [\"pyx-edge\"]:\n%s", all)
	}

	// The shared baseline firewall(s) must NOT carry the pyx-staging-fe tag —
	// it must never share the 0.0.0.0/0:443 rule every other service gets.
	for _, doc := range docs {
		if strings.Contains(doc, `resource "digitalocean_firewall" "passo-do-baseline-sg"`) &&
			strings.Contains(doc, "pyx-staging-fe") {
			t.Errorf("shared baseline firewall must not include pyx-staging-fe:\n%s", doc)
		}
	}
}

// TestStagingFEDOBootstrapRenderedWhenFullServiceBootstraps proves the
// staging-fe bootstrap (not a bare box) reaches the rendered HCL when
// DOBaselineOptions.FullServiceBootstraps is set, exactly like its obs/sast/
// backend/vpn siblings.
func TestStagingFEDOBootstrapRenderedWhenFullServiceBootstraps(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cat := MustEmbedded()
	secrets := DOBaselineSecrets{
		SpacesAccessKey:    "ak",
		SpacesSecretKey:    "sk",
		BoardDatabaseURL:   "postgres://u:p@h:5432/mesh_app",
		EmbedTokenSecret:   "embed",
		SSOVaultOIDCSecret: "oidc-secret",
	}
	docs, err := AssembleDOBaseline(ctx, cat, DOBaselineInput("Frankfurt", "", "", ""), secrets, DOBaselineOptions{
		FullServiceBootstraps: true,
	})
	if err != nil {
		t.Fatalf("AssembleDOBaseline: %v", err)
	}
	all := strings.Join(docs, "\n")
	if !strings.Contains(all, "https://staging.de9vejckwo4b9.amplifyapp.com") {
		t.Fatalf("staging-fe full bootstrap (Amplify upstream) did not reach the rendered HCL:\n%s", all)
	}
	if !strings.Contains(all, "${var.staging_fe_vault_role_id}") {
		t.Fatalf("staging-fe Vault AppRole var ref did not reach the rendered HCL:\n%s", all)
	}
}

// TestDOBaselineVariableNamesIncludesStagingFE asserts the harness's variable
// declarations include the staging-fe Vault AppRole var names so the rendered
// estate.tf is self-contained (`terraform validate`-able) without any manual
// wiring step.
func TestDOBaselineVariableNamesIncludesStagingFE(t *testing.T) {
	t.Parallel()
	names := DOBaselineVariableNames()
	want := []string{"staging_fe_vault_role_id", "staging_fe_vault_secret_id"}
	for _, w := range want {
		hit := false
		for _, n := range names {
			if n == w {
				hit = true
				break
			}
		}
		if !hit {
			t.Errorf("DOBaselineVariableNames() missing %q, got %v", w, names)
		}
	}
}
