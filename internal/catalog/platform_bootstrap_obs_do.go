package catalog

import (
	"fmt"
	"strings"
)

// platform_bootstrap_obs_do.go — pd-MIG-CUTOVER-F2-02 (obs, DigitalOcean).
//
// platform_asgs.go expresses the observability aggregator as a canonical
// `virtual-machine-scale-group` of 1. But a bare Ubuntu box is not the obs
// service: its substance is the hand-written AWS user_data
// (packages/observability/infra/terraform/user_data.sh) — pull the Go aggregator
// tarball from an artifact store, bootstrap /etc/observability.env, run it as a
// systemd unit, and front it with a self-signed nginx :443 (the dashboard is
// VPN-only). This file ports that bootstrap to DigitalOcean.
//
// Why a DO-SPECIFIC bootstrap (UserDataByProvider["digitalocean"]) rather than
// the provider-neutral UserData field: the AWS script is welded to AWS-only
// primitives that are meaningless on a DigitalOcean droplet —
//   - dnf/AL2023 package manager        -> Ubuntu apt.
//   - `aws s3 cp` from an S3 bucket via the instance role
//                                       -> `aws s3 cp` from DO Spaces
//                                          (s3://pyx-artifacts-fra1/beta/…,
//                                          S3-compatible, with an injected
//                                          --endpoint-url + Spaces keys, because
//                                          DO has no instance role).
//   - AWS Secrets Manager env bootstrap -> secrets injected at render as Terraform
//                                          variables (no instance role on DO).
//   - OBS_USE_AWS=1 + the CloudWatch poller
//                                       -> DROPPED entirely (there is no
//                                          CloudWatch on DigitalOcean). The
//                                          credential-free HTTP-service probes and
//                                          the mesh poller are kept.
//
// The rendered cloud-init is placed into the obs scale-group's
// AssembleScaleGroup.UserDataByProvider["digitalocean"], which the scale-group
// renderer descends to the droplet_autoscale user_data on a DO placement while
// AWS keeps its own (S3/Secrets-Manager/CloudWatch) bootstrap — one canonical
// component, two provider-specific boot scripts, no forked topology (SPEC §1).
//
// SECURITY: like platform_bootstrap_sso.go, NO secret VALUE is inlined. The
// Spaces access/secret keys (Secrets Manager beta-DigitalOceanSpacesKeys) are
// referenced by Terraform variable NAME; the operator wires those vars to the
// Secrets Manager source. The script never embeds a literal credential.
//
// BOOT-FETCH MIGRATION (EPIC-BOOTFETCH-AWS-SM-TO-VAULT, pd-MIG-CUTOVER-F2-02
// follow-up): the AWS user_data.sh fetches `beta/observability-env` from AWS
// Secrets Manager at every service (re)start (not just once at render time) via
// `aws secretsmanager get-secret-value` + jq, so a live secret rotation takes
// effect on the next restart without a redeploy. The DO port now mirrors that
// boot-time-fetch semantics against DO Vault instead of hardcoding the mesh
// secret as a render-time Terraform variable: the droplet has no instance role,
// so it logs into Vault via AppRole (VaultAddrVar/VaultRoleIDVar/
// VaultSecretIDVar — injected the same way SastDOBootstrapSpec injects
// RegistryTokenVar/APITokenVar) and reads the KV-v2 leaf
// secret/infra/<env>/observability/env, key `_json` — which holds the SAME
// JSON blob the AWS secret held — then extracts OBS_MESH_CLIENT_SECRET from it
// exactly like the AWS bootstrap's `jq -r '.OBS_MESH_CLIENT_SECRET // empty'`.
// See vault_bootfetch.go for the shared AppRole-login + KV-v2-read snippet.

// Pinned obs deployment constants — kept identical to the hand-written AWS
// user_data / deploy-observability.yml so the DO bootstrap is a faithful port,
// not a drift. The artifact lives in DO Spaces fra1 (the DO peer of the AWS
// pyxcloud-api-artifact S3 bucket).
const (
	// obsSpacesArtifactURL is the DO Spaces object holding the aggregator tarball
	// (the DO peer of s3://pyxcloud-api-artifact/beta/observability.tar.gz).
	obsSpacesArtifactURL = "s3://pyx-artifacts-fra1/beta/observability.tar.gz"
	// obsSpacesEndpoint is the S3-compatible DO Spaces endpoint for fra1.
	obsSpacesEndpoint = "https://fra1.digitaloceanspaces.com"
	// obsAppPort is the aggregator's HTTP listen port (matches the AWS user_data
	// app_port and the /healthz probe).
	obsAppPort = "8080"
	// obsHostname is the public (VPN-only) dashboard hostname; used for the
	// self-signed nginx cert CN/SAN. Identical to the AWS module.
	obsHostname = "observability.pyxcloud.io"
	// obsMeshMCPURL / obsMeshTokenURL / obsMeshClientID mirror the AWS user_data
	// mesh-poller wiring (agents card). The client SECRET is a Terraform variable.
	obsMeshMCPURL      = "https://mcp.passo.build/mcp"
	obsMeshTokenURL    = "https://beta-auth.pyxcloud.io/realms/passobuild/protocol/openid-connect/token"
	obsMeshClientID    = "mesh-agent"
	obsPollIntervalSec = "30"
)

