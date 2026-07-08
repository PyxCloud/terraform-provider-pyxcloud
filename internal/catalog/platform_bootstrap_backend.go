package catalog

import (
	"fmt"
	"strconv"
	"strings"
)

// platform_bootstrap_backend.go — pd-MIG-CUTOVER-F2-02 (EPIC-AWS-TO-DO-MIGRATION).
//
// platform_asgs.go already expresses the `backend` platform service
// (pyx-backend) in the canonical vocabulary as a `virtual-machine-scale-group`
// of 1. But a scale-group of a bare Ubuntu box is NOT the backend: the provider
// must render the Go binary bootstrap, environment file, systemd unit and health
// probes that replace the retired Java/Quarkus native service.
//
// This file ports that bootstrap into the catalog as the DigitalOcean-specific
// override — RenderBackendDOUserData — and wires it as the backend scale-group's
// UserDataByProvider["digitalocean"] (BackendDOScaleGroupComponent). It is the
// HARD service of the cutover batch because it carries a huge env block AND is
// coupled to the live AWS control plane (an instance role + IMDS + the SAST ASG
// + CloudWatch), none of which exist on a DigitalOcean droplet. So the port is
// not a copy: it is an ADAPTATION of each AWS coupling for the DO cutover.
//
// The AWS-coupling decisions FOR THE CUTOVER (documented so the diff is auditable):
//
//   - Go binary source: DO Spaces `<env>-pyxcloud-artifact/pyx-backend`
//     fetched with the S3-compatible AWS CLI pointed at the fra1 Spaces
//     endpoint. There is NO instance role on DO, so the Spaces access
//     key/secret are injected at render as Terraform variables (never inlined).
//
//   - Main database: Go keeps the historical Quarkus-style config keys under the
//     PYX_ env prefix (`PYX_QUARKUS_DATASOURCE_*`). The provider accepts the
//     existing single DB URL secret, normalizes libpq postgres:// URLs to JDBC,
//     and derives username/password for pgx.
//
//   - OIDC / Vault / GitHub / Stripe / multi-cloud (Azure/GCP/DO/Linode/Ubicloud)
//     keys are KEPT — the app still talks to the same SSO, Vault, GitHub and Stripe
//     and still makes cross-cloud SDK calls. They are injected at render as
//     Terraform variables (secrets marked sensitive), the same source the AWS
//     module wired.
//
//   - Vault URL `beta-vault.pyxcloud.io` is KEPT verbatim. PREREQUISITE, flagged:
//     Vault must be reachable from the DO droplet (today it sits behind the AWS
//     VPC / VPN). Cross-cloud reachability (VPC peering, a public+mTLS Vault
//     listener, or the Vault-HA operator on DO) is a hard prereq for the cutover —
//     if Vault is unreachable the backend health-probe's `vault=` line stays DOWN
//     and delegated secrets fail.
//
//   - AWS SDK credentials (AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY) are KEPT as
//     a passthrough so the app's cross-cloud AWS SDK calls (it provisions/reads AWS
//     on behalf of users) keep working from the DO box. They are injected as
//     sensitive Terraform variables, NOT an instance role.
//
//   - SAST-ASG integration is DISABLED (PYX_SAST_ASG_ENABLED=false; the bucket /
//     name left empty). SAST is being re-architected separately; the DO droplet has
//     no `autoscaling:SetDesiredCapacity` coupling and no SAST runner ASG, so the
//     JIT-scale integration is turned off for the cutover rather than pointed at a
//     non-existent DO ASG.
//
//   - CloudWatch agent + X-Ray are DROPPED. There is no CloudWatchAgentServerPolicy
//     instance role and no DO analogue wired here; observability on DO is the LGTM
//     stack (separate component). The `amazon-cloudwatch-agent` install, the
//     cw-agent-api.json config and the put-metric-data call are removed.
//
//   - The health-probe cron is KEPT but it now exercises the Go endpoints
//     `/healthz` and `/readyz`. AWS IMDS and CloudWatch metric publishing stay
//     removed.
//
// SECURITY: like platform_bootstrap_sso.go, NO secret VALUE is inlined. Every
// credential (DB URL, OIDC/MCP secrets, GitHub PAT, Stripe token, AWS keys, the
// multi-cloud tokens, the Spaces keys, the git deploy key, the GCP SA JSON) is
// referenced by Terraform variable name; the operator wires those vars to the
// same Secrets Manager source. The script never embeds a literal credential.

