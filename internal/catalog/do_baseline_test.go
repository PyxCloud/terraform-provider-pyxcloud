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
		`resource "digitalocean_loadbalancer" "edge-lb"`,
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
