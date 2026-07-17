package catalog

import (
	"context"
	"fmt"
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

// TestDOBaselineStagingOriginsArePrivate is the staging network-boundary
// contract: API, auth, MCP and FE origins are reachable only through the VPC
// edge. Authentication remains an independent application-layer requirement;
// a private source must never create an auth bypass or a public origin rule.
func TestDOBaselineStagingOriginsArePrivate(t *testing.T) {
	joined := strings.Join(renderTestBaseline(t, DOBaselineOptions{PrivateDBHost: true}), "\n")
	if strings.Contains(joined, `source_addresses = ["0.0.0.0/0", "::/0"]`) {
		t.Fatal("staging baseline must not expose TLS origins to the public internet")
	}
	if !strings.Contains(joined, `source_tags = ["pyx-edge"]`) {
		t.Fatal("staging TLS origins must only accept traffic from the VPC edge")
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
		{"staging-auth.pyxcloud.io", "proxy_pass http://127.0.0.1:8080"},
		{"staging-api.pyxcloud.io", "proxy_pass http://127.0.0.1:8080"},
		{"staging-mcp.passo.build", "proxy_pass http://127.0.0.1:8787"},
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
// service bootstrap, and the three Cloudflare-Full origins (sso/backend/mcp) also
// carry the nginx :443 terminator to the correct local port. A self-heal/roll from
// this render boots the real service + edge, not a bare box.
func TestDOBaselineFullServiceBootstraps(t *testing.T) {
	docs := renderFullBaseline(t)
	svc := perServiceUserData(t, docs)

	// 1. Every service carries a non-empty full bootstrap with its service marker.
	markers := map[string][]string{
		"sso":     {"keycloak", "KC_HOSTNAME=staging-auth.pyxcloud.io", "KC_PROXY_HEADERS=xforwarded"},
		"backend": {"pyx-backend", "ExecStart=/home/main/pyx-backend", "/readyz"},
		"mcp":     {"passobuild-mcp", "PYXCLOUD_MCP_HTTP_PORT=8787"},
		"obs":     {"observability"},
		"sast":    {"semgrep"},
		"vpn":     {"wireguard", "wg0"},
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

	// 2. Exactly the three edge origins (sso/backend/mcp) carry a :443 terminator to
	//    the correct upstream port. sast/vpn must NOT. obs has its own :443 (checked
	//    separately) but must NOT carry an sso/backend/mcp public server_name.
	edge := map[string]struct{ host, upstream string }{
		"sso":     {"staging-auth.pyxcloud.io", "proxy_pass http://127.0.0.1:8080"},
		"backend": {"staging-api.pyxcloud.io", "proxy_pass http://127.0.0.1:8080"},
		"mcp":     {"staging-mcp.passo.build", "proxy_pass http://127.0.0.1:8787"},
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
	for _, name := range []string{"sast", "vpn"} {
		if strings.Contains(svc[name], "listen 443 ssl") {
			t.Errorf("service %q must NOT carry a :443 edge terminator", name)
		}
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
	// origin_tls_key/origin_tls_cert are declared unconditionally (like every other
	// name here) but are only REFERENCED in plain HCL (var.x, not a heredoc
	// ${var.x}) and only when LBTermination is set — this render has it off, so
	// they are exempted from the "must be referenced" direction of the check.
	lbTerminationVars := map[string]bool{doOriginCertKeyVar: true, doOriginCertLeafVar: true}
	vars := DOBaselineVariableNames()
	if len(vars) == 0 {
		t.Fatalf("DOBaselineVariableNames returned no variables")
	}
	for _, name := range vars {
		if lbTerminationVars[name] {
			continue
		}
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

// --- LBTermination (durable DO edge fix) -----------------------------------
//
// The staging outage this flag fixes: droplets self-terminated TLS on a PUBLIC
// :443 (nginx listening on 0.0.0.0/0). The corrected model is LB-terminates-TLS +
// app-port-reachable-only-from-the-LB. These tests pin (a) per-service LBs with
// https termination (no tls_passthrough), (b) firewall app-port inbound scoped to
// source_load_balancer_uids with no 0.0.0.0/0 on the app port, (c) no nginx :443
// terminator snippet anywhere, (d) the origin cert vars are declared, and that the
// flag being off leaves the byte-for-byte legacy output unchanged.

// TestDOBaselineLBTerminationOffUnchanged asserts LBTermination defaults false and,
// left off, produces the exact legacy estate (same resource set / firewall / LB
// shape asserted by TestDOBaselineResourceSet): three per-service TCP-passthrough
// load balancers (lb-sso/lb-backend/lb-mcp), reconciled to match the already-live
// DO estate (pd-MIG-CUTOVER state reconciliation) — not the single-LB shape this
// test asserted before that reconciliation landed.
func TestDOBaselineLBTerminationOffUnchanged(t *testing.T) {
	withoutFlag := strings.Join(renderTestBaseline(t, DOBaselineOptions{PrivateDBHost: true}), "\n")
	explicitOff := strings.Join(renderTestBaseline(t, DOBaselineOptions{PrivateDBHost: true, LBTermination: false}), "\n")
	if withoutFlag != explicitOff {
		t.Fatalf("LBTermination zero-value must match an explicit false")
	}
	// Legacy single firewall + three per-service tls_passthrough LBs (lb-sso /
	// lb-backend / lb-mcp), no LBTermination certificate resource.
	for _, want := range []string{
		`resource "digitalocean_firewall" "passo-do-baseline-sg"`,
		`resource "digitalocean_loadbalancer" "lb-sso"`,
		`resource "digitalocean_loadbalancer" "lb-backend"`,
		`resource "digitalocean_loadbalancer" "lb-mcp"`,
		`tls_passthrough = false`,
	} {
		if !strings.Contains(withoutFlag, want) {
			t.Errorf("legacy (flag off) output missing %q", want)
		}
	}
	for _, unwanted := range []string{
		`resource "digitalocean_loadbalancer" "edge-lb"`,
		`resource "digitalocean_loadbalancer" "sso-lb"`,
		`resource "digitalocean_loadbalancer" "backend-lb"`,
		`resource "digitalocean_loadbalancer" "mcp-lb"`,
		`resource "digitalocean_certificate"`,
		`source_load_balancer_uids`,
	} {
		if strings.Contains(withoutFlag, unwanted) {
			t.Errorf("legacy (flag off) output must not contain %q", unwanted)
		}
	}
}

// TestDOBaselineLBTerminationPerServiceLB asserts each public origin gets its own
// LB terminating TLS (https entry, http target, certificate_name, NO
// tls_passthrough) and the correct target port + healthcheck per service.
func TestDOBaselineLBTerminationPerServiceLB(t *testing.T) {
	joined := strings.Join(renderTestBaseline(t, DOBaselineOptions{PrivateDBHost: true, LBTermination: true}), "\n")

	wantLBs := []struct {
		name       string
		tag        string
		port       int
		healthPath string
	}{
		{"sso-lb", "pyx-sso", 8080, "/q/health"},
		{"backend-lb", "pyx-backend", 8080, "/healthz"},
		{"mcp-lb", "pyx-mcp", 8787, "/health"},
	}
	for _, w := range wantLBs {
		i := strings.Index(joined, `resource "digitalocean_loadbalancer" "`+w.name+`"`)
		if i < 0 {
			t.Fatalf("missing per-service LB %q", w.name)
		}
		// Scope the assertions to this LB's block (up to the next top-level resource).
		block := joined[i:]
		if j := strings.Index(block[1:], "\nresource \""); j >= 0 {
			block = block[:j+1]
		}
		if !strings.Contains(block, `droplet_tag = "`+w.tag+`"`) {
			t.Errorf("%s: expected droplet_tag %q", w.name, w.tag)
		}
		if !strings.Contains(block, `entry_protocol   = "https"`) {
			t.Errorf("%s: expected entry_protocol https", w.name)
		}
		if !strings.Contains(block, `target_protocol  = "http"`) {
			t.Errorf("%s: expected target_protocol http (LB terminates TLS)", w.name)
		}
		if !strings.Contains(block, fmt.Sprintf("target_port      = %d", w.port)) {
			t.Errorf("%s: expected target_port %d", w.name, w.port)
		}
		if strings.Contains(block, "tls_passthrough") {
			t.Errorf("%s: must NOT use tls_passthrough (the LB terminates TLS)", w.name)
		}
		if !strings.Contains(block, "certificate_name") {
			t.Errorf("%s: expected a certificate_name reference", w.name)
		}
		if !strings.Contains(block, fmt.Sprintf("port                     = %d", w.port)) {
			t.Errorf("%s: expected healthcheck port %d", w.name, w.port)
		}
		if !strings.Contains(block, `path                     = "`+w.healthPath+`"`) {
			t.Errorf("%s: expected healthcheck path %q", w.name, w.healthPath)
		}
	}
	// The legacy shared edge-lb must be gone.
	if strings.Contains(joined, `resource "digitalocean_loadbalancer" "edge-lb"`) {
		t.Errorf("LBTermination must replace edge-lb with per-service LBs")
	}
}

// TestDOBaselineLBTerminationCertificate asserts the origin certificate is a
// "custom" type sourced from TF vars (no inlined secret material) and is what the
// per-service LBs reference via certificate_name.
func TestDOBaselineLBTerminationCertificate(t *testing.T) {
	joined := strings.Join(renderTestBaseline(t, DOBaselineOptions{PrivateDBHost: true, LBTermination: true}), "\n")
	if !strings.Contains(joined, `resource "digitalocean_certificate"`) {
		t.Fatalf("expected a digitalocean_certificate resource")
	}
	if !strings.Contains(joined, `type              = "custom"`) {
		t.Errorf("origin certificate must be type=custom (Cloudflare Origin cert)")
	}
	if !strings.Contains(joined, "private_key       = var."+doOriginCertKeyVar) {
		t.Errorf("private_key must come from var.%s (no secret material in code)", doOriginCertKeyVar)
	}
	if !strings.Contains(joined, "leaf_certificate  = var."+doOriginCertLeafVar) {
		t.Errorf("leaf_certificate must come from var.%s (no secret material in code)", doOriginCertLeafVar)
	}
	// No inlined PEM material anywhere.
	if strings.Contains(joined, "-----BEGIN") {
		t.Errorf("no certificate/key material may be inlined in the rendered HCL")
	}
	// Every per-service LB references the shared cert by name.
	if n := strings.Count(joined, "certificate_name = digitalocean_certificate."); n != 3 {
		t.Errorf("expected 3 certificate_name references (one per origin LB), got %d", n)
	}
}

// TestDOBaselineLBTerminationFirewall asserts the app-port inbound rule for each
// origin is scoped to source_load_balancer_uids and that NO 0.0.0.0/0 inbound rule
// exists on any app port (443 or the upstream ports).
func TestDOBaselineLBTerminationFirewall(t *testing.T) {
	joined := strings.Join(renderTestBaseline(t, DOBaselineOptions{PrivateDBHost: true, LBTermination: true}), "\n")

	wantFirewalls := []struct {
		name string
		port int
		lb   string
	}{
		{"passo-do-baseline-sso-sg", 8080, "sso-lb"},
		{"passo-do-baseline-backend-sg", 8080, "backend-lb"},
		{"passo-do-baseline-mcp-sg", 8787, "mcp-lb"},
	}
	for _, w := range wantFirewalls {
		i := strings.Index(joined, `resource "digitalocean_firewall" "`+w.name+`"`)
		if i < 0 {
			t.Fatalf("missing per-service firewall %q", w.name)
		}
		block := joined[i:]
		if j := strings.Index(block[1:], "\nresource \""); j >= 0 {
			block = block[:j+1]
		}
		if !strings.Contains(block, fmt.Sprintf(`port_range                = "%d"`, w.port)) {
			t.Errorf("%s: expected inbound port_range %d", w.name, w.port)
		}
		if !strings.Contains(block, "source_load_balancer_uids = [digitalocean_loadbalancer."+w.lb+".id]") {
			t.Errorf("%s: expected source_load_balancer_uids scoped to %s", w.name, w.lb)
		}
		// Only the inbound_rule may not be public; egress (icmp/tcp/udp to
		// 0.0.0.0/0) is the shared, legitimate outbound rule set every baseline
		// firewall carries and is out of scope for this check.
		if in := block[:strings.Index(block, "outbound_rule")]; strings.Contains(in, "0.0.0.0/0") {
			t.Errorf("%s: inbound_rule must NOT allow 0.0.0.0/0", w.name)
		}
	}
	// No firewall anywhere opens 443 (or any app port) to the public internet.
	if strings.Contains(joined, `port_range       = "443"`) {
		t.Errorf("LBTermination must not leave a public :443 inbound rule")
	}
	if strings.Contains(joined, `source_addresses = ["0.0.0.0/0", "::/0"]`) {
		// This exact literal is only ever used for the legacy public :443 rule in
		// this file; its presence means the public rule leaked into the LB path.
		t.Errorf("LBTermination must not carry a 0.0.0.0/0 inbound source_addresses rule")
	}
}

// TestDOBaselineLBTerminationNoNginxTerminator asserts that with LBTermination
// set, no droplet carries the nginx :443 terminator snippet — even when
// EdgeTLSOrigins or FullServiceBootstraps is also requested. The LB is now the
// TLS edge; a droplet-side terminator would reopen the public :443 exposure.
func TestDOBaselineLBTerminationNoNginxTerminator(t *testing.T) {
	cases := []struct {
		name string
		opts DOBaselineOptions
	}{
		{"bare", DOBaselineOptions{PrivateDBHost: true, LBTermination: true}},
		{"with EdgeTLSOrigins also set", DOBaselineOptions{PrivateDBHost: true, LBTermination: true, EdgeTLSOrigins: true}},
	}
	for _, c := range cases {
		joined := strings.Join(renderTestBaseline(t, c.opts), "\n")
		if strings.Contains(joined, "listen 443 ssl") {
			t.Errorf("%s: must not emit any nginx :443 terminator", c.name)
		}
		if strings.Contains(joined, "proxy_pass http://127.0.0.1:") {
			t.Errorf("%s: must not emit any terminator reverse-proxy block", c.name)
		}
	}

	// Also true for the FullServiceBootstraps durable render — scoped to the
	// public Cloudflare origins (sso/backend/mcp). The `obs` service carries its
	// OWN independent nginx :443 listener (VPN-only observability dashboard, not
	// a Cloudflare public origin — see doEdgeOrigins' doc comment) which is
	// unrelated to EdgeTLSOrigins/LBTermination and must be left alone.
	in := DOBaselineInput("Frankfurt", "x86_64", "ubuntu", "1.30")
	docs, err := AssembleDOBaseline(context.Background(), MustEmbedded(), in, fullBaselineSecrets(),
		DOBaselineOptions{PrivateDBHost: true, FullServiceBootstraps: true, LBTermination: true})
	if err != nil {
		t.Fatalf("AssembleDOBaseline(full+LBTermination): %v", err)
	}
	svc := perServiceUserData(t, docs)
	for _, name := range []string{"sso", "backend", "mcp"} {
		if strings.Contains(svc[name], "listen 443 ssl") {
			t.Errorf("FullServiceBootstraps+LBTermination: origin %q must not carry a nginx :443 terminator", name)
		}
	}
	// The real per-service bootstraps must still be present (LBTermination only
	// changes the edge, not the service itself).
	for _, want := range []string{"keycloak", "pyx-backend", "passobuild-mcp"} {
		found := false
		for _, ud := range svc {
			if strings.Contains(ud, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected some service user_data to contain %q", want)
		}
	}
}

// TestDOBaselineLBTerminationVariableNames asserts the origin cert TF var names
// are declared by DOBaselineVariableNames() (so the harness always emits a
// matching `variable` block for them, regardless of which flags are set).
func TestDOBaselineLBTerminationVariableNames(t *testing.T) {
	vars := DOBaselineVariableNames()
	want := map[string]bool{doOriginCertKeyVar: false, doOriginCertLeafVar: false}
	for _, v := range vars {
		if _, ok := want[v]; ok {
			want[v] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("DOBaselineVariableNames() must declare %q", name)
		}
	}
}