// Pinned artifact coordinates for the backend Go binary on DO Spaces — kept in
// one place so key naming stays consistent across renderers.
const (
	// backendSpacesKey is the stable Go binary key under <env>-pyxcloud-artifact.
	backendSpacesKey = "pyx-backend"
	// backendSpacesEndpoint is the S3-compatible fra1 Spaces endpoint the AWS CLI
	// is pointed at (no instance role; the CLI uses the injected Spaces keys).
	backendSpacesEndpoint = "https://fra1.digitaloceanspaces.com"
	// backendSpacesRegion is the region token the S3-compatible client expects for
	// fra1 Spaces.
	backendSpacesRegion = "fra1"
	// backendAppVersion is the Go backend VERSION pulled for a versioned rollout
	// when available; the stable key remains the boot fallback.
	backendAppVersion = "0.4.60"
	// backendMainDBDatabase documents the target DO Managed Postgres database
	// (mesh_app) — the JDBC URL var (beta-DO-pyx-main-db-url) already encodes it;
	// kept as a constant so the assertion + comment name the same value.
	backendMainDBDatabase = "mesh_app"
)

// BackendBootstrapSpec is the typed, provider-neutral input for the canonical
// backend (pyx-backend Go monolith) DigitalOcean bootstrap. Every value the
// hand-written AWS module pulled from a Terraform interpolation is lifted to an
// explicit field so the component is self-describing and round-trippable. The
// secret fields name the Terraform variable that holds the secret (NOT the
// value), so nothing sensitive enters the abstract topology or Terraform state.
type BackendBootstrapSpec struct {
	// Environment is the deploy environment (e.g. "beta"); drives the OIDC/Vault
	// hostnames (<env>-auth / <env>-vault / <env>-console .pyxcloud.io). Required.
	Environment string
	// DomainName is the apex used for the public hostnames. Defaults to "pyxcloud.io".
	DomainName string

	// AppVersion is the Go backend VERSION to pull from Spaces. Defaults to the
	// pinned backendAppVersion so a bare spec prefers the current versioned key.
	AppVersion string

	// --- DO Spaces (Go binary source; replaces the S3 instance-role pull) ---
	// SpacesKeyVar / SpacesSecretVar name the Terraform variables holding the DO
	// Spaces access key / secret (Secrets Manager beta-DigitalOceanSpacesKeys).
	SpacesKeyVar    string // default "do_spaces_key"
	SpacesSecretVar string // default "do_spaces_secret"

	// --- Main database (DO pyx-main-db, mesh_app) ---
	// MainDBURLVar names the variable holding the full jdbc URL (sslmode=require)
	// for the DO Managed Postgres pyx-main-db mesh_app database (Secrets Manager
	// beta-DO-pyx-main-db-url).
	MainDBURLVar string // default "do_main_db_url"

	// --- Kept application secrets/config (injected at render as variables) ---
	OIDCClientSecretVar   string // default "oidc_client_secret"
	MCPSAClientIDVar      string // default "mcp_sa_client_id"
	MCPSAClientSecretVar  string // default "mcp_sa_client_secret"
	GitHubPATVar          string // default "gh_pat"
	StripeTokenVar        string // default "stripe_token"
	StripePriceIDVar      string // default "stripe_license_price_id"
	RunnerPublicKeyVar    string // default "runner_public_key"
	GitPrivateKeyVar      string // default "git_private_key"
	GCPSAKeyJSONVar       string // default "gcp_sa_key_json"
	AutomigratePubKeyVar  string // default "automigrate_public_key"
	AutomigrateEnabledVar string // default "automigrate_enabled"
	AutomigrateExecuteVar string // default "automigrate_execute"

	// --- Multi-cloud SDK credentials (KEPT for cross-cloud provisioning) ---
	// AWS creds are the passthrough that keeps the app's cross-cloud AWS SDK calls
	// working from the DO box (no instance role on DO).
	AWSAccessKeyIDVar     string // default "aws_access_key_id"
	AWSSecretAccessKeyVar string // default "aws_secret_access_key"
	AWSRegionVar          string // default "aws_region"
	AzureSubscriptionVar  string // default "azure_subscription_id"
	AzureTenantVar        string // default "azure_tenant_id"
	AzureClientIDVar      string // default "azure_client_id"
	AzureClientSecretVar  string // default "azure_client_secret"
	GCPProjectIDVar       string // default "gcp_project_id"
	GCPAccountVar         string // default "gcp_account"
	DigitalOceanTokenVar  string // default "digitalocean_token"
	LinodeTokenVar        string // default "linode_token"
	UbicloudIDVar         string // default "ubicloud_id"
	UbicloudTokenVar      string // default "ubicloud_token"
	UbicloudLLMKeyVar     string // default "ubicloud_llm_api_key"
}

