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
		`resource "digitalocean_droplet_autoscale" "sso"`,
		`resource "digitalocean_droplet_autoscale" "staging-fe"`,
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
	for _, forbidden := range []string{`resource "digitalocean_loadbalancer"`, `resource "digitalocean_certificate"`} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("private staging baseline must not emit %q", forbidden)
		}
	}
	// backend/mcp/obs/sast/vpn are gone live (obs/sast/backend/vpn purged
	// 2026-07-10; mcp confirmed gone in the 2026-07-20 live reconciliation, now
	// served by DO App Platform off this baseline) — see do_baseline.go's
	// file-header "RECONCILED AGAINST LIVE" note.
	for _, unwanted := range []string{
		`resource "digitalocean_droplet_autoscale" "backend"`,
		`resource "digitalocean_droplet_autoscale" "mcp"`,
		`resource "digitalocean_droplet_autoscale" "obs"`,
		`resource "digitalocean_droplet_autoscale" "sast"`,
		`resource "digitalocean_droplet_autoscale" "vpn"`,
	} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("live-reconciled render must not contain %q", unwanted)
		}
	}
	// No suffixed firewall chunk (only the sso tag now, one chunk; staging-fe
	// gets its own dedicated firewall, excluded from this tag list).
	if strings.Contains(joined, `"passo-do-baseline-sg-2"`) {
		t.Errorf("live-reconciled render must not emit a second firewall chunk")
	}
	// Both remaining droplets are right-sized to 1vCPU/2GiB: sso (cost/
	// staging-rightsize-v2, -$12/mo — down from the live 2vCPU/4GiB) and
	// staging-fe (matches its live droplet size, doctl 582920441 at
	// 1 VCPU / 2048MB). Match on the droplet_template size line specifically —
	// `db-s-2vcpu-4gb` (the managed PG clusters, unaffected by this PR) also
	// contains the substring "s-2vcpu-4gb", so a bare Contains check on that
	// string would pass for the wrong reason.
	if n := strings.Count(joined, `size               = "s-1vcpu-2gb"`); n != 2 {
		t.Errorf("expected 2 droplets at s-1vcpu-2gb (sso + staging-fe), got %d", n)
	}
	if strings.Contains(joined, `size               = "s-2vcpu-4gb"`) {
		t.Errorf("no droplet should still be s-2vcpu-4gb after the sso right-size")
	}
}

// TestDOBaselineStagingOriginsArePrivate is the staging network-boundary
// contract: API, auth, MCP and FE origins are reachable only through the VPC
// edge. Authentication remains an independent application-layer requirement;
// a private source must never create an auth bypass or a public origin rule.
func TestDOBaselineStagingOriginsArePrivate(t *testing.T) {
	for _, opts := range []DOBaselineOptions{
		{PrivateDBHost: true},
		{PrivateDBHost: true, LBTermination: true},
	} {
		joined := strings.Join(renderTestBaseline(t, opts), "\n")
		if strings.Contains(joined, `source_addresses = ["0.0.0.0/0", "::/0"]`) {
			t.Fatal("staging baseline must not expose TLS origins to the public internet")
		}
		if !strings.Contains(joined, `source_tags = ["pyx-edge"]`) {
			t.Fatal("staging TLS origins must only accept traffic from the VPC edge")
		}
		if strings.Contains(joined, `source_load_balancer_uids`) {
			t.Fatal("private staging origins must be selected by pyx-edge source tags, not a public load balancer")
		}
		if strings.Contains(joined, `resource "digitalocean_loadbalancer"`) || strings.Contains(joined, `resource "digitalocean_certificate"`) {
			t.Fatal("private staging baseline must not render load balancers or platform certificates")
		}
	}
}

