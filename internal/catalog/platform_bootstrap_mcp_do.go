package catalog

import (
	"fmt"
	"strconv"
	"strings"
)

// platform_bootstrap_mcp_do.go — pd-MIG-CUTOVER-F2-02 (EPIC-AWS-TO-DO-MIGRATION).
//
// platform_asgs.go already expresses the `mcp` platform service (the board-OS MCP
// server, Go, repo skill-plugin/mcp-go) in the canonical vocabulary as a
// `virtual-machine-scale-group` of 1. But a bare Ubuntu box is NOT the MCP
// server: the substance is its bootstrap — pull the versioned Go binary tarball
// from DO Spaces, write /etc/passobuild-mcp.env (board DB URL, embed-token
// secret, decomposition thresholds, admin roles), install a hardened systemd unit
// and listen on :8787.
//
// This file ports the LIVE mcp bootstrap (the one actually applied to the running
// beta-passobuild-mcp droplet) into the catalog as the DigitalOcean-specific
// override — RenderMcpDOUserData — and it is wired as the mcp scale-group's
// UserDataByProvider["digitalocean"] via PlatformScaleGroupComponentsWithDOBootstraps
// (platform_asgs.go). It is the SIXTH service of the F2-02 batch and the one that
// was previously MISSING from the catalog: the live droplet was applied by hand,
// so the catalog and reality had drifted. Adding it here makes the live mcp
// reproducible from the abstract topology and CLOSES that drift.
//
// Faithful-to-live decisions (documented so the diff is auditable):
//
//   - Binary source: DO Spaces s3://pyx-artifacts-fra1/beta/mcp.tar.gz, fetched
//     with the S3-compatible AWS CLI pointed at the fra1 Spaces endpoint. There is
//     NO instance role on DO, so the Spaces access key/secret are injected at
//     render from Secrets Manager beta-DigitalOceanSpacesKeys as Terraform
//     variables (never inlined).
//
//   - Board database: BOARD_DATABASE_URL comes from the DO Managed Postgres
//     pyx-main-db, database mesh_app, taken from Secrets Manager
//     beta-DO-pyx-main-db-url (the SAME managed PG + db the backend uses) as a
//     single Terraform variable.
//
//   - EMBED_TOKEN_SECRET is injected at render (Terraform variable) — the shared
//     secret the board OS uses to sign/verify the embedded-widget SSO tokens.
//
//   - Board control knobs are pinned to the LIVE values:
//     BOARD_DECOMPOSE_MIN_COMPLEXITY=6, BOARD_VERIFY_MIN_COMPLEXITY=9,
//     BOARD_OPTIMIZE_MIN_COMPLEXITY=6, BOARD_ADMIN_ROLES=board-admin.
//
//   - Listens on :8787 (behind the shared ALB, matching the live droplet). NO
//     CloudWatch agent is installed (there is no instance role / CloudWatch on DO;
//     observability is the separate LGTM stack).
//
// SECURITY: like the other bootstraps, NO secret VALUE is inlined. Every
// credential (the Spaces keys, the board DB URL, the embed-token secret) is
// referenced by Terraform variable name; the operator wires those vars to the
// same Secrets Manager source. The script never embeds a literal credential.
//
// VAULT MIGRATION (EPIC-BOOTFETCH-AWS-SM-TO-VAULT, wave 2): the Spaces keys and
// EMBED_TOKEN_SECRET are now resolved by Terraform at APPLY time directly from
// Vault via `data "vault_kv_secret_v2"` (see vault_datasource.go) —
// secret/infra/<env>/do/spaces-keys (access_key/secret_key) and
// secret/infra/<env>/mcp (key embed_token) — instead of an operator-populated
// ${var.<x>} sourced from a Secrets Manager export. The board DB URL
// (BoardDBURLVar) has no Vault leaf yet, so it stays a ${var.<x>} render input
// for now (unmigrated; tracked separately).