// withDefaults fills the production-faithful defaults for any unset variable-name
// field so callers can pass an almost-empty spec and still get the canonical
// wiring.
func (s BackendBootstrapSpec) withDefaults() BackendBootstrapSpec {
	def := func(v, d string) string {
		if strings.TrimSpace(v) == "" {
			return d
		}
		return v
	}
	s.DomainName = def(s.DomainName, "pyxcloud.io")
	s.AppVersion = def(s.AppVersion, backendAppVersion)
	s.SpacesKeyVar = def(s.SpacesKeyVar, "do_spaces_key")
	s.SpacesSecretVar = def(s.SpacesSecretVar, "do_spaces_secret")
	s.MainDBURLVar = def(s.MainDBURLVar, "do_main_db_url")
	s.OIDCClientSecretVar = def(s.OIDCClientSecretVar, "oidc_client_secret")
	s.MCPSAClientIDVar = def(s.MCPSAClientIDVar, "mcp_sa_client_id")
	s.MCPSAClientSecretVar = def(s.MCPSAClientSecretVar, "mcp_sa_client_secret")
	s.GitHubPATVar = def(s.GitHubPATVar, "gh_pat")
	s.StripeTokenVar = def(s.StripeTokenVar, "stripe_token")
	s.StripePriceIDVar = def(s.StripePriceIDVar, "stripe_license_price_id")
	s.RunnerPublicKeyVar = def(s.RunnerPublicKeyVar, "runner_public_key")
	s.GitPrivateKeyVar = def(s.GitPrivateKeyVar, "git_private_key")
	s.GCPSAKeyJSONVar = def(s.GCPSAKeyJSONVar, "gcp_sa_key_json")
	s.AutomigratePubKeyVar = def(s.AutomigratePubKeyVar, "automigrate_public_key")
	s.AutomigrateEnabledVar = def(s.AutomigrateEnabledVar, "automigrate_enabled")
	s.AutomigrateExecuteVar = def(s.AutomigrateExecuteVar, "automigrate_execute")
	s.AWSAccessKeyIDVar = def(s.AWSAccessKeyIDVar, "aws_access_key_id")
	s.AWSSecretAccessKeyVar = def(s.AWSSecretAccessKeyVar, "aws_secret_access_key")
	s.AWSRegionVar = def(s.AWSRegionVar, "aws_region")
	s.AzureSubscriptionVar = def(s.AzureSubscriptionVar, "azure_subscription_id")
	s.AzureTenantVar = def(s.AzureTenantVar, "azure_tenant_id")
	s.AzureClientIDVar = def(s.AzureClientIDVar, "azure_client_id")
	s.AzureClientSecretVar = def(s.AzureClientSecretVar, "azure_client_secret")
	s.GCPProjectIDVar = def(s.GCPProjectIDVar, "gcp_project_id")
	s.GCPAccountVar = def(s.GCPAccountVar, "gcp_account")
	s.DigitalOceanTokenVar = def(s.DigitalOceanTokenVar, "digitalocean_token")
	s.LinodeTokenVar = def(s.LinodeTokenVar, "linode_token")
	s.UbicloudIDVar = def(s.UbicloudIDVar, "ubicloud_id")
	s.UbicloudTokenVar = def(s.UbicloudTokenVar, "ubicloud_token")
	s.UbicloudLLMKeyVar = def(s.UbicloudLLMKeyVar, "ubicloud_llm_api_key")
	return s
}

