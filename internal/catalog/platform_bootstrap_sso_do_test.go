package catalog

import (
	"context"
	"strings"
	"testing"
)

// validSSODOSpec is a fully-specified spec (the two unmigrated secrets present;
// every Vault-sourced field takes its default KV path) — the shape the CI/render
// step produces after EPIC-BOOTFETCH-AWS-SM-TO-VAULT wave 2.
func validSSODOSpec() SSODOBootstrapSpec {
	return SSODOBootstrapSpec{
		Environment:     "beta",
		VaultOIDCSecret: "vault-oidc-secret",
		RunnerPublicKey: "ssh-ed25519 AAAA runner",
		SMTPKVPath:      "infra/staging/sso/smtp",
	}
}

// TestRenderSSODOBootstrapRequiresInjectedSecrets asserts the render refuses to
// proceed when the one remaining literal-injected secret (VaultOIDCSecret, no
// Vault leaf provisioned for it) is missing — on DO there is no on-droplet
// fallback for it either.
func TestRenderSSODOBootstrapRequiresInjectedSecrets(t *testing.T) {
	t.Parallel()
	if _, err := RenderSSODOBootstrapUserData(SSODOBootstrapSpec{Environment: "beta"}); err == nil {
		t.Fatal("want error when VaultOIDCSecret is not injected, got nil")
	}
	if _, err := RenderSSODOBootstrapUserData(SSODOBootstrapSpec{}); err == nil {
		t.Fatal("want error for missing environment, got nil")
	}
}

// TestRenderSSODOBootstrapFaithfulPort asserts the DO bootstrap carries the SSO
// substance AND the DO-specific differences: the DO Spaces boot-fetch of the
// pinned bundle, the Vault-sourced keycloak-db URL, KC_VAULT=file, the
// --optimized --import-realm start, and NO JIT-VPN SPI env.
func TestRenderSSODOBootstrapFaithfulPort(t *testing.T) {
	t.Parallel()
	ud, err := RenderSSODOBootstrapUserData(validSSODOSpec())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	mustContain := []string{
		"#!/bin/bash",
		"keycloak-26.1.4.zip",     // pinned Keycloak (unchanged github URL)
		"OpenJDK21U",              // pinned JDK 21
		"beta-auth.pyxcloud.io",   // <env>-auth.<domain>
		"pyx-jit-allowlist-*.jar", // still boot-fetched from the bundle
		"keycloak-realm.json",     // realm import from the bundle
		// DO Spaces boot-fetch of the pinned bundle with endpoint override.
		"s3://pyx-artifacts-fra1/sso/sso-bundle-80f79e3550.tgz",
		"--endpoint-url \"https://fra1.digitaloceanspaces.com\"",
		// keycloak-db (DO Managed PG), Vault-sourced jdbc URL with sslmode=require.
		`KC_DB_URL=${data.vault_kv_secret_v2.infra_staging_sso_keycloak_db_url.data["url"]}`,
		"KC_VAULT=file",
		"kc.sh start --verbose --import-realm --optimized",
		"--features=token-exchange,admin-fine-grained-authz",
	}
	for _, want := range mustContain {
		if !strings.Contains(ud, want) {
			t.Errorf("rendered DO SSO bootstrap missing %q", want)
		}
	}
	// JIT-VPN SPI env must NOT be set on DO (the SPI no-ops there).
	for _, absent := range []string{"JIT_VPN_SG_ID", "JIT_DDB_TABLE", "JIT_TARGET_REALM"} {
		if strings.Contains(ud, absent) {
			t.Errorf("DO SSO bootstrap must not set %q (JIT SPI no-ops on DO)", absent)
		}
	}
}

// TestSSODOUserDataVaultDataSourceRefs asserts the migrated secrets (keycloak-db
// URL/creds, admin password, Spaces keys, SMTP) are referenced as Vault data
// sources — never as ${var.x} or a literal value baked in by this function —
// while the two unmigrated secrets (VaultOIDCSecret, RunnerPublicKey) are still
// injected literally (there is no instance role on DO to fetch them, and no
// Vault leaf exists for them in this wave).
func TestSSODOUserDataVaultDataSourceRefs(t *testing.T) {
	t.Parallel()
	spec := validSSODOSpec()
	ud, err := RenderSSODOBootstrapUserData(spec)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		`data.vault_kv_secret_v2.infra_staging_sso_keycloak_db_url.data["url"]`,
		`data.vault_kv_secret_v2.infra_staging_sso_keycloak_db.data["username"]`,
		`data.vault_kv_secret_v2.infra_staging_sso_keycloak_db.data["password"]`,
		`data.vault_kv_secret_v2.infra_staging_sso_keycloak_admin.data["password"]`,
		`data.vault_kv_secret_v2.infra_staging_do_spaces_keys.data["access_key"]`,
		`data.vault_kv_secret_v2.infra_staging_do_spaces_keys.data["secret_key"]`,
		`data.vault_kv_secret_v2.infra_staging_sso_smtp.data["username"]`,
		`data.vault_kv_secret_v2.infra_staging_sso_smtp.data["password"]`,
	} {
		if !strings.Contains(ud, want) {
			t.Errorf("expected Vault data-source reference %q not found", want)
		}
	}
	// The two unmigrated secrets are still injected literally.
	for _, want := range []string{spec.VaultOIDCSecret} {
		if !strings.Contains(ud, want) {
			t.Errorf("unmigrated injected secret %q not baked into DO user_data", want)
		}
	}
}