// Pinned artifact coordinates for the board-OS MCP binary on DO Spaces — one
// place so a cutover version/key change is a single edit.
const (
	// mcpSpacesBucket is the DO Spaces bucket (fra1) holding the mcp tarball.
	mcpSpacesBucket = "pyx-artifacts-fra1"
	// mcpSpacesKey is the tarball key under the bucket (beta/mcp.tar.gz).
	mcpSpacesKey = "beta/mcp.tar.gz"
	// mcpBinaryPath is the ACTUAL server binary path after `tar -xzf mcp.tar.gz -C
	// /opt/passobuild-mcp`. The tarball layout is `mcp/passobuild-mcp` (verified
	// against beta/mcp.tar.gz), so the binary lands at
	// /opt/passobuild-mcp/mcp/passobuild-mcp. systemd ExecStart MUST point here —
	// the earlier bootstrap pointed at /opt/passobuild-mcp/mcp (the DIRECTORY),
	// causing a 203/EXEC restart loop.
	mcpBinaryPath = "/opt/passobuild-mcp/mcp/passobuild-mcp"
	// mcpSpacesEndpoint is the S3-compatible fra1 Spaces endpoint the AWS CLI is
	// pointed at (no instance role; the CLI uses the injected Spaces keys).
	mcpSpacesEndpoint = "https://fra1.digitaloceanspaces.com"
	// mcpSpacesRegion is the region token the S3-compatible client expects for fra1.
	mcpSpacesRegion = "fra1"
	// mcpMainDBDatabase documents the target DO Managed Postgres database (mesh_app)
	// the board DB URL var (beta-DO-pyx-main-db-url) already encodes; kept so the
	// comment names the same value as the backend.
	mcpMainDBDatabase = "mesh_app"
	// mcpListenPort is the port the board-OS MCP server listens on (behind the ALB),
	// matching the live droplet.
	mcpListenPort = 8787
	// The board decomposition/verify/optimize complexity thresholds and admin roles,
	// pinned to the live droplet's values.
	mcpDecomposeMinComplexity = 6
	mcpVerifyMinComplexity    = 9
	mcpOptimizeMinComplexity  = 6
	mcpAdminRoles             = "board-admin"
)

// McpDOBootstrapSpec is the typed, provider-neutral input for the canonical
// board-OS MCP DigitalOcean bootstrap. The secret fields name the Terraform
// variable that holds the secret (NOT the value), so nothing sensitive enters the
// abstract topology or Terraform state.
type McpDOBootstrapSpec struct {
	// Environment is the deploy environment (e.g. "beta"). Defaults to "beta".
	Environment string

	// --- DO Spaces (binary source; replaces any S3 instance-role pull) ---
	// SpacesKVPath is the Vault KV-v2 leaf (under the `secret` mount) holding the
	// DO Spaces access_key/secret_key, resolved by Terraform at apply time via a
	// `data "vault_kv_secret_v2"` block (EPIC-BOOTFETCH-AWS-SM-TO-VAULT).
	// Default "infra/staging/do/spaces-keys".
	SpacesKVPath string

	// --- Board database (DO pyx-main-db, mesh_app) ---
	// BoardDBURLVar names the Terraform variable holding the full
	// BOARD_DATABASE_URL for the DO Managed Postgres pyx-main-db mesh_app
	// database. UNMIGRATED: no Vault leaf exists for this secret yet, so it
	// stays a render-time ${var.<x>} input (Secrets Manager beta-DO-pyx-main-db-url).
	BoardDBURLVar string // default "do_main_db_url"

	// --- Embed-token secret ---
	// EmbedTokenKVPath is the Vault KV-v2 leaf holding EMBED_TOKEN_SECRET (key
	// "embed_token"), resolved by Terraform at apply time.
	// Default "infra/staging/mcp".
	EmbedTokenKVPath string
}

// withDefaults fills the production-faithful defaults for any unset field so a
// bare spec renders the live-faithful bootstrap.
func (s McpDOBootstrapSpec) withDefaults() McpDOBootstrapSpec {
	def := func(v, d string) string {
		if strings.TrimSpace(v) == "" {
			return d
		}
		return v
	}
	s.Environment = def(s.Environment, "beta")
	s.SpacesKVPath = def(s.SpacesKVPath, "infra/staging/do/spaces-keys")
	// Shares the backend's DO Managed Postgres URL variable (same pyx-main-db /
	// mesh_app), so a single Secrets-Manager-backed variable serves both services.
	s.BoardDBURLVar = def(s.BoardDBURLVar, "do_main_db_url")
	s.EmbedTokenKVPath = def(s.EmbedTokenKVPath, "infra/staging/mcp")
	return s
}

// McpDOBootstrapVariableNames returns, in deterministic order, the Terraform
// variable names this bootstrap still references via ${var.<x>} (only the
// unmigrated board-DB URL today), partitioned plain vs sensitive so the
// assembler/CLI can emit the matching `variable "<x>" {}` declarations.
func (s McpDOBootstrapSpec) McpDOBootstrapVariableNames() (plain []string, sensitive []string) {
	s = s.withDefaults()
	plain = []string{}
	sensitive = []string{
		s.BoardDBURLVar,
	}
	return plain, sensitive
}

// McpDOVaultDataSources returns, in deterministic order, the `data
// "vault_kv_secret_v2"` HCL blocks this bootstrap's render-time secrets need
// (see vault_datasource.go): the DO Spaces keys and the embed-token secret.
func (s McpDOBootstrapSpec) McpDOVaultDataSources() []string {
	s = s.withDefaults()
	var docs []string
	for _, path := range []string{s.SpacesKVPath, s.EmbedTokenKVPath} {
		doc, _ := VaultKVDataSourceHCL(path)
		docs = append(docs, doc)
	}
	return docs
}