// BackendBootstrapVariableNames returns, in deterministic order, the Terraform
// variable names this bootstrap references, partitioned plain vs sensitive so the
// assembler/CLI can emit the matching `variable "<x>" {}` declarations (the
// credential-bearing ones marked sensitive) and the rendered .tf `validate`s.
func (s BackendBootstrapSpec) BackendBootstrapVariableNames() (plain []string, sensitive []string) {
	s = s.withDefaults()
	plain = []string{
		s.AWSRegionVar,
		s.MCPSAClientIDVar,
		s.AzureSubscriptionVar, s.AzureTenantVar, s.AzureClientIDVar,
		s.GCPProjectIDVar, s.GCPAccountVar,
		s.UbicloudIDVar,
		s.AutomigratePubKeyVar, s.AutomigrateEnabledVar, s.AutomigrateExecuteVar,
		s.StripePriceIDVar,
		s.RunnerPublicKeyVar,
	}
	sensitive = []string{
		s.MainDBURLVar,
		s.SpacesKeyVar, s.SpacesSecretVar,
		s.OIDCClientSecretVar, s.MCPSAClientSecretVar,
		s.GitHubPATVar, s.StripeTokenVar,
		s.GitPrivateKeyVar, s.GCPSAKeyJSONVar,
		s.AWSAccessKeyIDVar, s.AWSSecretAccessKeyVar,
		s.AzureClientSecretVar,
		s.DigitalOceanTokenVar, s.LinodeTokenVar,
		s.UbicloudTokenVar, s.UbicloudLLMKeyVar,
	}
	return plain, sensitive
}

