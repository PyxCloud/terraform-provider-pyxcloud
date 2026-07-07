package catalog

import (
	"fmt"
	"strings"
)

// platform_bootstrap_staging_fe_do.go — pd-STAGING-FE-SHIM (parallel, dedicated
// replacement for the fragile staging.passo.build shim).
//
// PROBLEM
// -------
// staging.passo.build (VPN-only; edge SNI-routes :443 to a hand-managed shim at
// 10.0.1.7:443) keeps going down (ERR_CONNECTION_CLOSED). That shim is
// co-located on the `obs` droplet (platform_bootstrap_obs_do.go) — no pipeline,
// hand-SSH'd — and if it ever tries to `next build`/`next start` on that box it
// risks OOMing the observability aggregator it shares RAM with. There is no
// committed source for what runs there today.
//
// FIX (this file)
// ----------------
// A dedicated, stateless `staging-fe` droplet that runs ONLY nginx as a
// reverse proxy to the existing Amplify staging branch
// (https://staging.de9vejckwo4b9.amplifyapp.com, app de9vejckwo4b9, eu-west-1).
// There is NO `next build`/`npm install` on this box — nginx is the entire
// workload, so it cannot OOM the way a Next.js self-build can. The Amplify
// branch has `enableBasicAuth: true` (it returns 401 to anonymous callers), so
// this proxy injects the branch's Basic-Auth credential as an upstream
// `Authorization: Basic <creds>` header — fetched from DO Vault at BOOT time
// (mirrors platform_bootstrap_obs_do.go's mesh-secret AppRole fetch via
// vault_bootfetch.go), never inlined, never a render-time Terraform variable.
//
// WHY A NEW DOBaselineServices() ENTRY, NOT ANOTHER obs SIDECAR: the whole
// point is decoupling from the obs box's shared fate (RAM contention, hand-SSH
// drift, single point of failure for both dashboards and the FE). A dedicated
// droplet-autoscale-of-1 (self-heal floor of 1, same mechanism `obs`/`sast`/
// `vpn` already use — see do_baseline.go) gives it independent self-healing
// without an obs redeploy ever touching it, and vice versa.
//
// WHY ITS OWN FIREWALL, NOT THE SHARED passo-do-baseline-sg: every other
// baseline service (in the non-LBTermination, default path) shares one
// firewall that opens :443 to 0.0.0.0/0 — acceptable for services whose only
// public exposure is either behind Cloudflare or genuinely meant to be
// internet-reachable. staging.passo.build is VPN-only by product intent (it is
// not in Cloudflare DNS at all): its ONLY legitimate caller is the `pyx-edge`
// SNI router. Reusing the shared firewall would silently re-expose :443 to the
// whole internet, repeating the exact posture gap that made the obs-box shim
// fragile in the first place. See AssembleDOBaseline's dedicated
// `<baseline>-staging-fe-sg` firewall (source_tags = ["pyx-edge"] for 443,
// VPC-only for 22 — mirrors the Vault-HA private-only firewall precedent in
// vaultha_droplet_do.go).

const (
	// stagingFEAmplifyHostname is the Amplify branch URL's hostname — the proxy
	// upstream. proxy_ssl_server_name/SNI and the Host header sent upstream both
	// use this literal value (Amplify multi-tenant TLS termination requires the
	// SNI to match the branch's own *.amplifyapp.com hostname, not the public
	// staging.passo.build hostname end users see).
	stagingFEAmplifyHostname = "staging.de9vejckwo4b9.amplifyapp.com"
	// stagingFEAmplifyUpstream is the full https:// upstream URL nginx proxies to.
	stagingFEAmplifyUpstream = "https://" + stagingFEAmplifyHostname
	// stagingFEPublicHostname is the public (VPN-only) FQDN end users hit; used
	// for the self-signed cert CN/SAN and the nginx server_name. The edge SNI
	// router (topology.json) routes this hostname's :443 traffic here.
	stagingFEPublicHostname = "staging.passo.build"
	// stagingFEConsoleHostname is an additional SAN/server_name alias some staging
	// links use (staging-console.passo.build); harmless to include even if unused.
	stagingFEConsoleHostname = "staging-console.passo.build"
)