// OBSDOBootstrapSpec is the typed input for the canonical observability
// aggregator bootstrap on DigitalOcean. Every secret is named by the Terraform
// variable that holds it (NOT the value) so nothing sensitive enters the abstract
// topology or Terraform state via this component.
type OBSDOBootstrapSpec struct {
	// SpacesAccessKeyVar / SpacesSecretKeyVar name the Terraform variables holding
	// the DO Spaces access/secret keys (Secrets Manager beta-DigitalOceanSpacesKeys)
	// used to `aws s3 cp` the artifact from Spaces. There is no instance role on DO,
	// so these are injected at render.
	SpacesAccessKeyVar string // default "do_spaces_access_key"
	SpacesSecretKeyVar string // default "do_spaces_secret_key"

	// --- Vault AppRole boot-fetch (EPIC-BOOTFETCH-AWS-SM-TO-VAULT) ---
	// The mesh poller's OIDC client secret is no longer a render-time Terraform
	// variable: it is fetched at BOOT time from DO Vault (mirroring the AWS
	// bootstrap's live `aws secretsmanager get-secret-value` fetch), so a secret
	// rotation takes effect on the next service restart without a redeploy.
	//
	// VaultAddrVar / VaultRoleIDVar / VaultSecretIDVar name the Terraform
	// variables holding the Vault address, and the `observability-boot` AppRole
	// role_id/secret_id (never the values). Defaults match the platform
	// convention: "vault_addr" / "obs_vault_role_id" / "obs_vault_secret_id".
	VaultAddrVar     string
	VaultRoleIDVar   string
	VaultSecretIDVar string
	// VaultKVPath is the KV-v2 leaf (under the `secret` mount, no `data/` infix)
	// holding the obs env secrets. Default "infra/staging/observability/env".
	VaultKVPath string
	// VaultJSONKey is the key inside that leaf carrying the full JSON blob (the
	// same shape AWS Secrets Manager beta/observability-env held), from which
	// OBS_MESH_CLIENT_SECRET is extracted downstream exactly like the AWS
	// bootstrap's `jq -r '.OBS_MESH_CLIENT_SECRET // empty'`. Default "_json".
	VaultJSONKey string
}

// withDefaults fills the production-faithful defaults for any unset variable-name
// field so callers can pass an empty spec and still get the canonical wiring.
func (s OBSDOBootstrapSpec) withDefaults() OBSDOBootstrapSpec {
	def := func(v, d string) string {
		if strings.TrimSpace(v) == "" {
			return d
		}
		return v
	}
	s.SpacesAccessKeyVar = def(s.SpacesAccessKeyVar, "do_spaces_access_key")
	s.SpacesSecretKeyVar = def(s.SpacesSecretKeyVar, "do_spaces_secret_key")
	s.VaultAddrVar = def(s.VaultAddrVar, "vault_addr")
	s.VaultRoleIDVar = def(s.VaultRoleIDVar, "obs_vault_role_id")
	s.VaultSecretIDVar = def(s.VaultSecretIDVar, "obs_vault_secret_id")
	s.VaultKVPath = def(s.VaultKVPath, "infra/staging/observability/env")
	s.VaultJSONKey = def(s.VaultJSONKey, "_json")
	return s
}

// OBSDOBootstrapVariableNames returns, in deterministic order, the Terraform
// variable names this bootstrap references, split into plain and sensitive. The
// assembler/CLI uses it to emit the matching `variable "<x>" {}` declarations
// (the secret ones marked sensitive) so the rendered .tf is self-contained.
func (s OBSDOBootstrapSpec) OBSDOBootstrapVariableNames() (plain []string, sensitive []string) {
	s = s.withDefaults()
	// The Vault address is a URL, not a credential -> plain. The Spaces keys and
	// the AppRole role_id/secret_id are credentials -> sensitive.
	plain = []string{s.VaultAddrVar}
	sensitive = []string{
		s.SpacesAccessKeyVar, s.SpacesSecretKeyVar,
		s.VaultRoleIDVar, s.VaultSecretIDVar,
	}
	return plain, sensitive
}

