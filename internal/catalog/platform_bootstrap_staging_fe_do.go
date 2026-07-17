package catalog

import (
	"fmt"
	"strings"
)

const (
	stagingFEPublicHostname   = "staging.passo.build"
	stagingFEConsoleHostname  = "staging-console.pyxcloud.io"
	stagingFEAppPort          = 3000
	stagingFEArtifactInbox    = "/var/lib/staging-fe/inbox/standalone.tar.gz"
	stagingFERuntimeVaultPath = "infra/staging/staging-fe/runtime"
)

// StagingFEDOBootstrapSpec contains only non-secret bootstrap coordinates.
// The artifact is delivered to the inbox out-of-band and a response-wrapped,
// one-use AppRole SecretID is delivered on converge's stdin.
type StagingFEDOBootstrapSpec struct {
	ArtifactKeyVar    string
	ArtifactSHA256Var string
	VaultAddrVar      string
	VaultRoleIDVar    string
	VaultKVPath       string
	VaultEnvKey       string
}

func (s StagingFEDOBootstrapSpec) withDefaults() StagingFEDOBootstrapSpec {
	def := func(value, fallback string) string {
		if strings.TrimSpace(value) == "" {
			return fallback
		}
		return strings.TrimSpace(value)
	}
	s.ArtifactKeyVar = def(s.ArtifactKeyVar, "staging_fe_artifact_key")
	s.ArtifactSHA256Var = def(s.ArtifactSHA256Var, "staging_fe_artifact_sha256")
	s.VaultAddrVar = def(s.VaultAddrVar, "vault_addr")
	s.VaultRoleIDVar = def(s.VaultRoleIDVar, "staging_fe_vault_role_id")
	s.VaultKVPath = strings.Trim(def(s.VaultKVPath, stagingFERuntimeVaultPath), "/")
	s.VaultEnvKey = def(s.VaultEnvKey, "_env")
	return s
}

func (s StagingFEDOBootstrapSpec) StagingFEDOBootstrapVariableNames() (plain, sensitive []string) {
	s = s.withDefaults()
	return []string{s.ArtifactKeyVar, s.ArtifactSHA256Var, s.VaultAddrVar, s.VaultRoleIDVar}, nil
}