// StagingFEDOBootstrapSpec is the typed input for the staging-fe shim bootstrap.
// Every secret is named by the Vault AppRole variable that authenticates the
// boot-time fetch (NOT the credential value) so nothing sensitive enters the
// abstract topology, Terraform state, or this repo.
type StagingFEDOBootstrapSpec struct {
	// VaultAddrVar / VaultRoleIDVar / VaultSecretIDVar name the Terraform
	// variables holding the Vault address and the `staging-fe-boot` AppRole
	// role_id/secret_id (never the values). Defaults: "vault_addr" (shared with
	// every other boot-fetching service), "staging_fe_vault_role_id",
	// "staging_fe_vault_secret_id".
	VaultAddrVar     string
	VaultRoleIDVar   string
	VaultSecretIDVar string
	// VaultKVPath is the KV-v2 leaf (under the `secret` mount, no `data/` infix)
	// holding the Amplify Basic-Auth credential. Default
	// "infra/staging/staging-fe/amplify-basic-auth" (follows the canonical
	// secret/infra/<env>/<service> convention — see do-vault-canonical-secrets).
	VaultKVPath string
	// VaultKey is the field inside that leaf holding the credential — the exact
	// base64(user:pass) string Amplify's branch `basicAuthCredentials` holds, so
	// it can be used verbatim as the `Authorization: Basic <value>` header with
	// no re-encoding. Default "basic_auth_b64".
	VaultKey string
}

// withDefaults fills the production-faithful defaults for any unset field.
func (s StagingFEDOBootstrapSpec) withDefaults() StagingFEDOBootstrapSpec {
	def := func(v, d string) string {
		if strings.TrimSpace(v) == "" {
			return d
		}
		return v
	}
	s.VaultAddrVar = def(s.VaultAddrVar, "vault_addr")
	s.VaultRoleIDVar = def(s.VaultRoleIDVar, "staging_fe_vault_role_id")
	s.VaultSecretIDVar = def(s.VaultSecretIDVar, "staging_fe_vault_secret_id")
	s.VaultKVPath = def(s.VaultKVPath, "infra/staging/staging-fe/amplify-basic-auth")
	s.VaultKey = def(s.VaultKey, "basic_auth_b64")
	return s
}

// StagingFEDOBootstrapVariableNames returns, in deterministic order, the
// Terraform variable names this bootstrap references, split into plain and
// sensitive — mirrors OBSDOBootstrapVariableNames. vault_addr is shared across
// every boot-fetching service so the caller (DOBaselineVariableNames) dedupes it.
func (s StagingFEDOBootstrapSpec) StagingFEDOBootstrapVariableNames() (plain []string, sensitive []string) {
	s = s.withDefaults()
	plain = []string{s.VaultAddrVar}
	sensitive = []string{s.VaultRoleIDVar, s.VaultSecretIDVar}
	return plain, sensitive
}

