package catalog

import (
	"strings"
	"testing"
)

// TestRenderMcpDOListensViaHTTPPortEnv is the F2-02 mcp blocker regression: the
// mcp-go binary reads PYXCLOUD_MCP_HTTP_PORT (config.go). An empty/unset value
// puts it in STDIO mode — it starts, reads EOF from stdin under systemd and exits
// 0/SUCCESS with NO listener. A legacy PORT= line is IGNORED. The bootstrap MUST
// set PYXCLOUD_MCP_HTTP_PORT so the server enters HTTP mode and listens on :8787.
func TestRenderMcpDOListensViaHTTPPortEnv(t *testing.T) {
	t.Parallel()
	ud, err := RenderMcpDOUserData(McpDOBootstrapSpec{Environment: "beta"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(ud, "PYXCLOUD_MCP_HTTP_PORT=8787") {
		t.Error("mcp bootstrap must set PYXCLOUD_MCP_HTTP_PORT=8787 (the env var the binary actually reads) or it defaults to stdio and exits 0 with no listener")
	}
	// The legacy/ignored PORT= line must NOT be the only port config: guard against
	// a regression where someone reintroduces PORT= as the (ignored) port source.
	for _, line := range strings.Split(ud, "\n") {
		if strings.TrimSpace(line) == "PORT=8787" {
			t.Error("mcp bootstrap must NOT rely on a bare PORT=8787 line; the mcp-go binary ignores it (reads PYXCLOUD_MCP_HTTP_PORT)")
		}
	}
}

// TestRenderMcpDOInlinesNoSecretValues asserts the mcp bootstrap references every
// credential by a Vault data source or (for the not-yet-migrated board DB URL) a
// Terraform variable, never as a literal (EPIC-BOOTFETCH-AWS-SM-TO-VAULT wave 2).
func TestRenderMcpDOInlinesNoSecretValues(t *testing.T) {
	t.Parallel()
	ud, err := RenderMcpDOUserData(McpDOBootstrapSpec{Environment: "beta"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, ref := range []string{
		`${data.vault_kv_secret_v2.infra_staging_do_spaces_keys.data["access_key"]}`,
		`${data.vault_kv_secret_v2.infra_staging_do_spaces_keys.data["secret_key"]}`,
		`${data.vault_kv_secret_v2.infra_staging_mcp.data["embed_token"]}`,
		"${var.do_main_db_url}", // unmigrated: no Vault leaf yet for the board DB URL
	} {
		if !strings.Contains(ud, ref) {
			t.Errorf("expected secret to be referenced by %q, but it was not", ref)
		}
	}
	// No lingering ${var.x} secret refs from the pre-Vault era for the migrated secrets.
	for _, retired := range []string{"${var.do_spaces_key}", "${var.do_spaces_secret}", "${var.mcp_embed_token_secret}"} {
		if strings.Contains(ud, retired) {
			t.Errorf("rendered mcp DO bootstrap must not reference the retired ${var.x} secret %q", retired)
		}
	}
}

// TestMcpDOVaultDataSources asserts the bootstrap declares the two Vault KV data
// sources it needs (Spaces keys + the embed-token leaf).
func TestMcpDOVaultDataSources(t *testing.T) {
	t.Parallel()
	docs := McpDOBootstrapSpec{Environment: "beta"}.McpDOVaultDataSources()
	if len(docs) != 2 {
		t.Fatalf("expected 2 vault data sources, got %d: %v", len(docs), docs)
	}
	joined := strings.Join(docs, "\n")
	for _, want := range []string{
		`name  = "infra/staging/do/spaces-keys"`,
		`name  = "infra/staging/mcp"`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
}