// RenderOBSDOBootstrapUserData renders the canonical observability aggregator
// cloud-init for DigitalOcean as a bash script with `${var.<x>}` placeholders for
// the injected secrets. It is a faithful, Ubuntu-ified port of the AWS
// packages/observability/infra/terraform/user_data.sh with the AWS-only pieces
// swapped: apt instead of dnf, DO Spaces (S3-compatible, injected keys) instead
// of an S3 bucket + instance role, secrets injected at render instead of Secrets
// Manager, and OBS_USE_AWS/the CloudWatch poller DROPPED (meaningless on DO). The
// HTTP-probe + mesh pollers, the systemd unit, the self-signed nginx :443, and
// the /healthz probe on :8080 all match the AWS module. The returned string is
// meant to be placed into AssembleScaleGroup.UserDataByProvider["digitalocean"]
// for the `obs` service.
func RenderOBSDOBootstrapUserData(spec OBSDOBootstrapSpec) (string, error) {
	s := spec.withDefaults()
	v := func(name string) string { return "${var." + name + "}" }

	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }

	w("#!/usr/bin/env bash")
	w("set -euo pipefail")
	w("export HOME=/root")
	w("# Canonical observability aggregator bootstrap for DigitalOcean —")
	w("# Ubuntu-ified port of packages/observability/infra/terraform/user_data.sh")
	w("# (pd-MIG-CUTOVER-F2-02). Provider-neutral secrets are Terraform variables,")
	w("# never inlined. The AWS CloudWatch poller is dropped (no CloudWatch on DO).")
	w("")
	w("# Ubuntu: apt (not dnf/AL2023). Tools for pull + nginx TLS + env bootstrap.")
	w("# NOTE: the `awscli` apt package has NO installation candidate on Ubuntu 24.04")
	w("# ('no installation candidate') -> the boot aborted before the service started.")
	w("# Install the official AWS CLI v2 from the zip (same approach as mcp/backend for")
	w("# the S3-compatible DO Spaces fetch) instead of the apt package.")
	w("export DEBIAN_FRONTEND=noninteractive")
	w("apt-get update -y")
	w("# python3 (not jq) parses the Vault AppRole login/read JSON responses below —")
	w("# ships on the Ubuntu droplet image, no extra apt package needed for jq.")
	w("apt-get install -y curl python3 tar nginx openssl unzip")
	w("curl -s \"https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip\" -o \"awscliv2.zip\"")
	w("unzip -q awscliv2.zip && ./aws/install")
	w("# `aws` now resolves to /usr/local/bin/aws (v2). Make the rest of the pull use it.")
	w("")
	w("mkdir -p /opt/observability")
	w("cd /opt/observability")
	w("")
	w("# Pull the aggregator binary from DO Spaces (S3-compatible). No instance role")
	w("# on DO, so the Spaces keys (Secrets Manager beta-DigitalOceanSpacesKeys) are")
	w("# injected at render and exported for the aws CLI with the Spaces endpoint.")
	w("export AWS_ACCESS_KEY_ID='%s'", v(s.SpacesAccessKeyVar))
	w("export AWS_SECRET_ACCESS_KEY='%s'", v(s.SpacesSecretKeyVar))
	w("# Retry the pull a few times (transient Spaces/network hiccups) so `set -e`")
	w("# doesn't abort the whole boot before the service is even installed.")
	w("for attempt in 1 2 3 4 5; do")
	w("  if /usr/local/bin/aws s3 cp \"%s\" /tmp/obs.tar.gz --endpoint-url %s --cli-read-timeout 0 --cli-connect-timeout 30; then", obsSpacesArtifactURL, obsSpacesEndpoint)
	w("    break")
	w("  fi")
	w("  echo \"obs artifact pull failed (attempt $attempt); retrying...\" >&2; sleep $((attempt*5))")
	w("done")
	w("test -s /tmp/obs.tar.gz")
	w("tar -xzf /tmp/obs.tar.gz -C /opt/observability")
	w("chmod +x /opt/observability/aggregator")
	w("")
	w("# Env bootstrap: BOOT-TIME fetch from DO Vault (EPIC-BOOTFETCH-AWS-SM-TO-VAULT),")
	w("# mirroring the AWS module's live `aws secretsmanager get-secret-value` fetch —")
	w("# a secret rotation in Vault takes effect on the next restart, no redeploy.")
	w("# Only the credential-free HTTP-service probes and the mesh poller are")
	w("# enabled — the AWS CloudWatch poller is DROPPED on DigitalOcean.")
	w("cat >/etc/observability.env.tmp <<EOF_BASE")
	w("OBS_LISTEN_ADDR=:%s", obsAppPort)
	w("OBS_POLL_INTERVAL_SEC=%s", obsPollIntervalSec)
	w("EOF_BASE")
	w("")
	w("# Fetch the full observability-env JSON blob from Vault (KV-v2 leaf, key")
	w("# `%s`) via AppRole login — the DO peer of `aws secretsmanager get-secret-value")
	w("# --secret-id beta/observability-env`. Populates $OBS_ENV_JSON.", s.VaultJSONKey)
	w("%s", VaultBootFetchSnippet(s.VaultAddrVar, s.VaultRoleIDVar, s.VaultSecretIDVar, s.VaultKVPath, s.VaultJSONKey, "OBS_ENV_JSON"))
	w("")
	w("# Mesh poller (agents card) — enabled when the mesh client secret is present.")
	w("# Extracted from the fetched JSON exactly like the AWS bootstrap's")
	w("# `jq -r '.OBS_MESH_CLIENT_SECRET // empty'`.")
	w("OBS_MESH_CLIENT_SECRET=$(printf '%%s' \"$OBS_ENV_JSON\" | python3 -c 'import json,sys; print(json.load(sys.stdin).get(\"OBS_MESH_CLIENT_SECRET\",\"\"))' 2>/dev/null || true)")
	w("unset OBS_ENV_JSON")
	w("if [ -n \"$OBS_MESH_CLIENT_SECRET\" ]; then")
	w("  cat >>/etc/observability.env.tmp <<EOF_MESH")
	w("OBS_MESH_MCP_URL=%s", obsMeshMCPURL)
	w("OBS_MESH_TOKEN_URL=%s", obsMeshTokenURL)
	w("OBS_MESH_CLIENT_ID=%s", obsMeshClientID)
	w("OBS_MESH_CLIENT_SECRET=$OBS_MESH_CLIENT_SECRET")
	w("EOF_MESH")
	w("fi")
	w("chmod 600 /etc/observability.env.tmp")
	w("chown root:root /etc/observability.env.tmp")
	w("mv /etc/observability.env.tmp /etc/observability.env")
	w("")
	w("# systemd unit for the aggregator (matches the AWS module).")
	w("cat >/etc/systemd/system/observability.service <<UNIT")
	w("[Unit]")
	w("Description=PyxCloud observability aggregator")
	w("After=network-online.target")
	w("Wants=network-online.target")
	w("")
	w("[Service]")
	w("Type=simple")
	w("WorkingDirectory=/opt/observability")
	w("EnvironmentFile=-/etc/observability.env")
	w("ExecStart=/opt/observability/aggregator")
	w("Restart=always")
	w("RestartSec=5")
	w("StandardOutput=journal")
	w("StandardError=journal")
	w("")
	w("[Install]")
	w("WantedBy=multi-user.target")
	w("UNIT")
	w("")
	w("systemctl daemon-reload")
	w("systemctl enable --now observability.service || true")
	w("")
	w("# HTTPS on :443 via nginx (dashboard reachable as https://%s over the VPN,", obsHostname)
	w("# no port). Self-signed cert; VPN-only (DO firewall is per-service), so a")
	w("# trusted-once cert is acceptable — same posture as the AWS module.")
	w("mkdir -p /etc/nginx/certs")
	w("if [ ! -f /etc/nginx/certs/obs.crt ]; then")
	w("  openssl req -x509 -nodes -newkey rsa:2048 -days 3650 \\")
	w("    -keyout /etc/nginx/certs/obs.key -out /etc/nginx/certs/obs.crt \\")
	w("    -subj \"/CN=%s\" -addext \"subjectAltName=DNS:%s\"", obsHostname, obsHostname)
	w("fi")
	w("cat >/etc/nginx/conf.d/observability.conf <<NGINX")
	w("server {")
	w("  listen 443 ssl;")
	w("  server_name %s;", obsHostname)
	w("  ssl_certificate /etc/nginx/certs/obs.crt;")
	w("  ssl_certificate_key /etc/nginx/certs/obs.key;")
	w("  location / {")
	w("    proxy_pass http://127.0.0.1:%s;", obsAppPort)
	w("    proxy_set_header Host \\$host;")
	w("    proxy_set_header X-Forwarded-For \\$remote_addr;")
	w("  }")
	w("}")
	w("NGINX")
	w("systemctl enable --now nginx")
	w("systemctl restart nginx")
	w("")
	w("# Health gate: /healthz on :%s (matches the AWS module).", obsAppPort)
	w("for i in 1 2 3 4 5 6 7 8 9 10; do")
	w("  if curl -fsS \"http://127.0.0.1:%s/healthz\" >/dev/null 2>&1; then", obsAppPort)
	w("    echo \"healthy\"; break")
	w("  fi")
	w("  sleep 2")
	w("done")

	return b.String(), nil
}
