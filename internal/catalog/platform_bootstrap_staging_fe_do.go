package catalog

import (
	"fmt"
	"strings"
)

const (
	stagingFEPublicHostname  = "staging.passo.build"
	stagingFEConsoleHostname = "staging-console.pyxcloud.io"
	stagingFEAppPort         = 3000
)

// StagingFEDOBootstrapSpec describes the private staging Next.js origin. The
// build is produced elsewhere with `output: standalone`; this bootstrap only
// installs and runs a content-addressed artifact, never builds source on-box.
type StagingFEDOBootstrapSpec struct {
	SpacesAccessKeyVar string
	SpacesSecretKeyVar string
	ArtifactKeyVar     string
	VaultAddrVar       string
	VaultRoleIDVar     string
	VaultSecretIDVar   string
	VaultKVPath        string
	VaultEnvKey        string
}

func (s StagingFEDOBootstrapSpec) withDefaults() StagingFEDOBootstrapSpec {
	def := func(value, fallback string) string {
		if strings.TrimSpace(value) == "" {
			return fallback
		}
		return strings.TrimSpace(value)
	}
	s.SpacesAccessKeyVar = def(s.SpacesAccessKeyVar, "do_spaces_access_key")
	s.SpacesSecretKeyVar = def(s.SpacesSecretKeyVar, "do_spaces_secret_key")
	s.ArtifactKeyVar = def(s.ArtifactKeyVar, "staging_fe_artifact_key")
	s.VaultAddrVar = def(s.VaultAddrVar, "vault_addr")
	s.VaultRoleIDVar = def(s.VaultRoleIDVar, "staging_fe_vault_role_id")
	s.VaultSecretIDVar = def(s.VaultSecretIDVar, "staging_fe_vault_secret_id")
	s.VaultKVPath = def(s.VaultKVPath, "infra/staging/staging-fe/runtime")
	s.VaultEnvKey = def(s.VaultEnvKey, "_env")
	return s
}

func (s StagingFEDOBootstrapSpec) StagingFEDOBootstrapVariableNames() (plain, sensitive []string) {
	s = s.withDefaults()
	plain = []string{s.ArtifactKeyVar, s.VaultAddrVar}
	sensitive = []string{s.SpacesAccessKeyVar, s.SpacesSecretKeyVar, s.VaultRoleIDVar, s.VaultSecretIDVar}
	return plain, sensitive
}

// RenderStagingFEDOBootstrapUserData renders a restart-safe private origin for
// the full Next.js server runtime. Server routes such as the OIDC callback and
// backend/MCP proxies execute here inside the VPC rather than on Amplify.
func RenderStagingFEDOBootstrapUserData(spec StagingFEDOBootstrapSpec) (string, error) {
	s := spec.withDefaults()
	tlsBootstrap, err := RenderStagingTLSBootstrap(StagingTLSBootstrapSpec{
		Domains:          []string{stagingFEPublicHostname, stagingFEConsoleHostname},
		VaultAddrVar:     s.VaultAddrVar,
		VaultRoleIDVar:   s.VaultRoleIDVar,
		VaultSecretIDVar: s.VaultSecretIDVar,
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
	w("apt-get update -y")
	w("apt-get install -y --no-install-recommends awscli ca-certificates curl nginx nodejs openssl python3 tar")
	w("")
	w("STAGING_FE_ARTIFACT_KEY='%s'", v(s.ArtifactKeyVar))
	w("if ! printf '%%s' \"$STAGING_FE_ARTIFACT_KEY\" | grep -Eq '^vibe-frontend/[0-9a-f]{40}/standalone.tar.gz$'; then")
	w("  echo \"staging-fe: artifact key must match vibe-frontend/[0-9a-f]{40}/standalone.tar.gz\" >&2")
	w("  exit 1")
	w("fi")
	w("ARTIFACT_SHA=$(printf '%%s' \"$STAGING_FE_ARTIFACT_KEY\" | cut -d/ -f2)")
	w("RELEASE_DIR=\"/opt/staging-fe/releases/$ARTIFACT_SHA\"")
	w("install -d -m 0755 /opt/staging-fe/releases")
	w("if [ ! -f \"$RELEASE_DIR/server.js\" ]; then")
	w("  rm -rf \"$RELEASE_DIR\"")
	w("  install -d -m 0755 \"$RELEASE_DIR\"")
	w("  AWS_ACCESS_KEY_ID='%s' AWS_SECRET_ACCESS_KEY='%s' \\", v(s.SpacesAccessKeyVar), v(s.SpacesSecretKeyVar))
	w("    aws s3 cp \"s3://%s/$STAGING_FE_ARTIFACT_KEY\" /tmp/staging-fe.tar.gz \\", doBaselineSpacesBucket)
	w("      --endpoint-url https://fra1.digitaloceanspaces.com --no-progress")
	w("  tar -xzf /tmp/staging-fe.tar.gz -C \"$RELEASE_DIR\"")
	w("  rm -f /tmp/staging-fe.tar.gz")
	w("fi")
	w("test -f \"$RELEASE_DIR/server.js\" || { echo 'staging-fe: standalone server.js missing from artifact' >&2; exit 1; }")
	w("chown -R root:root \"$RELEASE_DIR\"")
	w("chmod -R a+rX \"$RELEASE_DIR\"")
	w("ln -sfn \"$RELEASE_DIR\" /opt/staging-fe/current")
	w("test -f /opt/staging-fe/current/server.js")
	w("")
	w("%s", VaultBootFetchSnippet(s.VaultAddrVar, s.VaultRoleIDVar, s.VaultSecretIDVar, s.VaultKVPath, s.VaultEnvKey, "STAGING_FE_ENV"))
	w("install -m 0600 /dev/null /etc/staging-fe.env")
	w("printf '%%s\\n' \"$STAGING_FE_ENV\" > /etc/staging-fe.env")
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
	w("unset STAGING_FE_ENV")
	w("")
	w("cat >/etc/systemd/system/staging-fe.service <<'UNIT'")
	w("[Unit]")
	w("Description=passo.build staging Next.js standalone runtime")
	w("After=network-online.target")
	w("Wants=network-online.target")
	w("")
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
	w("")
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
	w("nginx -t")
	w("systemctl daemon-reload")
	w("systemctl enable --now staging-fe.service nginx")
	w("for attempt in 1 2 3 4 5 6 7 8 9 10; do")
	w("  curl -fsS http://127.0.0.1:%d/ >/dev/null && exit 0", stagingFEAppPort)
	w("  sleep 3")
	w("done")
	w("echo 'staging-fe: standalone runtime failed health gate' >&2")
	w("exit 1")

	return b.String(), nil
}