// RenderBackendDOUserData renders the canonical pyx-backend Go DigitalOcean
// cloud-init as a bash script with `${var.<x>}` placeholders. It pulls the Go
// binary from DO Spaces (injected keys, no instance role), maps legacy
// Quarkus-style env names to the Go config loader, keeps the cross-cloud keys,
// disables the old SAST-ASG integration, drops CloudWatch/X-Ray, and probes the
// Go `/healthz` and `/readyz` endpoints. The returned string is meant to be
// placed into the backend scale-group's UserDataByProvider["digitalocean"].
func RenderBackendDOUserData(spec BackendBootstrapSpec) (string, error) {
	s := spec.withDefaults()
	if strings.TrimSpace(s.Environment) == "" {
		return "", fmt.Errorf("backend-bootstrap: environment is required (drives the <env>-auth/<env>-vault/<env>-console.%s hostnames)", s.DomainName)
	}
	env := s.Environment
	v := func(name string) string { return "${var." + name + "}" }

	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	// wl emits a line VERBATIM (no printf interpretation). Use it for any line that
	// contains a literal `%` that must NOT be treated as a fmt verb — notably the
	// #127 jdbc-normalization Python heredoc, whose `%s`/`%` operator would
	// otherwise be mangled by fmt.Fprintf into `%!s(MISSING)` sentinels, breaking
	// the on-box normalizer (F2-02 crash-loop root cause).
	wl := func(line string) { b.WriteString(line); b.WriteByte('\n') }

	w("#!/bin/bash")
	w("set -euo pipefail")
	w("export DEBIAN_FRONTEND=noninteractive")
	w("# Canonical pyx-backend Go monolith DigitalOcean bootstrap rendered by the")
	w("# PyxCloud provider. Provider-neutral placeholders; all secrets are Terraform")
	w("# variables, never inlined. AWS-couplings adapted for the DO cutover.")
	w("")
	w("# Service user + the STABLE deploy-runner key (no per-deploy user_data churn).")
	w("sudo useradd -m -s /bin/bash main || true")
	w("sudo usermod -aG sudo main")
	w("echo \"main ALL=(ALL) NOPASSWD: ALL\" | sudo tee /etc/sudoers.d/main > /dev/null")
	w("sudo mkdir -p /home/main/.ssh && sudo chmod 700 /home/main/.ssh")
	w("echo \"%s\" | sudo tee /home/main/.ssh/authorized_keys > /dev/null", v(s.RunnerPublicKeyVar))
	w("sudo chmod 600 /home/main/.ssh/authorized_keys && sudo chown -R main:main /home/main/.ssh")
	w("")
	w("# Base dependencies + the AWS CLI (used as the S3-compatible client for DO Spaces).")
	w("sudo apt-get update -y")
	w("sudo apt-get install -o Dpkg::Options::=\"--force-confold\" -y wget unzip openssl zip curl jq git")
	w("curl -s \"https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip\" -o \"awscliv2.zip\"")
	w("unzip -q awscliv2.zip && sudo ./aws/install")
	w("")
	w("# --- /home/main/env : full application config (adapted for the DO cutover) ---")
	w("cat > /home/main/env <<'EOV'")
	w("# --- OIDC / Keycloak ---")
	w("PYX_QUARKUS_OIDC_AUTH_SERVER_URL=https://%s-auth.%s/realms/pyx", env, s.DomainName)
	w("PYX_QUARKUS_OIDC_CLIENT_ID=backend")
	w("PYX_QUARKUS_OIDC_CREDENTIALS_SECRET=%s", v(s.OIDCClientSecretVar))
	w("PYX_QUARKUS_OIDC_PASSOBUILD_AUTH_SERVER_URL=https://%s-auth.%s/realms/passobuild", env, s.DomainName)
	w("PYX_QUARKUS_OIDC_PASSOBUILD_CLIENT_ID=passobuild")
	w("PYX_QUARKUS_OIDC_PASSOBUILD_TOKEN_AUDIENCE=passobuild-mcp")
	w("PYX_MCP_SA_CLIENT_ID=%s", v(s.MCPSAClientIDVar))
	w("PYX_MCP_SA_CLIENT_SECRET=%s", v(s.MCPSAClientSecretVar))
	w("# --- Vault --- (KEPT; PREREQ: beta-vault.%s must be reachable from the DO droplet)", s.DomainName)
	w("PYX_VAULT_ADDR=https://%s-vault.%s", env, s.DomainName)
	w("VAULT_AUTH_ROLE=api-delegation")
	w("# --- CORS & Frontend ---")
	w("PYX_QUARKUS_HTTP_CORS_ORIGINS=https://pyxcloud.io,https://%s-console.%s,https://passo.build,https://www.passo.build", env, s.DomainName)
	w("PYX_QUARKUS_HTTP_CORS_METHODS=GET,POST,PUT,DELETE,OPTIONS")
	w("PYXCLOUD_FRONTEND_URL=https://%s-console.%s", env, s.DomainName)
	w("PYXCLOUD_WEBAUTHN_RP_ID=%s-console.%s", env, s.DomainName)
	w("# --- GitHub ---")
	w("PYX_GITHUB_PERSONAL_ACCESS_TOKEN=%s", v(s.GitHubPATVar))
	w("PYX_GITHUB_PIPELINE_REPO=ddev-deploy-user-pipeline")
	w("# --- Database (DO Managed Postgres pyx-main-db, database %s) ---", backendMainDBDatabase)
	w("# Source secret (beta-DO-pyx-main-db-url) may be a libpq URI (postgres://user:pass@host:port/db?...)")
	w("# OR already-jdbc form. The Go loader keeps historical quarkus.datasource.* keys behind")
	w("# the PYX_ env prefix, so normalize into PYX_QUARKUS_DATASOURCE_* below.")
	w("PYX_QUARKUS_DATASOURCE_JDBC_URL=%s", v(s.MainDBURLVar))
	w("# --- AWS (SDK creds passthrough: KEPT so cross-cloud AWS SDK calls work from DO) ---")
	w("AWS_REGION=%s", v(s.AWSRegionVar))
	w("AWS_ACCESS_KEY_ID=%s", v(s.AWSAccessKeyIDVar))
	w("AWS_SECRET_ACCESS_KEY=%s", v(s.AWSSecretAccessKeyVar))
	w("# --- Azure ---")
	w("AZURE_SUBSCRIPTION_ID=%s", v(s.AzureSubscriptionVar))
	w("AZURE_TENANT_ID=%s", v(s.AzureTenantVar))
	w("AZURE_CLIENT_ID=%s", v(s.AzureClientIDVar))
	w("AZURE_CLIENT_SECRET=%s", v(s.AzureClientSecretVar))
	w("# --- GCP ---")
	w("GCP_PROJECT_ID=%s", v(s.GCPProjectIDVar))
	w("GCP_ACCOUNT=%s", v(s.GCPAccountVar))
	w("GOOGLE_APPLICATION_CREDENTIALS=/home/main/gcp-sa-key.json")
	w("# --- DigitalOcean ---")
	w("DIGITALOCEAN_TOKEN=%s", v(s.DigitalOceanTokenVar))
	w("# --- Ubicloud / LLM ---")
	w("UBICLOUD_ID=%s", v(s.UbicloudIDVar))
	w("UBICLOUD_TOKEN=%s", v(s.UbicloudTokenVar))
	w("UBICLOUD_LLM_API_KEY=%s", v(s.UbicloudLLMKeyVar))
	w("# --- Cheap-actuator background pool (server-side board drain) ---")
	w("PYX_ACTUATOR_BACKGROUND_ENABLED=true")
	w("PYX_SERVER_AI_ACTUATOR_POOL_SIZE=6")
	w("# --- Linode ---")
	w("LINODE_TOKEN=%s", v(s.LinodeTokenVar))
	w("# --- Stripe ---")
	w("STRIPE_TOKEN=%s", v(s.StripeTokenVar))
	w("STRIPE_LICENSE_PRICE_ID=%s", v(s.StripePriceIDVar))
	w("# --- Git SSH (for Terraform generation) ---")
	w("GIT_PRIVATE_KEY_PATH=/home/main/.ssh/git_deploy_key")
	w("# --- SSO IP ---")
	w("PYX_SSO_IP=127.0.0.1,0:0:0:0:0:0:0:1,localhost")
	w("# --- Pyxfile auto-migration ---")
	w("PYXCLOUD_AUTOMIGRATE_PUBLIC_KEY=%s", v(s.AutomigratePubKeyVar))
	w("PYXCLOUD_AUTOMIGRATE_ENABLED=%s", v(s.AutomigrateEnabledVar))
	w("PYXCLOUD_AUTOMIGRATE_EXECUTE=%s", v(s.AutomigrateExecuteVar))
	w("# --- SAST JIT ASG Runner: DISABLED for the DO cutover (SAST re-architected separately;")
	w("#     no autoscaling:SetDesiredCapacity coupling and no SAST runner ASG on DO). ---")
	w("PYX_SAST_ASG_ENABLED=false")
	w("PYX_SAST_ASG_BUCKET=")
	w("PYX_SAST_ASG_NAME=")
	w("EOV")
	w("")
	w("# --- Normalize DB URL (F2-02): the DO pyx-main-db secret is stored as a libpq URI")
	w("# (postgres://user:pass@host:port/db?sslmode=require). Convert it in place to")
	w("# jdbc:postgresql:// form and split out PYX_QUARKUS_DATASOURCE_USERNAME/PASSWORD")
	w("# for the Go pgx pool (idempotent; a no-op if the value")
	w("# is already jdbc form and the username/password vars already exist).")
	// The EONORM Python heredoc is emitted VERBATIM via wl (NOT through fmt.Fprintf):
	// it contains literal `%s`/`%u`/`%p` and the Python `%` string-format operator,
	// which fmt would otherwise mangle into `%!s(MISSING)` sentinels and destroy the
	// normalizer (F2-02 crash-loop). The heredoc body is single-quoted ('EONORM'), so
	// the shell performs no expansion either — the Python source arrives on-box exactly
	// as written here.
	wl("python3 - <<'EONORM'")
	wl("import re")
	wl("p = \"/home/main/env\"")
	wl("lines = open(p).read().splitlines()")
	wl("out, have_user, have_pass = [], False, False")
	wl("for ln in lines:")
	wl("    if ln.startswith(\"PYX_QUARKUS_DATASOURCE_USERNAME=\"): have_user = True")
	wl("    if ln.startswith(\"PYX_QUARKUS_DATASOURCE_PASSWORD=\"): have_pass = True")
	wl("for ln in lines:")
	wl("    if ln.startswith(\"PYX_QUARKUS_DATASOURCE_JDBC_URL=\"):")
	wl("        val = ln.split(\"=\", 1)[1].strip().strip(\"'\\\"\")")
	wl("        m = re.match(r\"postgres(?:ql)?://([^:]+):([^@]+)@([^:/]+):(\\d+)/([^?]+)(\\?.*)?$\", val)")
	wl("        if m:")
	wl("            user, pw, host, port, db, q = m.groups()")
	wl("            q = q or \"\"")
	wl("            out.append(\"PYX_QUARKUS_DATASOURCE_JDBC_URL=jdbc:postgresql://%s:%s/%s%s\" % (host, port, db, q))")
	wl("            if not have_user: out.append(\"PYX_QUARKUS_DATASOURCE_USERNAME=%s\" % user)")
	wl("            if not have_pass: out.append(\"PYX_QUARKUS_DATASOURCE_PASSWORD=%s\" % pw)")
	wl("        else:")
	wl("            out.append(ln)  # already jdbc form (or unrecognized) -> leave as-is")
	wl("    else:")
	wl("        out.append(ln)")
	wl("open(p, \"w\").write(\"\\n\".join(out) + \"\\n\")")
	wl("EONORM")
	w("")
	w("# GCP SA key + git deploy key (secrets by variable, never inlined literals).")
	w("cat > /home/main/gcp-sa-key.json <<'EOGCP'")
	w("%s", v(s.GCPSAKeyJSONVar))
	w("EOGCP")
	w("chmod 600 /home/main/gcp-sa-key.json")
	w("cat > /home/main/.ssh/git_deploy_key <<'EOGIT'")
	w("%s", v(s.GitPrivateKeyVar))
	w("EOGIT")
	w("chmod 600 /home/main/.ssh/git_deploy_key")
	w("chown main:main /home/main/env /home/main/gcp-sa-key.json /home/main/.ssh/git_deploy_key")
	w("")
	w("# --- Hardened systemd unit (Go binary, health :8080) ---")
	w("cat > /etc/systemd/system/pyx-backend.service <<'EOSVC'")
	w("[Unit]")
	w("Description=PyxCloud Go Backend")
	w("After=network.target")
	w("StartLimitIntervalSec=0")
	w("[Service]")
	w("User=main")
	w("Group=main")
	w("EnvironmentFile=/home/main/env")
	w("WorkingDirectory=/home/main")
	w("ExecStart=/home/main/pyx-backend")
	w("StandardOutput=append:/var/log/pyx-backend.log")
	w("StandardError=append:/var/log/pyx-backend.log")
	w("Restart=always")
	w("RestartSec=3")
	w("MemoryMax=1500M")
	w("[Install]")
	w("WantedBy=multi-user.target")
	w("EOSVC")
	w("sudo touch /var/log/pyx-backend.log && sudo chown main:main /var/log/pyx-backend.log")
	w("sudo systemctl daemon-reload && sudo systemctl enable pyx-backend")
	w("")
	w("# --- Local :8080 health-probe (replaces the AWS metadata + CloudWatch metric probe) ---")
	w("# DROPPED for the DO cutover: the CloudWatch agent + X-Ray, the EC2 link-local")
	w("# metadata instance-id lookup and the CloudWatch metric publish. This probe just")
	w("# curls the LOCAL :8080 health endpoint and logs vault=/sso= via `logger` (DO metadata")
	w("# differs from EC2 and there is no CloudWatch to publish to).")
	w("cat > /home/main/health-probe.sh <<'EOHP'")
	w("#!/bin/bash")
	w("set -uo pipefail")
	w("LIVE=$(curl -sf -o /dev/null -w '%%%%{http_code}' --max-time 5 \"http://localhost:8080/healthz\" 2>/dev/null || echo 000)")
	w("READY=$(curl -sf -o /dev/null -w '%%%%{http_code}' --max-time 5 \"http://localhost:8080/readyz\" 2>/dev/null || echo 000)")
	w("logger -t pyx-backend-health \"healthz=$LIVE readyz=$READY\"")
	w("EOHP")
	w("chown main:main /home/main/health-probe.sh && chmod +x /home/main/health-probe.sh")
	w("# Install the health-probe cron. On a fresh box the root crontab is EMPTY, so")
	w("# `crontab -l | grep -v ...` makes grep exit 1 (no match) which, under")
	w("# `set -o pipefail`, aborts the whole boot BEFORE the binary pull. Build the new")
	w("# crontab without a failing pipeline (grep guarded with `|| true`).")
	w("CRON_EXISTING=$(sudo crontab -l -u root 2>/dev/null | grep -v 'health-probe.sh' || true)")
	w("printf '%%s\\n%%s\\n' \"$CRON_EXISTING\" \"* * * * * /home/main/health-probe.sh\" | sed '/^$/d' | sudo crontab -u root -")
	w("")
	w("# --- Pull the Go binary from DO Spaces (S3-compatible; injected keys, NO instance role) ---")
	w("# Robust pull: retry with backoff, verify the download is non-trivially large")
	w("# and matches the Spaces object size before starting, and only start AFTER a")
	w("# verified fetch (never start on a partial binary).")
	w("export AWS_ACCESS_KEY_ID=\"%s\"", v(s.SpacesKeyVar))
	w("export AWS_SECRET_ACCESS_KEY=\"%s\"", v(s.SpacesSecretVar))
	w("PYX_VERSION=\"%s\"", s.AppVersion)
	w("SPACES_ENDPOINT=\"%s\"", backendSpacesEndpoint)
	w("MIN_BYTES=10000000  # sanity floor: Go backend binary should never be tiny/empty")
	w("# Resolve the object key to pull: prefer the versioned key, fall back to the stable key.")
	w("BIN_KEY=\"%s\"", backendSpacesKey)
	w("BUCKET=\"%s-pyxcloud-artifact\"", env)
	w("if [ -n \"$PYX_VERSION\" ] && /usr/local/bin/aws s3api head-object --bucket \"$BUCKET\" --key \"%s-$PYX_VERSION\" --endpoint-url \"$SPACES_ENDPOINT\" --region %s >/dev/null 2>&1; then", backendSpacesKey, backendSpacesRegion)
	w("  BIN_KEY=\"%s-$PYX_VERSION\"", backendSpacesKey)
	w("  echo \"Using versioned key $BIN_KEY.\"")
	w("else")
	w("  echo \"Versioned key missing/unset; using the stable '%s' key.\"", backendSpacesKey)
	w("fi")
	w("# Expected size from the Spaces object metadata (used to verify a complete pull).")
	w("EXPECTED_BYTES=$(/usr/local/bin/aws s3api head-object --bucket \"$BUCKET\" --key \"$BIN_KEY\" --endpoint-url \"$SPACES_ENDPOINT\" --region %s --query ContentLength --output text 2>/dev/null || echo 0)", backendSpacesRegion)
	w("# Base object is s3://$BUCKET/%s (stable key); $BIN_KEY may carry the -$PYX_VERSION suffix.", backendSpacesKey)
	w("echo \"Pulling backend Go binary s3://$BUCKET/$BIN_KEY (expected $EXPECTED_BYTES bytes) ...\"")
	w("PULL_OK=0")
	w("for attempt in 1 2 3 4 5; do")
	w("  rm -f /home/main/pyx-backend")
	w("  if /usr/local/bin/aws s3 cp \"s3://$BUCKET/$BIN_KEY\" /home/main/pyx-backend --endpoint-url \"$SPACES_ENDPOINT\" --region %s --cli-read-timeout 0 --cli-connect-timeout 30; then", backendSpacesRegion)
	w("    GOT_BYTES=$(stat -c%%s /home/main/pyx-backend 2>/dev/null || echo 0)")
	w("    if [ \"$GOT_BYTES\" -ge \"$MIN_BYTES\" ] && { [ \"$EXPECTED_BYTES\" = \"0\" ] || [ \"$GOT_BYTES\" = \"$EXPECTED_BYTES\" ]; }; then")
	w("      echo \"Verified Go binary: $GOT_BYTES bytes (expected $EXPECTED_BYTES).\"; PULL_OK=1; break")
	w("    fi")
	w("    echo \"Pull incomplete: got $GOT_BYTES bytes, expected $EXPECTED_BYTES (>= $MIN_BYTES); retrying...\" >&2")
	w("  else")
	w("    echo \"aws s3 cp failed (attempt $attempt); retrying...\" >&2")
	w("  fi")
	w("  sleep $((attempt*15))")
	w("done")
	w("# The Spaces keys are scoped to the artifact pull only; the app's AWS SDK uses the")
	w("# AWS_* creds from /home/main/env (loaded by the systemd EnvironmentFile), not these.")
	w("unset AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY")
	w("if [ \"$PULL_OK\" != \"1\" ]; then")
	w("  echo \"FATAL: could not fetch a complete backend Go binary after retries; NOT starting the service.\" >&2")
	w("  exit 1")
	w("fi")
	w("chown main:main /home/main/pyx-backend && chmod 755 /home/main/pyx-backend")
	w("# Only now (verified binary present) start the service.")
	w("sudo systemctl daemon-reload && sudo systemctl restart pyx-backend")

	return b.String(), nil
}