// RenderMcpDOUserData renders the canonical board-OS MCP DigitalOcean cloud-init
// as a bash script with Vault-sourced secret references (DO Spaces keys,
// EMBED_TOKEN_SECRET — resolved by Terraform at apply time from Vault, see
// vault_datasource.go) and one remaining ${var.<x>} placeholder for the
// not-yet-migrated board DB URL. It reproduces the LIVE beta-passobuild-mcp
// bootstrap: pull mcp.tar.gz from DO Spaces (injected keys, no instance role),
// write /etc/passobuild-mcp.env (BOARD_DATABASE_URL, EMBED_TOKEN_SECRET, the
// decompose/verify/optimize thresholds, BOARD_ADMIN_ROLES), install a hardened
// systemd unit and listen on :8787 (no CloudWatch). The returned string is
// meant to be placed into the mcp scale-group's
// UserDataByProvider["digitalocean"], closing the F2-02 catalog drift.
func RenderMcpDOUserData(spec McpDOBootstrapSpec) (string, error) {
	s := spec.withDefaults()
	if strings.TrimSpace(s.Environment) == "" {
		return "", fmt.Errorf("mcp-bootstrap: environment is required")
	}
	v := func(name string) string { return "${var." + name + "}" }
	_, spacesLabel := VaultKVDataSourceHCL(s.SpacesKVPath)
	_, embedLabel := VaultKVDataSourceHCL(s.EmbedTokenKVPath)
	spacesAccessKeyRef := VaultKVRef(spacesLabel, "access_key")
	spacesSecretKeyRef := VaultKVRef(spacesLabel, "secret_key")
	embedTokenRef := VaultKVRef(embedLabel, "embed_token")

	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }

	w("#!/bin/bash")
	w("set -euo pipefail")
	w("export DEBIAN_FRONTEND=noninteractive")
	w("# Canonical board-OS MCP server (Go, skill-plugin/mcp-go) DigitalOcean bootstrap —")
	w("# reproduces the LIVE beta-passobuild-mcp droplet (pd-MIG-CUTOVER-F2-02, closes drift).")
	w("# Spaces keys + EMBED_TOKEN_SECRET are Vault data sources (apply-time); the board")
	w("# DB URL is still a Terraform variable. No secret is ever inlined as a literal.")
	w("")
	w("# Base dependencies + the AWS CLI (used as the S3-compatible client for DO Spaces).")
	w("sudo apt-get update -y")
	w("sudo apt-get install -o Dpkg::Options::=\"--force-confold\" -y wget unzip curl jq tar")
	w("curl -s \"https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip\" -o \"awscliv2.zip\"")
	w("unzip -q awscliv2.zip && sudo ./aws/install")
	w("")
	w("# Service user.")
	w("sudo useradd -m -s /bin/bash mcp || true")
	w("sudo mkdir -p /opt/passobuild-mcp && sudo chown mcp:mcp /opt/passobuild-mcp")
	w("")
	w("# --- /etc/passobuild-mcp.env : board-OS config (adapted for the DO cutover) ---")
	w("cat > /etc/passobuild-mcp.env <<'EOV'")
	w("# --- Board database (DO Managed Postgres pyx-main-db, database %s) ---", mcpMainDBDatabase)
	w("# from Secrets Manager beta-DO-pyx-main-db-url (the same managed PG the backend uses).")
	w("BOARD_DATABASE_URL=%s", v(s.BoardDBURLVar))
	w("# --- Embedded-widget SSO token signing secret (Vault secret/infra/<env>/mcp, key embed_token) ---")
	w("EMBED_TOKEN_SECRET=%s", embedTokenRef)
	w("# --- Board decomposition / verify / optimize complexity gates (live values) ---")
	w("BOARD_DECOMPOSE_MIN_COMPLEXITY=%d", mcpDecomposeMinComplexity)
	w("BOARD_VERIFY_MIN_COMPLEXITY=%d", mcpVerifyMinComplexity)
	w("BOARD_OPTIMIZE_MIN_COMPLEXITY=%d", mcpOptimizeMinComplexity)
	w("# --- Admin roles ---")
	w("BOARD_ADMIN_ROLES=%s", mcpAdminRoles)
	w("# --- Listen port (behind the shared ALB) ---")
	w("# CRITICAL: the mcp-go binary reads PYXCLOUD_MCP_HTTP_PORT (config.go). An empty/unset")
	w("# value puts it in STDIO mode: it starts, reads EOF from stdin under systemd and exits")
	w("# 0/SUCCESS with NO listener (the F2-02 mcp blocker). Setting a legacy PORT= is IGNORED.")
	w("# Set the exact env var the binary reads so it enters HTTP mode and listens on :%d.", mcpListenPort)
	w("PYXCLOUD_MCP_HTTP_PORT=%d", mcpListenPort)
	w("EOV")
	w("chmod 640 /etc/passobuild-mcp.env && chown root:mcp /etc/passobuild-mcp.env")
	w("")
	w("# --- Pull the mcp tarball from DO Spaces (S3-compatible; Vault-sourced keys, NO instance role) ---")
	w("export AWS_ACCESS_KEY_ID=\"%s\"", spacesAccessKeyRef)
	w("export AWS_SECRET_ACCESS_KEY=\"%s\"", spacesSecretKeyRef)
	w("SPACES_ENDPOINT=\"%s\"", mcpSpacesEndpoint)
	w("echo \"Pulling board-OS MCP tarball from DO Spaces s3://%s/%s ...\"", mcpSpacesBucket, mcpSpacesKey)
	w("/usr/local/bin/aws s3 cp \"s3://%s/%s\" /tmp/mcp.tar.gz --endpoint-url \"$SPACES_ENDPOINT\" --region %s", mcpSpacesBucket, mcpSpacesKey, mcpSpacesRegion)
	w("# The Spaces keys are scoped to the artifact pull only.")
	w("unset AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY")
	w("sudo tar -xzf /tmp/mcp.tar.gz -C /opt/passobuild-mcp")
	w("sudo chown -R mcp:mcp /opt/passobuild-mcp")
	w("# The tarball extracts to mcp/passobuild-mcp, so the real server binary lives at")
	w("# /opt/passobuild-mcp/mcp/passobuild-mcp. The earlier bootstrap pointed ExecStart")
	w("# at /opt/passobuild-mcp/mcp (a DIRECTORY) -> systemd 203/EXEC restart loop. Pin the")
	w("# ExecStart to the actual binary path (%s) and assert it exists before starting.", mcpBinaryPath)
	w("if [ ! -x %s ]; then echo \"FATAL: mcp binary %s missing/not-executable after extract\" >&2; exit 1; fi", mcpBinaryPath, mcpBinaryPath)
	w("sudo chmod 755 %s", mcpBinaryPath)
	w("")
	w("# --- Hardened systemd unit (listen :%d, no CloudWatch) ---", mcpListenPort)
	w("cat > /etc/systemd/system/passobuild-mcp.service <<'EOSVC'")
	w("[Unit]")
	w("Description=PassoBuild board-OS MCP server")
	w("After=network.target")
	w("StartLimitIntervalSec=0")
	w("[Service]")
	w("User=mcp")
	w("Group=mcp")
	w("EnvironmentFile=/etc/passobuild-mcp.env")
	w("WorkingDirectory=/opt/passobuild-mcp/mcp")
	w("ExecStart=%s", mcpBinaryPath)
	w("StandardOutput=append:/var/log/passobuild-mcp.log")
	w("StandardError=append:/var/log/passobuild-mcp.log")
	w("Restart=always")
	w("RestartSec=10")
	w("[Install]")
	w("WantedBy=multi-user.target")
	w("EOSVC")
	w("sudo touch /var/log/passobuild-mcp.log && sudo chown mcp:mcp /var/log/passobuild-mcp.log")
	w("sudo systemctl daemon-reload && sudo systemctl enable passobuild-mcp")
	w("sudo systemctl restart passobuild-mcp")

	return b.String(), nil
}

