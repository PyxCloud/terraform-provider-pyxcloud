package catalog

import (
	"fmt"
	"strings"
)

// edge_tls_terminator.go — pd-MIG-CUTOVER-F4-PREP (EPIC-AWS-TO-DO-MIGRATION).
//
// WHY THIS EXISTS
// ---------------
// The DOKS -> droplet pivot removed the L7 host-routing that the DOKS ingress
// used to provide. The DO regional load-balancer (edge-lb, do_baseline.go) is an
// L4 `tls_passthrough` LB and CANNOT host-route: a probe to it with
// Host: beta-auth returned 000 because the service droplets listen only on their
// plain-HTTP service ports (backend 8080, mcp 8787, sso 8080) and NOTHING listens
// on :443. The per-service DO firewall, meanwhile, only opens inbound :443.
//
// The cutover model is therefore: **Cloudflare terminates public TLS and routes
// each hostname to the correct DO service origin.** For Cloudflare "Full" SSL the
// origin must present a cert on :443, so each origin droplet needs a lightweight
// TLS terminator on :443 that reverse-proxies to the local service port. The
// `obs` droplet ALREADY does exactly this (nginx `listen 443 ssl;` -> 127.0.0.1
// :8080 with a self-signed cert, VPN-only). This file lifts that proven, working
// pattern into a REUSABLE, SECRET-FREE catalog building block so backend / mcp /
// sso get an identical :443 origin — the last missing piece before the DNS flip.
//
// SECURITY / SCOPE
//   - No secret is inlined: the terminator only needs a hostname + an upstream
//     port. The self-signed cert is generated on the box at boot (Cloudflare Full,
//     not Full-Strict, accepts a self-signed origin cert; upgrade to a Cloudflare
//     Origin CA cert later for Full-Strict — see docs/cutover/CLOUDFLARE-CUTOVER.md).
//   - The snippet is idempotent (regenerates the cert only if absent) and appends
//     to an existing bootstrap: it installs nginx + openssl, writes one server
//     block, and (re)starts nginx. It does NOT touch the service itself.
//   - X-Forwarded-Proto=https / X-Forwarded-For are set so the upstream (Keycloak
//     with KC_PROXY_HEADERS=xforwarded, Quarkus, the MCP server) issues correct
//     absolute URLs behind the Cloudflare -> origin hop.

// EdgeTLSTerminator describes one nginx :443 -> local-port reverse proxy that
// turns a plain-HTTP service droplet into a Cloudflare "Full" origin.
type EdgeTLSTerminator struct {
	// Hostname is the public FQDN Cloudflare routes to this origin, e.g.
	// "beta-auth.pyxcloud.io". It names the self-signed cert CN/SAN and the nginx
	// server_name. Required.
	Hostname string
	// UpstreamPort is the local plain-HTTP service port to proxy to (backend 8080,
	// mcp 8787, sso 8080). Required, 1..65535.
	UpstreamPort int
}

// Validate checks the terminator descriptor.
func (t EdgeTLSTerminator) Validate() error {
	if strings.TrimSpace(t.Hostname) == "" {
		return fmt.Errorf("edge-tls-terminator: hostname is required")
	}
	if t.UpstreamPort < 1 || t.UpstreamPort > 65535 {
		return fmt.Errorf("edge-tls-terminator: upstream_port %d out of range (1..65535)", t.UpstreamPort)
	}
	return nil
}

// RenderEdgeTLSTerminatorSnippet returns a bash fragment (no shebang) that
// installs an nginx :443 TLS terminator reverse-proxying to 127.0.0.1:<port>. It
// is meant to be APPENDED to a service's existing cloud-init user_data so the box
// becomes a Cloudflare-Full origin without changing the service. Faithful port of
// the obs droplet's working nginx block (do_baseline state `obs` user_data).
//
// The fragment is self-contained and safe under `set -euo pipefail`.
func RenderEdgeTLSTerminatorSnippet(t EdgeTLSTerminator) (string, error) {
	if err := t.Validate(); err != nil {
		return "", err
	}
	host := strings.TrimSpace(t.Hostname)
	// Sanitize the host for a filename (no path separators / spaces).
	certBase := strings.NewReplacer("/", "_", " ", "_").Replace(host)

	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }

	w("# --- Cloudflare-Full origin: nginx :443 TLS terminator -> 127.0.0.1:%d ---", t.UpstreamPort)
	w("# pd-MIG-CUTOVER-F4-PREP. Self-signed origin cert (Cloudflare Full). The DO")
	w("# firewall opens :443 only; Cloudflare terminates public TLS and proxies here.")
	w("export DEBIAN_FRONTEND=noninteractive")
	w("apt-get update -y")
	w("apt-get install -y nginx openssl")
	w("mkdir -p /etc/nginx/certs")
	w("if [ ! -f /etc/nginx/certs/%s.crt ]; then", certBase)
	w("  openssl req -x509 -nodes -newkey rsa:2048 -days 3650 \\")
	w("    -keyout /etc/nginx/certs/%s.key -out /etc/nginx/certs/%s.crt \\", certBase, certBase)
	w("    -subj \"/CN=%s\" -addext \"subjectAltName=DNS:%s\"", host, host)
	w("fi")
	w("cat >/etc/nginx/conf.d/edge-%s.conf <<'NGINXEOF'", certBase)
	w("server {")
	w("  listen 443 ssl;")
	w("  server_name %s;", host)
	w("  ssl_certificate     /etc/nginx/certs/%s.crt;", certBase)
	w("  ssl_certificate_key /etc/nginx/certs/%s.key;", certBase)
	w("  # Large auth/redirect headers (Keycloak) — generous buffers.")
	w("  proxy_buffer_size 16k;")
	w("  proxy_buffers 8 16k;")
	w("  client_max_body_size 25m;")
	w("  location / {")
	w("    proxy_pass http://127.0.0.1:%d;", t.UpstreamPort)
	w("    proxy_set_header Host $host;")
	w("    proxy_set_header X-Real-IP $remote_addr;")
	w("    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;")
	w("    proxy_set_header X-Forwarded-Proto https;")
	w("    proxy_set_header X-Forwarded-Host $host;")
	w("    proxy_http_version 1.1;")
	w("  }")
	w("}")
	w("NGINXEOF")
	// Drop the stock default site so :80/:443 default_server doesn't shadow us.
	w("rm -f /etc/nginx/sites-enabled/default 2>/dev/null || true")
	w("nginx -t")
	w("systemctl enable nginx")
	w("systemctl restart nginx")
	w("# --- end Cloudflare-Full origin terminator ---")

	return strings.TrimRight(b.String(), "\n"), nil
}
