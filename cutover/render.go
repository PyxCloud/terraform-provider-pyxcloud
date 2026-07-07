// Command render is the COMMITTED, reproducible renderer for the AWS -> DigitalOcean
// cutover baseline (pd-MIG-CUTOVER-F2-02). It replaces the ad-hoc "re-render catalog
// HCL into a throwaway /tmp dir on every apply" workflow: the estate is rendered
// deterministically into cutover/generated/ from the committed catalog descriptor
// catalog.DOBaselineInput, and applied against the persistent state, which lives
// in the S3-compatible DigitalOcean Spaces bucket
// (s3://pyx-terraform-state/cutover/do-baseline-fra1.tfstate @ fra1). The legacy
// AWS S3 bucket (pyxcloud-terraform-state, eu-west-1) is retained as a cold backup
// until the AWS-decommission step; state was migrated with `terraform init
// -migrate-state` (pd-MIG-CUTOVER-STATE-OFF-AWS).
//
// It is intentionally the SPIRIT of the requested entry point:
//
//	catalog.AssembleHCL(ctx, catalog.MustEmbedded(),
//	    catalog.DOBaselineInput("Frankfurt","x86_64","ubuntu","1.30"))
//
// The generic AssembleHCL scale-group path descends to a DOKS
// digitalocean_kubernetes_cluster on DigitalOcean, but the LIVE cutover estate was
// applied as digitalocean_droplet_autoscale groups. Rendering the DOKS shape against
// that state would plan a full destroy+recreate of every service. So this renderer
// calls catalog.AssembleDOBaseline — the committed assembler that emits the exact
// droplet-autoscale estate matching the S3 state — with the same DOBaselineInput
// descriptor. See internal/catalog/do_baseline.go for the reconciliation rationale.
//
// SECRETS: nothing secret is committed. The mcp launch template's Spaces keys,
// EMBED token, and the DURABLE mesh_app BOARD_DATABASE_URL are injected at RENDER
// time from environment variables the README exports out of AWS Secrets Manager.
// The DO token and Spaces provider credentials are read by terraform from the
// DIGITALOCEAN_TOKEN / SPACES_* environment at plan/apply time (never rendered).
//
// Usage (see cutover/README.md for the full workflow):
//
//	export DO_BOARD_DATABASE_URL="$(aws secretsmanager get-secret-value ... beta-DO-pyx-main-db-url)"
//	export DO_SPACES_ACCESS_KEY=... DO_SPACES_SECRET_KEY=... DO_MCP_EMBED_TOKEN=...
//	go run ./cutover/render.go
//	(cd cutover/generated && terraform init && terraform apply -var 'do_ssh_keys=["57496891"]')
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/catalog"
)

