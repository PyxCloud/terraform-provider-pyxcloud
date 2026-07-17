package catalog

import (
	"context"
	"strings"
	"testing"
)

func testDOBaselineSecrets() DOBaselineSecrets {
	return DOBaselineSecrets{
		SpacesAccessKey:  "TEST_SPACES_ACCESS",
		SpacesSecretKey:  "TEST_SPACES_SECRET",
		BoardDatabaseURL: "postgres://mesh_app:TESTPW@pyx-main-db-do-user-1-0.k.db.ondigitalocean.com:25060/postgres?sslmode=require",
		EmbedTokenSecret: "TEST_EMBED_TOKEN",
	}
}

func renderTestBaseline(t *testing.T, opts DOBaselineOptions) []string {
	t.Helper()
	in := DOBaselineInput("Frankfurt", "x86_64", "ubuntu", "1.30")
	docs, err := AssembleDOBaseline(context.Background(), MustEmbedded(), in, testDOBaselineSecrets(), opts)
	if err != nil {
		t.Fatalf("AssembleDOBaseline: %v", err)
	}
	return docs
}

// TestDOBaselineResourceSet asserts the exact set of resources matches the
// deployed estate (droplet-autoscale shape, not DOKS) plus the Spaces bucket.
func TestDOBaselineResourceSet(t *testing.T) {
	joined := strings.Join(renderTestBaseline(t, DOBaselineOptions{PrivateDBHost: true}), "\n")
	want := []string{
		`resource "digitalocean_vpc" "passo-do-baseline-net"`,
		`resource "digitalocean_firewall" "passo-do-baseline-sg"`,
		`resource "digitalocean_database_cluster" "pyx-main-db"`,
		`resource "digitalocean_database_cluster" "keycloak-db"`,
		`resource "digitalocean_droplet_autoscale" "backend"`,
		`resource "digitalocean_droplet_autoscale" "mcp"`,
		`resource "digitalocean_droplet_autoscale" "obs"`,
		`resource "digitalocean_droplet_autoscale" "sast"`,
		`resource "digitalocean_droplet_autoscale" "sso"`,
		`resource "digitalocean_droplet_autoscale" "vpn"`,
		`resource "digitalocean_loadbalancer" "lb-sso"`,
		`resource "digitalocean_loadbalancer" "lb-backend"`,
		`resource "digitalocean_loadbalancer" "lb-mcp"`,
		`resource "digitalocean_spaces_bucket" "artifacts"`,
	}
	for _, w := range want {
		if !strings.Contains(joined, w) {
			t.Errorf("missing resource %q", w)
		}
	}
	// Must NOT emit the DOKS shape (which would destroy the live estate).
	if strings.Contains(joined, "digitalocean_kubernetes_cluster") {
		t.Errorf("baseline must not emit DOKS clusters (would destroy the live droplet estate)")
	}
	// The mcp size must match state (2vCPU/4GiB) and sso too.
	if !strings.Contains(joined, `s-2vcpu-4gb`) {
		t.Errorf("expected s-2vcpu-4gb droplet size")
	}
}

// TestDOBaselineMCPDurable is the durability contract: BOARD_DATABASE_URL is the
// mesh_app URL, injected at render time, and doadmin/defaultdb are gone.
func TestDOBaselineMCPDurable(t *testing.T) {
	joined := strings.Join(renderTestBaseline(t, DOBaselineOptions{PrivateDBHost: true}), "\n")
	if !strings.Contains(joined, "BOARD_DATABASE_URL=postgres://mesh_app:") {
		t.Errorf("mcp user_data must source BOARD_DATABASE_URL from mesh_app")
	}
	// No doadmin/defaultdb in the actual DB URL (a comment mentioning them is fine,
	// but the credential URL must not use them).
	if strings.Contains(joined, "BOARD_DATABASE_URL=postgres://doadmin") {
		t.Errorf("mcp BOARD_DATABASE_URL must not use doadmin")
	}
	if strings.Contains(joined, "mesh_app:TESTPW@pyx-main-db-do-user") && !strings.Contains(joined, "private-pyx-main-db-do-user") {
		t.Errorf("PrivateDBHost should rewrite the DB host to the private endpoint")
	}
	// Durable substrate preserved.
	for _, want := range []string{"PYXCLOUD_MCP_HTTP_PORT=8787", "passobuild-mcp.service", "fra1.digitaloceanspaces.com", "EMBED_TOKEN_SECRET=TEST_EMBED_TOKEN"} {
		if !strings.Contains(joined, want) {
			t.Errorf("mcp user_data lost durable element %q", want)
		}
	}
	// Only mcp carries a bootstrap; other groups have no user_data heredoc.
	if n := strings.Count(joined, "USERDATA"); n != 2 { // one open + one close marker
		t.Errorf("expected exactly one user_data heredoc (mcp only), got %d USERDATA markers", n)
	}
}

