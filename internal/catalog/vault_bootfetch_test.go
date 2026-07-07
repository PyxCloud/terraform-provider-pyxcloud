package catalog

import (
	"strings"
	"testing"
)

// TestVaultBootFetchSnippetShape asserts the shared boot-fetch helper renders
// an AppRole login against the injected Terraform variables (never a literal
// role_id/secret_id/address), a KV-v2 read of the given leaf, and extraction of
// the requested key into the caller-chosen shell variable.
func TestVaultBootFetchSnippetShape(t *testing.T) {
	t.Parallel()
	snip := VaultBootFetchSnippet("vault_addr", "obs_vault_role_id", "obs_vault_secret_id", "infra/staging/observability/env", "_json", "OBS_ENV_JSON")

	mustContain := []string{
		// Secrets referenced by Terraform variable, never inlined.
		"${var.vault_addr}",
		"${var.obs_vault_role_id}",
		"${var.obs_vault_secret_id}",
		// AppRole login.
		"auth/approle/login",
		"role_id",
		"secret_id",
		// KV-v2 read (data/ infix, no leading/trailing slash issues).
		"/v1/secret/data/infra/staging/observability/env",
		// python3 JSON parsing, not jq.
		"python3",
		"json.load",
		// Output variable assignment + the requested key.
		"OBS_ENV_JSON=",
		"\"_json\"",
		// Fail-fast behavior.
		"exit 1",
	}
	for _, want := range mustContain {
		if !strings.Contains(snip, want) {
			t.Errorf("vault boot-fetch snippet missing %q:\n%s", want, snip)
		}
	}
	if strings.Contains(snip, "jq ") {
		t.Errorf("vault boot-fetch snippet must use python3, not jq:\n%s", snip)
	}
}

// TestVaultBootFetchSnippetNoHardcodedCreds is the security invariant: nothing
// that looks like a literal role_id/secret_id/token ever appears — only
// ${var.<x>} placeholders and shell variable names.
func TestVaultBootFetchSnippetNoHardcodedCreds(t *testing.T) {
	t.Parallel()
	snip := VaultBootFetchSnippet("addr_var", "role_id_var", "secret_id_var", "infra/staging/sast/env", "SAST_TOKEN", "SAST_TOKEN_OUT")
	for _, forbidden := range []string{"hvs.", "hvb."} {
		if strings.Contains(snip, forbidden) {
			t.Errorf("vault boot-fetch snippet must never embed a literal Vault token prefix %q:\n%s", forbidden, snip)
		}
	}
	if !strings.Contains(snip, "${var.addr_var}") || !strings.Contains(snip, "${var.role_id_var}") || !strings.Contains(snip, "${var.secret_id_var}") {
		t.Errorf("vault boot-fetch snippet must reference all three caller-supplied variable names:\n%s", snip)
	}
}

// TestVaultBootFetchSnippetReusableForDifferentLeaves proves the helper is
// generic across services (obs today; sast/mcp/sso next) by rendering two
// different KV paths/keys and checking each snippet only contains its own
// path, not the other's — i.e. no leaked cross-service state.
func TestVaultBootFetchSnippetReusableForDifferentLeaves(t *testing.T) {
	t.Parallel()
	obs := VaultBootFetchSnippet("vault_addr", "obs_role", "obs_secret", "infra/staging/observability/env", "_json", "OBS_JSON")
	sast := VaultBootFetchSnippet("vault_addr", "sast_role", "sast_secret", "infra/staging/sast/env", "_json", "SAST_JSON")

	if !strings.Contains(obs, "infra/staging/observability/env") || strings.Contains(obs, "infra/staging/sast/env") {
		t.Errorf("obs snippet leaf path wrong/leaked:\n%s", obs)
	}
	if !strings.Contains(sast, "infra/staging/sast/env") || strings.Contains(sast, "infra/staging/observability/env") {
		t.Errorf("sast snippet leaf path wrong/leaked:\n%s", sast)
	}
}