// TestDOBaselinePrivateURLHelper covers the DOBaselineSecrets.privateURL
// rewrite logic directly. It used to be exercised end-to-end through the mcp
// durable render (TestDOBaselineMCPDurable / TestDOBaselinePrivateHostVerbatim),
// but mcp is no longer part of DOBaselineServices() (confirmed gone live in the
// 2026-07-20 reconciliation — see do_baseline.go's file-header note), so
// BoardDatabaseURL is never interpolated into any rendered user_data anymore.
// The field/method are kept (cutover/render.go still wires DO_BOARD_DATABASE_URL
// unconditionally) so this test keeps their behavior covered directly.
func TestDOBaselinePrivateURLHelper(t *testing.T) {
	s := DOBaselineSecrets{BoardDatabaseURL: "postgres://mesh_app:TESTPW@pyx-main-db-do-user-1-0.k.db.ondigitalocean.com:25060/postgres?sslmode=require"}
	if got := s.privateURL(false); got != s.BoardDatabaseURL {
		t.Errorf("privateURL(false) must be verbatim, got %q", got)
	}
	got := s.privateURL(true)
	if !strings.Contains(got, "private-pyx-main-db-do-user") {
		t.Errorf("privateURL(true) must rewrite the host to the private endpoint, got %q", got)
	}
	// Idempotent: already-private host is untouched.
	if again := (DOBaselineSecrets{BoardDatabaseURL: got}).privateURL(true); again != got {
		t.Errorf("privateURL(true) must be idempotent on an already-private host, got %q want %q", again, got)
	}
}