// McpDOScaleGroupComponent returns the canonical `mcp` virtual-machine-scale-group
// AssembleComponent with the DigitalOcean bootstrap wired as
// UserDataByProvider["digitalocean"] (generic UserData left empty so non-DO
// placements are unaffected). Mirrors BackendDOScaleGroupComponent so the mcp
// service can also be wired standalone; the unified F2-02 entry point is
// PlatformScaleGroupComponentsWithDOBootstraps.
func McpDOScaleGroupComponent(arch, os, kubernetesVersion string, spec McpDOBootstrapSpec) (AssembleComponent, error) {
	ud, err := RenderMcpDOUserData(spec)
	if err != nil {
		return AssembleComponent{}, err
	}
	var svc PlatformService
	for _, s := range PlatformServices() {
		if s.Name == "mcp" {
			svc = s
			break
		}
	}
	return AssembleComponent{
		Name: "mcp",
		Type: "virtual-machine-scale-group",
		ScaleGroup: &AssembleScaleGroup{
			Architecture:      strings.TrimSpace(arch),
			CPU:               strconv.Itoa(svc.CPU),
			RAM:               strconv.Itoa(svc.RAM),
			OS:                strings.TrimSpace(os),
			Min:               svc.MinDesired,
			Max:               svc.MinDesired,
			Desired:           svc.MinDesired,
			Health:            svc.Health,
			KubernetesVersion: strings.TrimSpace(kubernetesVersion),
			UserDataByProvider: map[string]string{
				ProviderDigitalOcean: ud,
			},
		},
	}, nil
}
