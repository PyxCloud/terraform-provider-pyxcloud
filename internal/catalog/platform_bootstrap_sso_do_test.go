package catalog

import (
	"context"
	"strings"
	"testing"
)

// validSSODOSpec is a fully-injected spec (every credential present) — the shape
// the CI/render step produces after resolving Secrets Manager.
func validSSODOSpec() SSODOBootstrapSpec {
	return SSODOBootstrapSpec{
		Environment:     "beta",
		KCDBURL:         "jdbc:postgresql://kc-do-do-user.b.db.ondigitalocean.com:25060/defaultdb?sslmode=require",
		KCDBUsername:    "kcuser",
		KCDBPassword:    "kc-db-pw",
		AdminPassword:   "admin-pw",
		VaultOIDCSecret: "vault-oidc-secret",
		SpacesAccessKey: "SPACESKEY",
		SpacesSecretKey: "spaces-secret",
		RunnerPublicKey: "ssh-ed25519 AAAA runner",
		SMTPUser:        "ses-smtp-user",
		SMTPPassword:    "ses-smtp-pw",
	}
}

// TestRenderSSODOBootstrapRequiresInjectedSecrets asserts the render refuses to
// proceed when any secret is missing — on DO there is no on-droplet fallback.
func TestRenderSSODOBootstrapRequiresInjectedSecrets(t *testing.T) {
	t.Parallel()
	if _, err := RenderSSODOBootstrapUserData(SSODOBootstrapSpec{Environment: "beta"}); err == nil {
		t.Fatal("want error when secrets are not injected, got nil")
	}
	if _, err := RenderSSODOBootstrapUserData(SSODOBootstrapSpec{}); err == nil {
		t.Fatal("want error for missing environment, got nil")
	}
}

// TestRenderSSODOBootstrapFaithfulPort asserts the DO bootstrap carries the SSO
// substance AND the DO-specific differences: the DO Spaces boot-fetch of the
// pinned bundle, the injected keycloak-db URL, KC_VAULT=file, the --optimized
// --import-realm start, and NO JIT-VPN SPI env.
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
		// keycloak-db (DO Managed PG), injected jdbc URL with sslmode=require.
		"KC_DB_URL=jdbc:postgresql://",
		"sslmode=require",
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

// TestSSODOUserDataInjectsSecretValues asserts the injected secrets are actually
// baked into the rendered user_data (there is no instance role on DO to fetch
// them), including the Spaces keys and the DB/admin/vault creds.
func TestSSODOUserDataInjectsSecretValues(t *testing.T) {
	t.Parallel()
	ud, err := RenderSSODOBootstrapUserData(validSSODOSpec())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"SPACESKEY", "spaces-secret", // DO Spaces keys (beta-DigitalOceanSpacesKeys)
		"kc-db-pw", "admin-pw", "vault-oidc-secret",
	} {
		if !strings.Contains(ud, want) {
			t.Errorf("injected secret %q not baked into DO user_data", want)
		}
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
		!strings.Contains(got, "KC_DB_URL=jdbc:postgresql://") {
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
		!strings.Contains(plan.UserData, "KC_DB_URL=jdbc:postgresql://") {
		t.Fatalf("DO scale-group plan did not resolve the sso DO user_data (keycloak-db + bundle)")
	}
}
