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
// credential by Terraform variable, never as a literal.
func TestRenderMcpDOInlinesNoSecretValues(t *testing.T) {
	t.Parallel()
	ud, err := RenderMcpDOUserData(McpDOBootstrapSpec{Environment: "beta"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, ref := range []string{
		"${var.do_spaces_key}",
		"${var.do_spaces_secret}",
		"${var.do_main_db_url}",
		"${var.mcp_embed_token_secret}",
	} {
		if !strings.Contains(ud, ref) {
			t.Errorf("expected secret to be referenced by variable %q, but it was not", ref)
		}
	}
}
