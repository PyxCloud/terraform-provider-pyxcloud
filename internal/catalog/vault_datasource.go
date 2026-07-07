package catalog

import (
	"fmt"
	"regexp"
	"strings"
)

// vault_datasource.go — EPIC-BOOTFETCH-AWS-SM-TO-VAULT (wave 2: sast/mcp/sso).
//
// vault_bootfetch.go's VaultBootFetchSnippet covers secrets a droplet fetches
// ITSELF at boot time via an AppRole login (the obs pattern: the value can
// rotate in Vault and a service restart picks it up with no redeploy).
//
// Not every remaining AWS Secrets Manager read fits that shape. Some secrets
// are consumed at RENDER/APPLY time — by the operator/CI Terraform run, not by
// the droplet — because either (a) the value has to be baked into the
// user_data heredoc verbatim (the sso DO bootstrap inlines KC_DB_URL/
// KC_DB_PASSWORD/etc. into keycloak.conf; there is no boot-time AppRole login
// on that path today) or (b) the value is a provider-level credential (DO
// Spaces keys, the DO API token) referenced by every service's bash script but
// resolved once per apply, not per boot. For those, the Vault equivalent of
// "the operator ran `aws secretsmanager get-secret-value` and exported an env
// var" is a Terraform-native `data "vault_kv_secret_v2"` data source: Terraform
// itself authenticates to Vault (via the `vault` provider block, itself backed
// by an AppRole or token in the environment/CI OIDC — never a literal in this
// repo) and reads the KV-v2 leaf at plan/apply time, and the resulting
// `data.vault_kv_secret_v2.<label>.data["<key>"]` expression is interpolated
// directly into the rendered HCL (in place of the old `${var.<x>}` that a
// human used to populate with a Secrets-Manager-sourced `-var`).
//
// This file is the SHARED helper for that shape. See internal-vpn PR #30
// (https://github.com/PyxCloud/internal-vpn/pull/30) for the reference
// pattern this mirrors: mount "secret", name "infra/<env>/<path>", a `vault`
// provider block with required_providers hashicorp/vault ~> 4.0.

// VaultProviderBlock is a STANDALONE terraform{required_providers{vault=...}} +
// provider "vault" {} HCL snippet — useful for a module that has no other
// required_providers block yet. The vault provider reads VAULT_ADDR /
// VAULT_TOKEN (or VAULT_ROLE_ID+VAULT_SECRET_ID via a login helper) from the
// environment/CI — nothing is hardcoded here, matching the AppRole-injected
// pattern used everywhere else in this package.
//
// CAUTION: a Terraform module may have only ONE required_providers block
// (a second one is a hard "Duplicate required providers configuration"
// error). Every render this package produces (AssembleHCL's DO path via
// requiredProvidersBlock/needsVault, and the cutover/render.go DO-baseline
// harness) already owns exactly one such block and merges `vault` into it —
// do NOT also call this function there. Only use VaultProviderBlock directly
// when assembling a module that has no other terraform{} block at all.
func VaultProviderBlock() string {
	return `terraform {
  required_providers {
    vault = {
      source  = "hashicorp/vault"
      version = "~> 4.0"
    }
  }
}

# VAULT_ADDR / VAULT_TOKEN (or VAULT_ROLE_ID + VAULT_SECRET_ID via the CI
# OIDC/AppRole login step) come from the environment — never hardcoded here.
provider "vault" {}
`
}

// vaultLabelSanitizer converts a KV path into a safe HCL data-source label
// (letters, digits, underscore only).
var vaultLabelSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_]+`)

// VaultKVDataSourceLabel derives a deterministic, valid HCL identifier for the
// `data "vault_kv_secret_v2" "<label>"` block from a KV path, e.g.
// "infra/staging/do/spaces-keys" -> "infra_staging_do_spaces_keys".
func VaultKVDataSourceLabel(kvPath string) string {
	trimmed := strings.Trim(strings.TrimSpace(kvPath), "/")
	label := vaultLabelSanitizer.ReplaceAllString(trimmed, "_")
	label = strings.Trim(label, "_")
	if label == "" {
		label = "secret"
	}
	return label
}

// VaultKVDataSourceHCL renders a `data "vault_kv_secret_v2"` block reading the
// KV-v2 leaf kvPath (under the `secret` mount, WITHOUT the `data/` infix — the
// vault_kv_secret_v2 data source itself adds the KV-v2 data/ indirection) and
// returns the rendered HCL doc alongside the data-source label (so the caller
// can build .data["key"] reference expressions with VaultKVRef).
func VaultKVDataSourceHCL(kvPath string) (doc string, label string) {
	kvPath = strings.Trim(strings.TrimSpace(kvPath), "/")
	label = VaultKVDataSourceLabel(kvPath)
	doc = fmt.Sprintf(`data "vault_kv_secret_v2" %q {
  mount = "secret"
  name  = %q
}`, label, kvPath)
	return doc, label
}

// VaultKVRef returns the terraform interpolation expression
// `${data.vault_kv_secret_v2.<label>.data["<key>"]}` for embedding directly
// into a rendered bash heredoc (the render-time-injected-value pattern the sso
// DO bootstrap already uses for its literal secrets, and the pattern sast's
// provider-level credentials now use in place of a human-populated ${var.x}).
func VaultKVRef(label, key string) string {
	return fmt.Sprintf("${data.vault_kv_secret_v2.%s.data[%q]}", label, key)
}

// KnownVaultKVPaths is the closed set of KV-v2 leaf paths any DO platform-
// service bootstrap in this catalog may reference via a `data
// "vault_kv_secret_v2"` block (EPIC-BOOTFETCH-AWS-SM-TO-VAULT). Kept as one
// deterministic list so a generic assembler (AssembleHCL in assemble.go) can
// detect which leaves a rendered user_data blob actually references — by
// checking each candidate's derived label, since VaultKVDataSourceLabel is a
// lossy (non-reversible) sanitization of the path — and emit exactly the
// `data` blocks needed, without hand-wiring per-service knowledge into the
// generic assembler. Add a new leaf here whenever a bootstrap starts
// referencing one.
func KnownVaultKVPaths() []string {
	return []string{
		"infra/staging/do/spaces-keys",
		"infra/staging/do/api-token",
		"infra/staging/mcp",
		"infra/staging/sso/keycloak-db-url",
		"infra/staging/sso/keycloak-admin",
		"infra/staging/sso/keycloak-db",
		"infra/staging/sso/idp-oauth",
		"infra/staging/sso/smtp",
		"infra/staging/sso/runner-ssh-key",
	}
}

// vaultKVLabelsReferenced scans blob for `data.vault_kv_secret_v2.<label>.` use
// and returns, in KnownVaultKVPaths order, the subset of known KV paths whose
// derived label is actually referenced.
func vaultKVLabelsReferenced(blob string) []string {
	var out []string
	for _, path := range KnownVaultKVPaths() {
		label := VaultKVDataSourceLabel(path)
		if strings.Contains(blob, "data.vault_kv_secret_v2."+label+".") {
			out = append(out, path)
		}
	}
	return out
}