// TestDOBaselineEdgeTLSOrigins asserts the F4-prep option adds an nginx :443
// terminator to the sole remaining Cloudflare-routed origin (sso) with the
// correct hostname and upstream port, and leaves the base estate untouched
// when off. backend/mcp were dropped from doEdgeOrigins() along with their
// DOBaselineServices() entries — see do_baseline.go's file-header note.
func TestDOBaselineEdgeTLSOrigins(t *testing.T) {
	off := strings.Join(renderTestBaseline(t, DOBaselineOptions{PrivateDBHost: true}), "\n")
	if strings.Contains(off, "listen 443 ssl") {
		t.Errorf("EdgeTLSOrigins off must not emit any :443 terminator")
	}
	on := strings.Join(renderTestBaseline(t, DOBaselineOptions{PrivateDBHost: true, EdgeTLSOrigins: true}), "\n")
	// One terminator for the sole remaining origin (sso) = 1 `listen 443 ssl`.
	if n := strings.Count(on, "listen 443 ssl"); n != 1 {
		t.Errorf("expected 1 :443 terminator (sso), got %d", n)
	}
	wantPairs := []struct{ host, port string }{
		{"staging-auth.pyxcloud.io", "proxy_pass http://127.0.0.1:8080"},
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
	for _, gone := range []string{"staging-api.pyxcloud.io", "staging-mcp.passo.build"} {
		if strings.Contains(on, gone) {
			t.Errorf("dropped origin %q must not appear in the render", gone)
		}
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
	s, err := RenderEdgeTLSTerminatorSnippet(EdgeTLSTerminator{Hostname: "staging-auth.pyxcloud.io", UpstreamPort: 8080})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	for _, want := range []string{"listen 443 ssl", "server_name staging-auth.pyxcloud.io;", "proxy_pass http://127.0.0.1:8080", "nginx -t"} {
		if !strings.Contains(s, want) {
			t.Errorf("snippet missing %q", want)
		}
	}
}

// fullBaselineSecrets extends the test secrets with the sso literal-injected set
// so the FullServiceBootstraps render succeeds. Most sso secrets (keycloak-db
// URL/creds, admin password, Spaces keys, SMTP) are now Vault data sources
// (EPIC-BOOTFETCH-AWS-SM-TO-VAULT wave 2) and need no test value; only the two
// unmigrated fields remain literal.
func fullBaselineSecrets() DOBaselineSecrets {
	s := testDOBaselineSecrets()
	s.SSOVaultOIDCSecret = "TEST_VAULT_OIDC"
	s.SSORunnerPublicKey = "ssh-ed25519 AAAATESTRUNNERKEY runner@pyx"
	return s
}

func renderFullBaseline(t *testing.T) []string {
	t.Helper()
	in := DOBaselineInput("Frankfurt", "x86_64", "ubuntu", "1.30")
	docs, err := AssembleDOBaseline(context.Background(), MustEmbedded(), in, fullBaselineSecrets(),
		DOBaselineOptions{PrivateDBHost: true, FullServiceBootstraps: true})
	if err != nil {
		t.Fatalf("AssembleDOBaseline(full): %v", err)
	}
	return docs
}

// perServiceUserData splits the rendered docs into a service->user_data map by
// scanning each droplet_autoscale resource's heredoc, so assertions can be scoped
// to the right box (a marker leaking into the wrong service is a real bug).
func perServiceUserData(t *testing.T, docs []string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, d := range docs {
		if !strings.Contains(d, `resource "digitalocean_droplet_autoscale"`) {
			continue
		}
		// name is the second quoted token on the resource line.
		i := strings.Index(d, `resource "digitalocean_droplet_autoscale" "`)
		rest := d[i+len(`resource "digitalocean_droplet_autoscale" "`):]
		name := rest[:strings.Index(rest, `"`)]
		ud := ""
		if s := strings.Index(d, "<<-USERDATA\n"); s >= 0 {
			body := d[s+len("<<-USERDATA\n"):]
			if e := strings.Index(body, "USERDATA\n"); e >= 0 {
				ud = body[:e]
			}
		}
		out[name] = ud
	}
	return out
}

// TestDOBaselineFullServiceBootstraps is the DURABILITY contract (pd-MIG-CUTOVER-F5):
// with FullServiceBootstraps set, EVERY service droplet template carries its complete
// service bootstrap, and the sole remaining Cloudflare-Full origin (sso) also
// carries the nginx :443 terminator to the correct local port. A self-heal/roll from
// this render boots the real service + edge, not a bare box. backend/mcp/obs/sast/vpn
// markers were removed along with their DOBaselineServices() entries — see
// do_baseline.go's file-header "RECONCILED AGAINST LIVE" note.
func TestDOBaselineFullServiceBootstraps(t *testing.T) {
	docs := renderFullBaseline(t)
	svc := perServiceUserData(t, docs)

	// 1. Every service carries a non-empty full bootstrap with its service marker.
	markers := map[string][]string{
		"sso":        {"keycloak", "KC_HOSTNAME=staging-auth.pyxcloud.io", "KC_PROXY_HEADERS=xforwarded"},
		"staging-fe": {"staging-fe"},
	}
	for name, wants := range markers {
		ud, ok := svc[name]
		if !ok || strings.TrimSpace(ud) == "" {
			t.Fatalf("service %q has no user_data in the durable render", name)
		}
		for _, w := range wants {
			if !strings.Contains(ud, w) {
				t.Errorf("service %q user_data missing marker %q", name, w)
			}
		}
	}
	for _, name := range []string{"backend", "mcp", "obs", "sast", "vpn"} {
		if _, ok := svc[name]; ok {
			t.Errorf("live-reconciled render must not emit a %q droplet_autoscale group", name)
		}
	}

	// 2. The sole remaining edge origin (sso) carries a :443 terminator to the
	//    correct upstream port. staging-fe must NOT (it is a private origin
	//    reached directly by the VPC edge, not through this terminator path).
	edge := map[string]struct{ host, upstream string }{
		"sso": {"staging-auth.pyxcloud.io", "proxy_pass http://127.0.0.1:8080"},
	}
	for name, e := range edge {
		ud := svc[name]
		if !strings.Contains(ud, "listen 443 ssl") {
			t.Errorf("edge origin %q missing nginx :443 terminator (listen 443 ssl)", name)
		}
		if !strings.Contains(ud, "server_name "+e.host+";") {
			t.Errorf("edge origin %q missing server_name %q", name, e.host)
		}
		if !strings.Contains(ud, e.upstream) {
			t.Errorf("edge origin %q missing upstream %q", name, e.upstream)
		}
	}
	// staging-fe is not in doEdgeOrigins() (only sso is), so it must not get a
	// SECOND nginx :443 block appended by edgeTerminatorFor on top of its own
	// self-terminated TLS listener (platform_bootstrap_staging_fe_do.go).
	if n := strings.Count(svc["staging-fe"], "listen 443 ssl"); n != 1 {
		t.Errorf("staging-fe must carry exactly its own :443 listener, not an appended edge terminator (got %d)", n)
	}

	// 3. ${var.<x>} references survive un-escaped (terraform must interpolate the
	//    injected secrets), and bash ${...} is escaped to $${...} in the heredoc.
	joined := strings.Join(docs, "\n")
	if !strings.Contains(joined, "${var.") {
		t.Errorf("durable render must keep ${var.<x>} references for terraform")
	}
	if strings.Contains(joined, "$${var.") {
		t.Errorf("${var.<x>} must NOT be escaped (terraform would fail to interpolate)")
	}

	// 4. The harness must be able to declare a variable for every referenced name.
	vars := DOBaselineVariableNames()
	if len(vars) == 0 {
		t.Fatalf("DOBaselineVariableNames returned no variables")
	}
	for _, name := range vars {
		if !strings.Contains(joined, "${var."+name+"}") {
			t.Errorf("declared variable %q is not referenced by any rendered user_data", name)
		}
	}
	// Every ${var.<x>} reference in the render must have a matching declaration.
	for _, ref := range distinctVarRefs(joined) {
		found := false
		for _, name := range vars {
			if name == ref {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("user_data references ${var.%s} but DOBaselineVariableNames does not declare it", ref)
		}
	}
}

// distinctVarRefs extracts the distinct <x> from every ${var.<x>} occurrence.
func distinctVarRefs(s string) []string {
	seen := map[string]bool{}
	var out []string
	for {
		i := strings.Index(s, "${var.")
		if i < 0 {
			break
		}
		s = s[i+len("${var."):]
		j := strings.IndexByte(s, '}')
		if j < 0 {
			break
		}
		name := s[:j]
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
		s = s[j+1:]
	}
	return out
}

// TestDOBaselineFullRequiresSSOSecrets asserts the durable render rejects a missing
// sso literal secret (sso inlines its values, so they must be injected).
func TestDOBaselineFullRequiresSSOSecrets(t *testing.T) {
	in := DOBaselineInput("Frankfurt", "x86_64", "ubuntu", "1.30")
	s := testDOBaselineSecrets() // no SSO* fields
	_, err := AssembleDOBaseline(context.Background(), MustEmbedded(), in, s,
		DOBaselineOptions{PrivateDBHost: true, FullServiceBootstraps: true})
	if err == nil {
		t.Fatalf("expected error: sso literal secrets missing in the durable render")
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

// NOTE: TestDOBaselinePrivateHostVerbatim (public-host-verbatim-when-off) was
// removed here — it asserted on BoardDatabaseURL appearing in the rendered
// user_data, which only ever happened via the mcp durable bootstrap. mcp is no
// longer part of DOBaselineServices() (see the file-header "RECONCILED AGAINST
// LIVE" note), so BoardDatabaseURL is never interpolated into any render
// output anymore. The underlying privateURL/verbatim behavior is still covered
// directly by TestDOBaselinePrivateURLHelper above.

// TestDOBaselineRequiresSecrets asserts missing secrets are rejected.
func TestDOBaselineRequiresSecrets(t *testing.T) {
	in := DOBaselineInput("Frankfurt", "x86_64", "ubuntu", "1.30")
	_, err := AssembleDOBaseline(context.Background(), MustEmbedded(), in, DOBaselineSecrets{}, DOBaselineOptions{})
	if err == nil {
		t.Fatalf("expected error for empty secrets")
	}
}