// RenderStagingFEDOBootstrapUserData installs a fail-closed origin. Cloud-init
// never receives a credential and deliberately leaves nginx/Next stopped. The
// out-of-band controller copies the pinned artifact into the inbox, then pipes
// a Vault wrapping token to staging-fe-converge over an authenticated channel.
func RenderStagingFEDOBootstrapUserData(spec StagingFEDOBootstrapSpec) (string, error) {
	s := spec.withDefaults()
	tlsBootstrap, err := RenderStagingTLSBootstrap(StagingTLSBootstrapSpec{
		Domains: []string{stagingFEPublicHostname, stagingFEConsoleHostname},
	})
	if err != nil {
		return "", fmt.Errorf("render staging FE TLS: %w", err)
	}
	v := func(name string) string { return "${var." + name + "}" }

	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	w("#!/usr/bin/env bash")
	w("set -euo pipefail")
	w("export DEBIAN_FRONTEND=noninteractive")
	w("printf '#!/bin/sh\\nexit 101\\n' >/usr/sbin/policy-rc.d")
	w("chmod 0755 /usr/sbin/policy-rc.d")
	w("apt-get update -y")
	w("apt-get install -y --no-install-recommends ca-certificates curl nginx nodejs openssl python3 tar")
	w("rm -f /usr/sbin/policy-rc.d")
	w("install -d -m 0700 /run/staging-fe")
	w("install -d -m 0700 /var/lib/staging-fe/inbox")
	w("install -d -m 0755 /opt/staging-fe/releases")
	w("")
	w("cat >/etc/systemd/system/staging-fe.service <<'UNIT'")
	w("[Unit]")
	w("Description=passo.build staging Next.js standalone runtime")
	w("After=network-online.target")
	w("Wants=network-online.target")
	w("[Service]")
	w("Type=simple")
	w("User=www-data")
	w("Group=www-data")
	w("WorkingDirectory=/opt/staging-fe/current")
	w("EnvironmentFile=/etc/staging-fe.env")
	w("ExecStart=/usr/bin/node server.js")
	w("Restart=always")
	w("RestartSec=5")
	w("NoNewPrivileges=true")
	w("PrivateTmp=true")
	w("[Install]")
	w("WantedBy=multi-user.target")
	w("UNIT")
	w("")
	w("%s", tlsBootstrap)
	w("cat >/etc/nginx/conf.d/staging-fe.conf <<'NGINX'")
	w("server {")
	w("  listen 443 ssl;")
	w("  server_name %s %s;", stagingFEPublicHostname, stagingFEConsoleHostname)
	w("  ssl_certificate /etc/letsencrypt/live/%s/fullchain.pem;", stagingFEPublicHostname)
	w("  ssl_certificate_key /etc/letsencrypt/live/%s/privkey.pem;", stagingFEPublicHostname)
	w("  proxy_buffer_size 16k;")
	w("  proxy_buffers 8 16k;")
	w("  client_max_body_size 25m;")
	w("  location / {")
	w("    proxy_pass http://127.0.0.1:%d;", stagingFEAppPort)
	w("    proxy_http_version 1.1;")
	w("    proxy_set_header Host $host;")
	w("    proxy_set_header X-Real-IP $remote_addr;")
	w("    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;")
	w("    proxy_set_header X-Forwarded-Proto https;")
	w("    proxy_set_header X-Forwarded-Host $host;")
	w("  }")
	w("}")
	w("NGINX")
	w("rm -f /etc/nginx/sites-enabled/default")
	w("")
	w("cat >/usr/local/sbin/staging-fe-converge <<'CONVERGE'")
	w("#!/usr/bin/env bash")
	w("set -euo pipefail")
	w("umask 077")
	w("ARTIFACT=%s", stagingFEArtifactInbox)
	w("ARTIFACT_KEY='%s'", v(s.ArtifactKeyVar))
	w("EXPECTED_SHA256='%s'", v(s.ArtifactSHA256Var))
	w("VAULT_ADDR='%s'", v(s.VaultAddrVar))
	w("ROLE_ID='%s'", v(s.VaultRoleIDVar))
	w("cleanup() {")
	w("  shred -u /run/staging-fe/wrap-curl.conf /run/staging-fe/vault-curl.conf 2>/dev/null || true")
	w("  unset WRAPPED_TOKEN SECRET_ID VAULT_TOKEN UNWRAP_RESP LOGIN_RESP RUNTIME_RESP STAGING_FE_ENV")
	w("}")
	w("trap cleanup EXIT INT TERM")
	w("test \"$#\" -eq 0 || { echo 'staging-fe: wrapping token is accepted on stdin only' >&2; exit 64; }")
	w("IFS= read -r WRAPPED_TOKEN || test -n \"${WRAPPED_TOKEN:-}\"")
	w("test -n \"$WRAPPED_TOKEN\" || { echo 'staging-fe: empty wrapping token' >&2; exit 1; }")
	w("test -f \"$ARTIFACT\" && test ! -L \"$ARTIFACT\" || { echo 'staging-fe: delivered artifact missing or unsafe' >&2; exit 1; }")
	w("printf '%%s' \"$ARTIFACT_KEY\" | grep -Eq '^vibe-frontend/[0-9a-f]{40}/standalone.tar.gz$' || { echo 'staging-fe: invalid artifact key' >&2; exit 1; }")
	w("printf '%%s' \"$EXPECTED_SHA256\" | grep -Eq '^[0-9a-f]{64}$' || { echo 'staging-fe: invalid artifact SHA-256' >&2; exit 1; }")
	w("printf '%%s  %%s\\n' \"$EXPECTED_SHA256\" \"$ARTIFACT\" | sha256sum --check --status - || { echo 'staging-fe: artifact digest mismatch' >&2; exit 1; }")
	w("if tar -tzf \"$ARTIFACT\" | grep -Eq '(^/|(^|/)\\.\\.(/|$))'; then echo 'staging-fe: unsafe archive path' >&2; exit 1; fi")
	w("install -d -m 0700 /run/staging-fe")
	w("printf 'header = \"X-Vault-Token: %%s\"\\n' \"$WRAPPED_TOKEN\" >/run/staging-fe/wrap-curl.conf")
	w("chmod 0600 /run/staging-fe/wrap-curl.conf")
	w("UNWRAP_RESP=$(curl -fsS -X POST --config /run/staging-fe/wrap-curl.conf \"$VAULT_ADDR/v1/sys/wrapping/unwrap\")")
	w("SECRET_ID=$(printf '%%s' \"$UNWRAP_RESP\" | python3 -c 'import json,sys; print(json.load(sys.stdin).get(\"data\",{}).get(\"secret_id\",\"\"))')")
	w("test -n \"$SECRET_ID\" || { echo 'staging-fe: unwrap returned no SecretID' >&2; exit 1; }")
	w("LOGIN_RESP=$(ROLE_ID=\"$ROLE_ID\" SECRET_ID=\"$SECRET_ID\" python3 -c 'import json,os; print(json.dumps({\"role_id\":os.environ[\"ROLE_ID\"],\"secret_id\":os.environ[\"SECRET_ID\"]}))' | curl -fsS -X POST --data-binary @- \"$VAULT_ADDR/v1/auth/approle/login\")")
	w("unset SECRET_ID WRAPPED_TOKEN UNWRAP_RESP")
	w("VAULT_TOKEN=$(printf '%%s' \"$LOGIN_RESP\" | python3 -c 'import json,sys; print(json.load(sys.stdin).get(\"auth\",{}).get(\"client_token\",\"\"))')")
	w("test -n \"$VAULT_TOKEN\" || { echo 'staging-fe: AppRole login returned no token' >&2; exit 1; }")
	w("printf 'header = \"X-Vault-Token: %%s\"\\n' \"$VAULT_TOKEN\" >/run/staging-fe/vault-curl.conf")
	w("chmod 0600 /run/staging-fe/vault-curl.conf")
	w("RUNTIME_RESP=$(curl -fsS --config /run/staging-fe/vault-curl.conf \"$VAULT_ADDR/v1/secret/data/%s\")", s.VaultKVPath)
	w("STAGING_FE_ENV=$(printf '%%s' \"$RUNTIME_RESP\" | python3 -c 'import json,sys; print(json.load(sys.stdin).get(\"data\",{}).get(\"data\",{}).get(\"%s\",\"\"))')", s.VaultEnvKey)
	w("test -n \"$STAGING_FE_ENV\" || { echo 'staging-fe: runtime env missing' >&2; exit 1; }")
	w("printf '%%s\\n' \"$STAGING_FE_ENV\" | awk 'NF && $0 !~ /^#/ && $0 !~ /^[A-Za-z_][A-Za-z0-9_]*=/ { exit 1 }' || { echo 'staging-fe: invalid runtime env format' >&2; exit 1; }")
	w("install -m 0600 /dev/null /etc/staging-fe.env")
	w("printf '%%s\\n' \"$STAGING_FE_ENV\" >/etc/staging-fe.env")
	w("cat >>/etc/staging-fe.env <<'ENVEOF'")
	w("NODE_ENV=production")
	w("HOSTNAME=127.0.0.1")
	w("PORT=%d", stagingFEAppPort)
	w("NEXT_PUBLIC_APP_URL=https://%s", stagingFEPublicHostname)
	w("NEXT_PUBLIC_API_URL=https://staging-api.pyxcloud.io")
	w("NEXT_PUBLIC_KEYCLOAK_ISSUER=https://staging-auth.pyxcloud.io/realms/passobuild")
	w("INTERNAL_AUTH_URL=https://staging-auth.pyxcloud.io/realms/passobuild/")
	w("KEYCLOAK_ISSUER=https://staging-auth.pyxcloud.io/realms/passobuild")
	w("PYX_MESH_KEYCLOAK_ISSUER=https://staging-auth.pyxcloud.io/realms/passobuild")
	w("PYX_MCP_URL=https://staging-mcp.passo.build/mcp")
	w("ENVEOF")
	w("chmod 0600 /etc/staging-fe.env")
	w("ARTIFACT_SHA=$(printf '%%s' \"$ARTIFACT_KEY\" | cut -d/ -f2)")
	w("RELEASE_DIR=\"/opt/staging-fe/releases/$ARTIFACT_SHA\"")
	w("rm -rf \"$RELEASE_DIR.new\"")
	w("install -d -m 0755 \"$RELEASE_DIR.new\"")
	w("tar --no-same-owner --no-same-permissions -xzf \"$ARTIFACT\" -C \"$RELEASE_DIR.new\"")
	w("test -f \"$RELEASE_DIR.new/server.js\" || { echo 'staging-fe: standalone server.js missing' >&2; exit 1; }")
	w("chown -R root:root \"$RELEASE_DIR.new\"")
	w("chmod -R a+rX \"$RELEASE_DIR.new\"")
	w("rm -rf \"$RELEASE_DIR\"")
	w("mv \"$RELEASE_DIR.new\" \"$RELEASE_DIR\"")
	w("ln -sfn \"$RELEASE_DIR\" /opt/staging-fe/current")
	w("export VAULT_ADDR VAULT_TOKEN")
	w("/usr/local/sbin/staging-tls-issue")
	w("unset VAULT_TOKEN")
	w("nginx -t")
	w("systemctl daemon-reload")
	w("systemctl enable staging-fe.service nginx")
	w("systemctl restart staging-fe.service")
	w("systemctl restart nginx")
	w("curl -fsS http://127.0.0.1:%d/ >/dev/null", stagingFEAppPort)
	w("rm -f %s", stagingFEArtifactInbox)
	w("CONVERGE")
	w("chmod 0700 /usr/local/sbin/staging-fe-converge")
	w("systemctl daemon-reload")
	w("systemctl disable staging-fe.service nginx")
	w("systemctl stop staging-fe.service nginx")
	w("echo 'staging-fe bootstrap installed; origin remains stopped until out-of-band convergence' | systemd-cat -t staging-fe-bootstrap")

	return b.String(), nil
}
