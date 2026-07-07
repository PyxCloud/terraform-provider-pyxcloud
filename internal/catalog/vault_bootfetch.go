package catalog

import (
	"fmt"
	"strings"
)

// vault_bootfetch.go — EPIC-BOOTFETCH-AWS-SM-TO-VAULT.
//
// Every platform bootstrap that used to fetch its runtime secrets from AWS
// Secrets Manager at BOOT time (not render time) needs a DO-Vault equivalent:
// the droplet has no instance role, so it authenticates to Vault with an
// AppRole (role_id/secret_id injected as Terraform variables, exactly like the
// other *Var fields threaded by platform_bootstrap_sast_do.go's
// RegistryTokenVar / APITokenVar) and reads a KV-v2 leaf over HTTPS.
//
// This file is the SHARED helper: it renders the POSIX shell snippet for (a)
// an AppRole login against $VAULT_ADDR with a short retry/backoff loop and a
// fail-fast, human-readable error, and (b) a KV-v2 read of one leaf followed
// by extraction of a single key. It is deliberately provider/service-agnostic
// so obs (this PR), sast, mcp and sso can all call it with their own
// addr/role_id/secret_id Terraform variable names and their own KV path/key.
//
// Design choices:
//   - curl, not the `vault` CLI: the CLI is not guaranteed to be on a bare
//     Ubuntu droplet image and installing it is one more moving part; curl is
//     already a dependency of every existing bootstrap.
//   - python3 for JSON parsing, not jq: python3 ships on the Ubuntu droplet
//     images used across this catalog (jq is an extra apt package on some of
//     them and its quoting is fiddlier for nested `.data.data.<key>` lookups).
//   - fail-fast with a clear stderr message: a droplet that cannot reach
//     Vault at boot should say exactly why (login vs read vs missing key)
//     rather than silently writing an empty secret and limping along.
//   - the AppRole role_id/secret_id are NEVER inlined as literal values here:
//     like every other secret in this package they are `${var.<x>}`
//     placeholders, resolved by the operator at render/apply time.

// VaultBootFetchSnippet renders a POSIX shell snippet that logs into Vault via
// AppRole and reads a single key out of a KV-v2 leaf, assigning the resulting
// value to the shell variable named outVar.
//
//   - addrVar, roleIDVar, secretIDVar are Terraform variable NAMES (not
//     values) holding the Vault address (e.g. "https://staging-vault.pyxcloud.io"),
//     the AppRole role_id and secret_id respectively. Callers inject them the
//     same way platform_bootstrap_sast_do.go injects RegistryTokenVar.
//   - kvPath is the KV-v2 leaf path under the `secret` mount, WITHOUT the
//     `data/` segment (e.g. "infra/staging/observability/env"); this helper
//     adds the `data/` infix for the read.
//   - key is the field to extract from `.data.data` in the leaf (e.g. "_json"
//     or "OBS_MESH_CLIENT_SECRET").
//   - outVar is the shell variable the extracted value is assigned to (e.g.
//     "OBS_ENV_JSON"). Must be a valid shell identifier; the caller is
//     responsible for consuming it (e.g. writing it into an env file) —
//     this helper does not chmod/persist anything itself.
//
// The snippet is self-contained (it does not `set -e`/`set -u` the caller's
// shell options) but every step fails fast and prints a clear message to
// stderr before exiting 1, so it is safe to source into a `set -euo pipefail`
// bootstrap without silently masking a Vault outage.
func VaultBootFetchSnippet(addrVar, roleIDVar, secretIDVar, kvPath, key, outVar string) string {
	addrVar = strings.TrimSpace(addrVar)
	roleIDVar = strings.TrimSpace(roleIDVar)
	secretIDVar = strings.TrimSpace(secretIDVar)
	kvPath = strings.Trim(strings.TrimSpace(kvPath), "/")
	key = strings.TrimSpace(key)
	outVar = strings.TrimSpace(outVar)

	v := func(name string) string { return "${var." + name + "}" }

	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }

	w("# --- Vault AppRole boot-fetch: %s (key %s) ---", kvPath, key)
	w("VAULT_ADDR='%s'", v(addrVar))
	w("VAULT_ROLE_ID='%s'", v(roleIDVar))
	w("VAULT_SECRET_ID='%s'", v(secretIDVar))
	w("VAULT_TOKEN=\"\"")
	w("for attempt in 1 2 3 4 5; do")
	w("  VAULT_LOGIN_RESP=$(curl -kfsS -X POST \"$VAULT_ADDR/v1/auth/approle/login\" \\")
	w("    -d \"{\\\"role_id\\\":\\\"$VAULT_ROLE_ID\\\",\\\"secret_id\\\":\\\"$VAULT_SECRET_ID\\\"}\" 2>/dev/null || true)")
	w("  VAULT_TOKEN=$(printf '%%s' \"$VAULT_LOGIN_RESP\" | python3 -c 'import json,sys; print(json.load(sys.stdin).get(\"auth\",{}).get(\"client_token\",\"\"))' 2>/dev/null || true)")
	w("  if [ -n \"$VAULT_TOKEN\" ]; then break; fi")
	w("  echo \"vault-bootfetch: approle login failed (attempt $attempt/5); retrying...\" >&2")
	w("  sleep $((attempt*2))")
	w("done")
	w("if [ -z \"$VAULT_TOKEN\" ]; then")
	w("  echo \"vault-bootfetch: FATAL could not obtain a Vault token via AppRole login at $VAULT_ADDR (role_id set: $( [ -n \\\"$VAULT_ROLE_ID\\\" ] && echo yes || echo no )). Aborting.\" >&2")
	w("  exit 1")
	w("fi")
	w("")
	w("VAULT_READ_RESP=\"\"")
	w("for attempt in 1 2 3 4 5; do")
	w("  VAULT_READ_RESP=$(curl -kfsS -H \"X-Vault-Token: $VAULT_TOKEN\" \"$VAULT_ADDR/v1/secret/data/%s\" 2>/dev/null || true)", kvPath)
	w("  if [ -n \"$VAULT_READ_RESP\" ]; then break; fi")
	w("  echo \"vault-bootfetch: read of secret/data/%s failed (attempt $attempt/5); retrying...\" >&2", kvPath)
	w("  sleep $((attempt*2))")
	w("done")
	w("if [ -z \"$VAULT_READ_RESP\" ]; then")
	w("  echo \"vault-bootfetch: FATAL empty response reading secret/data/%s from $VAULT_ADDR. Aborting.\" >&2", kvPath)
	w("  exit 1")
	w("fi")
	w("")
	w("%s=$(printf '%%s' \"$VAULT_READ_RESP\" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get(\"data\",{}).get(\"data\",{}).get(\"%s\",\"\"))' 2>/dev/null || true)", outVar, key)
	w("if [ -z \"$%s\" ]; then", outVar)
	w("  echo \"vault-bootfetch: FATAL key '%s' not found (or empty) at secret/data/%s. Aborting.\" >&2", key, kvPath)
	w("  exit 1")
	w("fi")
	w("unset VAULT_TOKEN VAULT_SECRET_ID VAULT_LOGIN_RESP VAULT_READ_RESP")
	w("# --- end Vault boot-fetch (%s) ---", kvPath)

	return b.String()
}