// TestDOBaselineEdgeTLSOrigins asserts the F4-prep option adds an nginx :443
// terminator to each Cloudflare-routed origin (sso/backend/mcp) with the correct
// hostname and upstream port, and leaves the base estate untouched when off.
func TestDOBaselineEdgeTLSOrigins(t *testing.T) {
	off := strings.Join(renderTestBaseline(t, DOBaselineOptions{PrivateDBHost: true}), "\n")
	if strings.Contains(off, "listen 443 ssl") {
		t.Errorf("EdgeTLSOrigins off must not emit any :443 terminator")
	}
	on := strings.Join(renderTestBaseline(t, DOBaselineOptions{PrivateDBHost: true, EdgeTLSOrigins: true}), "\n")
	// One terminator per origin (sso, backend, mcp) = 3 `listen 443 ssl`.
	if n := strings.Count(on, "listen 443 ssl"); n != 3 {
		t.Errorf("expected 3 :443 terminators (sso/backend/mcp), got %d", n)
	}
	wantPairs := []struct{ host, port string }{
		{"beta-auth.pyxcloud.io", "proxy_pass http://127.0.0.1:8080"},
		{"beta-api.pyxcloud.io", "proxy_pass http://127.0.0.1:8080"},
		{"mcp.passo.build", "proxy_pass http://127.0.0.1:8787"},
	}
	for _, p := range wantPairs {
		if !strings.Contains(on, "server_name "+p.host+";") {
			t.Errorf("edge origin missing server_name %q", p.host)
		}
		if !strings.Contains(on, p.port) {
			t.Errorf("edge origin missing upstream %q", p.port)
		}
	}
	// X-Forwarded-Proto must be https so Keycloak/Quarkus issue correct absolute URLs.
	if !strings.Contains(on, "proxy_set_header X-Forwarded-Proto https;") {
		t.Errorf("edge terminator must set X-Forwarded-Proto https")
	}
	// mcp keeps its durable bootstrap AND gets the terminator appended.
	if !strings.Contains(on, "PYXCLOUD_MCP_HTTP_PORT=8787") {
		t.Errorf("mcp durable bootstrap must survive terminator append")
	}
}

// TestEdgeTLSTerminatorValidation covers the helper's input guards.
func TestEdgeTLSTerminatorValidation(t *testing.T) {
	if _, err := RenderEdgeTLSTerminatorSnippet(EdgeTLSTerminator{Hostname: "", UpstreamPort: 8080}); err == nil {
		t.Errorf("expected error for empty hostname")
	}
	if _, err := RenderEdgeTLSTerminatorSnippet(EdgeTLSTerminator{Hostname: "x", UpstreamPort: 0}); err == nil {
		t.Errorf("expected error for invalid port")
	}
	s, err := RenderEdgeTLSTerminatorSnippet(EdgeTLSTerminator{Hostname: "beta-auth.pyxcloud.io", UpstreamPort: 8080})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	for _, want := range []string{"listen 443 ssl", "server_name beta-auth.pyxcloud.io;", "proxy_pass http://127.0.0.1:8080", "nginx -t"} {
		if !strings.Contains(s, want) {
			t.Errorf("snippet missing %q", want)
		}
	}
}

// TestDOBaselineDeterministic asserts render output is byte-stable.
func TestDOBaselineDeterministic(t *testing.T) {
	a := strings.Join(renderTestBaseline(t, DOBaselineOptions{PrivateDBHost: true}), "\n")
	b := strings.Join(renderTestBaseline(t, DOBaselineOptions{PrivateDBHost: true}), "\n")
	if a != b {
		t.Errorf("render is not deterministic")
	}
}

// TestDOBaselinePrivateHostVerbatim asserts the public host is used verbatim when
// PrivateDBHost is off.
func TestDOBaselinePrivateHostVerbatim(t *testing.T) {
	joined := strings.Join(renderTestBaseline(t, DOBaselineOptions{PrivateDBHost: false}), "\n")
	if strings.Contains(joined, "private-pyx-main-db-do-user") {
		t.Errorf("without PrivateDBHost the URL host must be verbatim (public)")
	}
	if !strings.Contains(joined, "mesh_app:TESTPW@pyx-main-db-do-user") {
		t.Errorf("expected verbatim mesh_app public host")
	}
}

// TestDOBaselineRequiresSecrets asserts missing secrets are rejected.
func TestDOBaselineRequiresSecrets(t *testing.T) {
	in := DOBaselineInput("Frankfurt", "x86_64", "ubuntu", "1.30")
	_, err := AssembleDOBaseline(context.Background(), MustEmbedded(), in, DOBaselineSecrets{}, DOBaselineOptions{})
	if err == nil {
		t.Fatalf("expected error for empty secrets")
	}
}