// stateBucket / stateKey / stateEndpoint pin the persistent state backend. This is
// the ONE authoritative state for the cutover baseline — the whole point of the
// harness is that state (and now the config) persists, not a /tmp render.
//
// The backend is the standard terraform "s3" backend pointed at the S3-COMPATIBLE
// DigitalOcean Spaces endpoint (fra1). Spaces has no DynamoDB and no real AWS
// region/STS/metadata, so the AWS-specific validation is skipped and locking uses
// the native S3 lockfile (use_lockfile=true, terraform >= 1.11). stateRegion is a
// required-but-ignored placeholder for the s3 backend; Spaces routing is by endpoint.
const (
	stateBucket   = "pyx-terraform-state"
	stateKey      = "cutover/do-baseline-fra1.tfstate"
	stateRegion   = "us-east-1" // placeholder; ignored by Spaces (routing is by endpoint)
	stateEndpoint = "https://fra1.digitaloceanspaces.com"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "render: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	secrets := catalog.DOBaselineSecrets{
		SpacesAccessKey:  os.Getenv("DO_SPACES_ACCESS_KEY"),
		SpacesSecretKey:  os.Getenv("DO_SPACES_SECRET_KEY"),
		BoardDatabaseURL: os.Getenv("DO_BOARD_DATABASE_URL"),
		EmbedTokenSecret: os.Getenv("DO_MCP_EMBED_TOKEN"),
		// Optional durable-origin reserved IP: when both set, the mcp bootstrap claims
		// the reserved IP to itself so an autoscale roll keeps the same Cloudflare origin.
		DigitalOceanToken: os.Getenv("DIGITALOCEAN_TOKEN"),
		McpReservedIP:     os.Getenv("DO_MCP_RESERVED_IP"),
	}
	for name, v := range map[string]string{
		"DO_SPACES_ACCESS_KEY":  secrets.SpacesAccessKey,
		"DO_SPACES_SECRET_KEY":  secrets.SpacesSecretKey,
		"DO_BOARD_DATABASE_URL": secrets.BoardDatabaseURL,
		"DO_MCP_EMBED_TOKEN":    secrets.EmbedTokenSecret,
	} {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("missing env %s — export it from Secrets Manager (see cutover/README.md)", name)
		}
	}

	// DURABLE render (pd-MIG-CUTOVER-F5): render the FULL per-service bootstrap for
	// every droplet so a self-heal/roll boots the real service + edge, not a bare
	// box. Opt-in via DO_FULL_SERVICE_BOOTSTRAPS=1.
	//
	// EPIC-BOOTFETCH-AWS-SM-TO-VAULT (wave 2): sast/mcp/sso now source almost all
	// of their render-time secrets DIRECTLY from Vault via `data
	// "vault_kv_secret_v2"` blocks Terraform resolves at apply time (see
	// DOBaselineVaultDataSources, appended to estate.tf below) — no more
	// operator-exported AWS-SM env vars for those. Only the two sso secrets with
	// no provisioned Vault leaf (VaultOIDCSecret, RunnerPublicKey — see the RISK
	// note in platform_bootstrap_sso_do.go) still come from the environment.
	full := strings.TrimSpace(os.Getenv("DO_FULL_SERVICE_BOOTSTRAPS")) == "1"
	if full {
		secrets.SSOVaultOIDCSecret = os.Getenv("DO_SSO_VAULT_OIDC_SECRET")
		secrets.SSORunnerPublicKey = os.Getenv("DO_SSO_RUNNER_PUBLIC_KEY")
		secrets.SSOSenderEmail = os.Getenv("DO_SSO_SENDER_EMAIL")
		if strings.TrimSpace(secrets.SSOVaultOIDCSecret) == "" {
			return fmt.Errorf("missing env DO_SSO_VAULT_OIDC_SECRET — required for DO_FULL_SERVICE_BOOTSTRAPS (no Vault KV leaf provisioned for it yet; see cutover/README.md)")
		}
	}

	ctx := context.Background()
	// Same descriptor as the requested AssembleHCL(... DOBaselineInput ...) call.
	in := catalog.DOBaselineInput("Frankfurt", "x86_64", "ubuntu", "1.30")
	// EdgeTLSOrigins (pd-MIG-CUTOVER-F4-PREP): opt-in via DO_EDGE_TLS_ORIGINS=1 so
	// each Cloudflare-routed origin (sso/backend/mcp) renders an nginx :443 TLS
	// terminator and can be flipped onto its DO origin behind Cloudflare "Full".
	// See docs/cutover/CLOUDFLARE-CUTOVER.md. Off by default (0 change to base estate).
	edgeTLS := strings.TrimSpace(os.Getenv("DO_EDGE_TLS_ORIGINS")) == "1"

	// VaultHA (pd-MIG-VAULT-HA-HARDEN Phase 0): opt-in via DO_VAULT_HA=1 so the
	// baseline appends the 3-node Raft Vault droplet cluster (vaultha_droplet_do.go).
	// Off by default => 0 change to the deployed estate. Region/Size/Image/VPCRef are
	// resolved by AssembleDOBaseline via the catalog, not set here.
	//
	// SEAL = SHAMIR (owner decision 2026-07-07): the AWS-KMS auto-unseal bridge (a
	// review-flagged security item — static AWS creds baked into the droplet's
	// systemd env) has been dropped. No seal stanza is rendered; every node requires
	// a MANUAL unseal (3-of-5 key shares held by the owner) after a restart/reboot —
	// see the unseal runbook in the secrets-manager repo. DO_VAULT_SEAL can still
	// select seal=transit for a future opt-in.
	vaultHA := strings.TrimSpace(os.Getenv("DO_VAULT_HA")) == "1"
	var vaultSpec catalog.VaultDropletSpec
	if vaultHA {
		seal := catalog.VaultSealMode(strings.TrimSpace(os.Getenv("DO_VAULT_SEAL")))
		if seal == "" {
			seal = catalog.VaultSealShamir
		}
		vaultSpec.Seal = seal
		// Optional stable public addresses so beta-vault A-record / a DO LB origin
		// survives a droplet roll (durable-DO-edge memo). Off unless DO_VAULT_RESERVED_IPS=1.
		vaultSpec.ReservedIPs = strings.TrimSpace(os.Getenv("DO_VAULT_RESERVED_IPS")) == "1"
		// The go-discover DO tag auto-join needs DIGITALOCEAN_TOKEN in the droplet env.
		// The catalog emits the placeholder line
		//   Environment=DIGITALOCEAN_TOKEN=${DIGITALOCEAN_TOKEN}
		// which is NOT valid inside a terraform heredoc (HCL parses ${...} as an
		// interpolation of an undeclared symbol and errors), so the harness substitutes
		// the real token into estate.tf at RENDER time (generated/ is gitignored). Fail
		// fast so a bad render never reaches terraform.
		if strings.TrimSpace(os.Getenv("DIGITALOCEAN_TOKEN")) == "" {
			return fmt.Errorf("missing env DIGITALOCEAN_TOKEN — required for DO_VAULT_HA=1 (vault raft auto-join by DO tag; source from beta-DigitalOceanToken)")
		}
	}

	// PrivateDBHost: reach pyx-main-db over the shared VPC private endpoint (the
	// mesh_app secret stores the public host).
	docs, err := catalog.AssembleDOBaseline(ctx, catalog.MustEmbedded(), in, secrets, catalog.DOBaselineOptions{PrivateDBHost: true, EdgeTLSOrigins: edgeTLS, FullServiceBootstraps: full, VaultHA: vaultHA, VaultHASpec: vaultSpec})
	if err != nil {
		return fmt.Errorf("assemble DO baseline: %w", err)
	}

	outDir := "cutover/generated"
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", outDir, err)
	}

	// backend.tf — S3-compatible (DigitalOcean Spaces) backend + required_providers +
	// provider config. The DO token and Spaces creds come from the environment, NOT
	// the file, so nothing secret is committed or rendered. The terraform s3 backend
	// reads its credentials from AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY at
	// init/plan/apply time; export the Spaces keys into those (see cutover/README.md).
	//
	// A Terraform module may have only ONE required_providers block (a second one
	// is a hard error), so `vault` is merged into THIS SAME block — never a
	// separate one — whenever the full render needs it (EPIC-BOOTFETCH-AWS-SM-
	// TO-VAULT: sast always; mcp/sso once DOBaselineVaultDataSources is non-empty).
	// The vault provider itself needs no explicit `provider "vault" {}` block: it
	// auto-configures from VAULT_ADDR/VAULT_TOKEN (or VAULT_ROLE_ID+VAULT_SECRET_ID
	// via a CI OIDC login step) in the environment, exactly like `digitalocean`
	// reads DIGITALOCEAN_TOKEN below.
	vaultRequiredProvider := ""
	if full && len(catalog.DOBaselineVaultDataSources()) > 0 {
		vaultRequiredProvider = `    vault = {
      source  = "hashicorp/vault"
      version = "~> 4.0"
    }
`
	}
	backend := fmt.Sprintf(`terraform {
  backend "s3" {
    bucket   = %q
    key      = %q
    region   = %q # placeholder; DigitalOcean Spaces ignores it (routing is by endpoint)
    endpoints = { s3 = %q }

    # DigitalOcean Spaces is S3-compatible but not AWS: skip all AWS-specific checks.
    skip_credentials_validation = true
    skip_metadata_api_check     = true
    skip_region_validation      = true
    skip_requesting_account_id  = true
    use_path_style              = true

    # No DynamoDB on Spaces — lock via a native S3 lockfile (terraform >= 1.11).
    use_lockfile = true

    # Credentials (Spaces keys) come from the environment, never committed:
    #   AWS_ACCESS_KEY_ID     = <beta-DigitalOceanSpacesKeys access_key>
    #   AWS_SECRET_ACCESS_KEY = <beta-DigitalOceanSpacesKeys secret_key>
  }
  required_providers {
    digitalocean = {
      source  = "digitalocean/digitalocean"
      version = "~> 2.0"
    }
%s  }
}

# Provider credentials come from the environment (never committed):
#   DIGITALOCEAN_TOKEN                       (beta-DigitalOceanToken)
#   SPACES_ACCESS_KEY_ID / SPACES_SECRET_ACCESS_KEY (beta-DigitalOceanSpacesKeys)
#   VAULT_ADDR / VAULT_TOKEN (or VAULT_ROLE_ID + VAULT_SECRET_ID via CI OIDC)
provider "digitalocean" {
}
`, stateBucket, stateKey, stateRegion, stateEndpoint, vaultRequiredProvider)
	if err := writeFile(filepath.Join(outDir, "backend.tf"), backend); err != nil {
		return err
	}

	// variables.tf — do_ssh_keys is passed at apply time (-var 'do_ssh_keys=["57496891"]').
	// When the DURABLE render is on, the still-unmigrated var-model secrets
	// (mcp's board DB URL, obs/backend/vpn) reference ${var.<x>}; declare one
	// sensitive variable per name so the estate `terraform validate`s. Values are
	// supplied at apply time via -var from Secrets Manager (see cutover/README.md).
	// sast/mcp's Spaces+embed-token/sso's migrated secrets are NOT here — they are
	// `data "vault_kv_secret_v2"` blocks Terraform resolves directly from Vault
	// (see estate.tf / DOBaselineVaultDataSources; the `vault` provider itself
	// authenticates via VAULT_ADDR/VAULT_TOKEN or VAULT_ROLE_ID+VAULT_SECRET_ID in
	// the environment/CI OIDC, never a -var here).
	vars := `variable "do_ssh_keys" {
  description = "DigitalOcean SSH key IDs injected into every droplet template."
  type        = list(string)
}
`
	if full {
		var vb strings.Builder
		vb.WriteString(vars)
		for _, name := range catalog.DOBaselineVariableNames() {
			fmt.Fprintf(&vb, "\nvariable %q {\n  type      = string\n  sensitive = true\n}\n", name)
		}
		vars = vb.String()
	}
	if err := writeFile(filepath.Join(outDir, "variables.tf"), vars); err != nil {
		return err
	}

	// estate.tf — the deterministic assembled resources.
	header := "# GENERATED by cutover/render.go — do NOT edit by hand.\n" +
		"# Reproduce: go run ./cutover/render.go  (see cutover/README.md)\n" +
		"# Source: catalog.DOBaselineInput(\"Frankfurt\",\"x86_64\",\"ubuntu\",\"1.30\")\n\n"
	estate := header + strings.Join(docs, "\n\n") + "\n"
	if vaultHA {
		// Substitute the catalog's DIGITALOCEAN_TOKEN placeholder (see the DO_VAULT_HA
		// block above). indentUserData HCL-escapes it to $${DIGITALOCEAN_TOKEN} so
		// terraform passes the LITERAL ${DIGITALOCEAN_TOKEN} through to the systemd
		// drop-in — where nothing ever expands it and raft auto-join silently fails.
		// Inline the real token at render time instead (generated/ is gitignored).
		doToken := strings.TrimSpace(os.Getenv("DIGITALOCEAN_TOKEN"))
		for _, placeholder := range []string{
			"Environment=DIGITALOCEAN_TOKEN=$${DIGITALOCEAN_TOKEN}",
			"Environment=DIGITALOCEAN_TOKEN=${DIGITALOCEAN_TOKEN}",
		} {
			estate = strings.ReplaceAll(estate, placeholder, "Environment=DIGITALOCEAN_TOKEN="+doToken)
		}
	}
	if err := writeFile(filepath.Join(outDir, "estate.tf"), estate); err != nil {
		return err
	}

	fmt.Printf("rendered %d resource documents to %s/estate.tf (+ backend.tf, variables.tf)\n", len(docs), outDir)
	fmt.Printf("next: (cd %s && terraform init && terraform plan -var 'do_ssh_keys=[\"57496891\"]')\n", outDir)
	return nil
}

// NOTE (EPIC-BOOTFETCH-AWS-SM-TO-VAULT, wave 2): this file used to normalize a
// libpq keycloak-db URI to the jdbc form Keycloak/pgjdbc requires (dropping
// embedded credentials) at Go-render time, because the operator exported the
// raw AWS-SM secret value into DO_SSO_KCDB_URL. Now that KC_DB_URL is a Vault
// `data "vault_kv_secret_v2"` reference resolved by Terraform (see
// platform_bootstrap_sso_do.go), that normalization can no longer happen in
// Go. RISK: secret/infra/staging/sso/keycloak-db-url's "url" key must already
// be stored in jdbc form (jdbc:postgresql://host:port/db?sslmode=require, NO
// embedded credentials) — if it is still the libpq form
// (postgres://user:pass@host:port/db?...), Keycloak will crash-loop exactly as
// it did before PR #10 fixed this the first time. Verify/fix the stored Vault
// value before rolling sso; do not assume it was migrated automatically.

func writeFile(path, content string) error {
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