// BackendDOScaleGroupComponent returns the canonical `backend`
// virtual-machine-scale-group AssembleComponent with the DigitalOcean bootstrap
// wired as UserDataByProvider["digitalocean"] (and the generic UserData left
// empty so non-DO placements are unaffected). This is the wiring point that turns
// "a scale-group of 1" into "the pyx-backend service on DigitalOcean": the DO
// override is resolved by TranslateScaleGroup and descended by the
// digitalocean_droplet_autoscale renderer — no new translator (SPEC §1).
//
// arch/os/kubernetesVersion may be empty to take the canonical defaults; they are
// forwarded to match the sizing/placement the other platform services use.
func BackendDOScaleGroupComponent(arch, os, kubernetesVersion string, spec BackendBootstrapSpec) (AssembleComponent, error) {
	ud, err := RenderBackendDOUserData(spec)
	if err != nil {
		return AssembleComponent{}, err
	}
	// Reuse the canonical PlatformServices sizing/health for the `backend` service
	// so the DO component matches the rest of the platform (a scale-group of 1,
	// LB-health, self-heal), rather than hand-picking values here.
	var svc PlatformService
	for _, s := range PlatformServices() {
		if s.Name == "backend" {
			svc = s
			break
		}
	}
	return AssembleComponent{
		Name: "backend",
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