// RenderStagingFEDOBootstrapUserData renders the staging-fe shim cloud-init:
// nginx ONLY (no Next.js build, nothing that can OOM), reverse-proxying to the
// Amplify staging branch with the Basic-Auth credential injected from a
// boot-time Vault read, self-signed :443 termination (the edge does L4
// ssl_preread passthrough — see topology.json — so this droplet is the actual
// TLS terminator), and a small systemd watchdog that restarts nginx if it stops
// answering (defense in depth alongside the droplet-autoscale self-heal floor).
func RenderStagingFEDOBootstrapUserData(spec StagingFEDOBootstrapSpec) (string, error) {
	s := spec.withDefaults()

	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }

	w("#!/usr/bin/env bash")
	w("set -euo pipefail")
	w("export HOME=/root")
	w("export DEBIAN_FRONTEND=noninteractive")
	w("# staging-fe shim — dedicated, stateless nginx reverse proxy to the Amplify")
	w("# staging branch (%s). NO Next.js build/install on this box:", stagingFEAmplifyUpstream)
	w("# nginx is the entire workload, so it cannot repeat the obs-box OOM risk.")
	w("# pd-STAGING-FE-SHIM. Idempotent + restart-safe (re-running this script is safe).")
	w("apt-get update -y")
	w("apt-get install -y nginx openssl curl python3")
	w("")
	w("# --- self-signed :443 cert (the edge SNI-routes here via L4 ssl_preread")
	w("# passthrough — see topology.json — so THIS box terminates client TLS, not")
	w("# the edge). VPN-only + firewalled to the edge tag, so a trusted-once cert")
	w("# is acceptable, matching the obs droplet's posture.")
	w("mkdir -p /etc/nginx/certs")
	w("if [ ! -f /etc/nginx/certs/staging-fe.crt ]; then")
	w("  openssl req -x509 -nodes -newkey rsa:2048 -days 3650 \\")
	w("    -keyout /etc/nginx/certs/staging-fe.key -out /etc/nginx/certs/staging-fe.crt \\")
	w("    -subj \"/CN=%s\" \\", stagingFEPublicHostname)
	w("    -addext \"subjectAltName=DNS:%s,DNS:%s\"", stagingFEPublicHostname, stagingFEConsoleHostname)
	w("fi")
	w("")
	w("%s", VaultBootFetchSnippet(s.VaultAddrVar, s.VaultRoleIDVar, s.VaultSecretIDVar, s.VaultKVPath, s.VaultKey, "AMPLIFY_BASIC_AUTH"))
	w("")
	w("# --- nginx reverse-proxy config: inject Authorization, preserve SNI, rewrite")
	w("# redirects/cookies back to the public hostname so the Amplify origin's")
	w("# 401 (enableBasicAuth) and its own domain never leak to end users. Written")
	w("# with an UNQUOTED heredoc so $AMPLIFY_BASIC_AUTH interpolates ONCE here at")
	w("# boot; every nginx-native \\$variable below is backslash-escaped so nginx")
	w("# (not this shell) evaluates it at request time.")
	w("cat >/etc/nginx/conf.d/staging-fe.conf <<NGINX")
	w("server {")
	w("  listen 443 ssl;")
	w("  server_name %s %s;", stagingFEPublicHostname, stagingFEConsoleHostname)
	w("  ssl_certificate     /etc/nginx/certs/staging-fe.crt;")
	w("  ssl_certificate_key /etc/nginx/certs/staging-fe.key;")
	w("")
	w("  # Amplify's edge IPs are not stable — resolve at request time, don't cache")
	w("  # the DNS answer for the process lifetime (default resolver = Cloudflare +")
	w("  # Google as a fallback; no secrets, no egress restriction on this box).")
	w("  resolver 1.1.1.1 8.8.8.8 valid=60s;")
	w("")
	w("  proxy_buffer_size 16k;")
	w("  proxy_buffers 8 16k;")
	w("  proxy_busy_buffers_size 32k;")
	w("  client_max_body_size 25m;")
	w("  proxy_http_version 1.1;")
	w("")
	w("  location / {")
	w("    set \\$amplify_upstream \"%s\";", stagingFEAmplifyUpstream)
	w("    proxy_pass \\$amplify_upstream;")
	w("    proxy_ssl_server_name on;")
	w("    proxy_ssl_name %s;", stagingFEAmplifyHostname)
	w("    proxy_set_header Host %s;", stagingFEAmplifyHostname)
	w("    proxy_set_header Authorization \"Basic $AMPLIFY_BASIC_AUTH\";")
	w("    proxy_set_header X-Real-IP \\$remote_addr;")
	w("    proxy_set_header X-Forwarded-For \\$proxy_add_x_forwarded_for;")
	w("    proxy_set_header X-Forwarded-Proto https;")
	w("    proxy_set_header X-Forwarded-Host %s;", stagingFEPublicHostname)
	w("")
	w("    # Amplify issues redirects/Set-Cookie scoped to its own *.amplifyapp.com")
	w("    # domain — rewrite both back to the public hostname so the browser never")
	w("    # sees the Amplify URL (and so cookies survive the proxy hop).")
	w("    proxy_redirect %s/ https://%s/;", stagingFEAmplifyUpstream, stagingFEPublicHostname)
	w("    proxy_cookie_domain %s %s;", stagingFEAmplifyHostname, stagingFEPublicHostname)
	w("  }")
	w("}")
	w("NGINX")
	w("unset AMPLIFY_BASIC_AUTH")
	w("")
	w("# Drop the stock default site so :80/:443 default_server doesn't shadow us.")
	w("rm -f /etc/nginx/sites-enabled/default 2>/dev/null || true")
	w("nginx -t")
	w("systemctl enable nginx")
	w("systemctl restart nginx")
	w("")
	w("# --- watchdog: defense in depth alongside the droplet-autoscale self-heal")
	w("# floor (which replaces the WHOLE droplet on a CPU-target breach, not on a")
	w("# hung nginx worker). A simple systemd timer restarts nginx if it stops")
	w("# answering locally — cheap insurance against the exact failure mode")
	w("# (ERR_CONNECTION_CLOSED) this shim exists to fix.")
	w("cat >/usr/local/bin/staging-fe-watchdog <<'WATCHDOG'")
	w("#!/usr/bin/env bash")
	w("set -uo pipefail")
	w("code=$(curl -ks -o /dev/null -w '%%%%{http_code}' --max-time 5 https://127.0.0.1/ || echo 000)")
	w("if [ \"$code\" = \"000\" ]; then")
	w("  echo \"$(date -Is) staging-fe-watchdog: nginx not answering (code=$code), restarting\" | systemd-cat -t staging-fe-watchdog")
	w("  systemctl restart nginx")
	w("fi")
	w("WATCHDOG")
	w("chmod 0755 /usr/local/bin/staging-fe-watchdog")
	w("")
	w("cat >/etc/systemd/system/staging-fe-watchdog.service <<'UNIT'")
	w("[Unit]")
	w("Description=staging-fe nginx watchdog")
	w("After=network-online.target nginx.service")
	w("")
	w("[Service]")
	w("Type=oneshot")
	w("ExecStart=/usr/local/bin/staging-fe-watchdog")
	w("UNIT")
	w("")
	w("cat >/etc/systemd/system/staging-fe-watchdog.timer <<'UNIT'")
	w("[Unit]")
	w("Description=Run the staging-fe nginx watchdog every minute")
	w("")
	w("[Timer]")
	w("OnBootSec=2min")
	w("OnUnitActiveSec=1min")
	w("AccuracySec=10s")
	w("")
	w("[Install]")
	w("WantedBy=timers.target")
	w("UNIT")
	w("")
	w("systemctl daemon-reload")
	w("systemctl enable --now staging-fe-watchdog.timer")
	w("")
	w("# Health gate: the box should be serving SOMETHING (even a 401 from Amplify")
	w("# before Basic-Auth would be a config bug, but connection failures are not")
	w("# acceptable) before boot completes.")
	w("for i in 1 2 3 4 5 6 7 8 9 10; do")
	w("  code=$(curl -ks -o /dev/null -w '%%%%{http_code}' --max-time 5 https://127.0.0.1/ || echo 000)")
	w("  if [ \"$code\" != \"000\" ]; then")
	w("    echo \"staging-fe healthy (http_code=$code)\"; break")
	w("  fi")
	w("  sleep 2")
	w("done")

	return b.String(), nil
}