// TestSSODOVaultDataSources asserts SSODOVaultDataSources declares the KV-v2
// leaves the migrated secrets need (4 unconditionally + smtp when set).
func TestSSODOVaultDataSources(t *testing.T) {
	t.Parallel()
	docs := validSSODOSpec().SSODOVaultDataSources()
	if len(docs) != 5 {
		t.Fatalf("expected 5 vault data sources (kcdb-url, kcdb, admin, spaces, smtp), got %d: %v", len(docs), docs)
	}
	joined := strings.Join(docs, "\n")
	for _, want := range []string{
		`name  = "infra/staging/sso/keycloak-db-url"`,
		`name  = "infra/staging/sso/keycloak-db"`,
		`name  = "infra/staging/sso/keycloak-admin"`,
		`name  = "infra/staging/do/spaces-keys"`,
		`name  = "infra/staging/sso/smtp"`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	// Without an SMTP KV path, only 4 leaves are needed.
	noSMTP := SSODOBootstrapSpec{Environment: "beta"}.SSODOVaultDataSources()
	if len(noSMTP) != 4 {
		t.Fatalf("expected 4 vault data sources with no SMTP path, got %d: %v", len(noSMTP), noSMTP)
	}
}

// TestWithSSODOUserDataWiresOnlySSO is the wiring proof: the DO bootstrap lands on
// the `sso` scale-group's UserDataByProvider["digitalocean"] and NOTHING else —
// the generic UserData and every other service are untouched.
func TestWithSSODOUserDataWiresOnlySSO(t *testing.T) {
	t.Parallel()
	ud, err := RenderSSODOBootstrapUserData(validSSODOSpec())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	comps := WithSSODOUserData(PlatformScaleGroupComponents("", "", ""), ud)
	bySvc := map[string]*AssembleScaleGroup{}
	for _, c := range comps {
		bySvc[c.Name] = c.ScaleGroup
	}
	sso := bySvc["sso"]
	if sso == nil {
		t.Fatal("no sso scale-group")
	}
	got := sso.UserDataByProvider[ProviderDigitalOcean]
	if !strings.Contains(got, "s3://pyx-artifacts-fra1/sso/sso-bundle-80f79e3550.tgz") ||
		!strings.Contains(got, "KC_DB_URL=${data.vault_kv_secret_v2.") {
		t.Fatalf("sso DO user_data missing the bundle or keycloak-db reference")
	}
	// The generic (AWS/default) UserData must stay empty on the bare constructor.
	if sso.UserData != "" {
		t.Errorf("generic UserData must be untouched, got len %d", len(sso.UserData))
	}
	// No other service gets a DO override.
	for _, other := range []string{"vpn", "obs", "sast", "backend", "mcp"} {
		if sg := bySvc[other]; sg != nil && sg.UserDataByProvider != nil {
			if _, ok := sg.UserDataByProvider[ProviderDigitalOcean]; ok {
				t.Errorf("service %q must not receive a DO user_data override", other)
			}
		}
	}
}

// TestSSODOUserDataRendersOnDigitalOcean is the end-to-end assembler proof: with
// the sso DO override wired in, an actual DigitalOcean render of the sso
// scale-group resolves the DO user_data (referencing keycloak-db + the bundle)
// through TranslateScaleGroup — i.e. UserDataByProvider["digitalocean"] wins.
func TestSSODOUserDataRendersOnDigitalOcean(t *testing.T) {
	t.Parallel()
	ud, err := RenderSSODOBootstrapUserData(validSSODOSpec())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	comps := WithSSODOUserData(PlatformScaleGroupComponents("", "", ""), ud)
	var ssoSG *AssembleScaleGroup
	for _, c := range comps {
		if c.Name == "sso" {
			ssoSG = c.ScaleGroup
		}
	}
	if ssoSG == nil {
		t.Fatal("no sso scale-group")
	}
	plan, err := TranslateScaleGroup(context.Background(), MustEmbedded(), ScaleGroupSpec{
		Name: "sso", Region: "Frankfurt", Provider: ProviderDigitalOcean,
		CPU: 2, RAM: 4, Min: 1, Max: 1, Desired: 1, Health: HealthELB,
		UserData: ssoSG.UserData, UserDataByProvider: ssoSG.UserDataByProvider,
	})
	if err != nil {
		t.Fatalf("translate sso on DO: %v", err)
	}
	if !strings.Contains(plan.UserData, "s3://pyx-artifacts-fra1/sso/sso-bundle-80f79e3550.tgz") ||
		!strings.Contains(plan.UserData, "KC_DB_URL=${data.vault_kv_secret_v2.") {
		t.Fatalf("DO scale-group plan did not resolve the sso DO user_data (keycloak-db + bundle)")
	}
}
