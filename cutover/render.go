// Command render is the COMMITTED, reproducible renderer for the AWS -> DigitalOcean
// cutover baseline (pd-MIG-CUTOVER-F2-02). It replaces the ad-hoc "re-render catalog
// HCL into a throwaway /tmp dir on every apply" workflow: the estate is rendered
// deterministically into cutover/generated/ from the committed catalog descriptor
// catalog.DOBaselineInput, and applied against the persistent S3 state
// (s3://pyxcloud-terraform-state/cutover/do-baseline-fra1.tfstate).
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

// stateBucket / stateKey / stateRegion pin the persistent S3 backend. This is the
// ONE authoritative state for the cutover baseline — the whole point of the
// harness is that state (and now the config) persists, not a /tmp render.
const (
	stateBucket = "pyxcloud-terraform-state"
	stateKey    = "cutover/do-baseline-fra1.tfstate"
	stateRegion = "eu-west-1"
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

	ctx := context.Background()
	// Same descriptor as the requested AssembleHCL(... DOBaselineInput ...) call.
	in := catalog.DOBaselineInput("Frankfurt", "x86_64", "ubuntu", "1.30")
	// EdgeTLSOrigins (pd-MIG-CUTOVER-F4-PREP): opt-in via DO_EDGE_TLS_ORIGINS=1 so
	// each Cloudflare-routed origin (sso/backend/mcp) renders an nginx :443 TLS
	// terminator and can be flipped onto its DO origin behind Cloudflare "Full".
	// See docs/cutover/CLOUDFLARE-CUTOVER.md. Off by default (0 change to base estate).
	edgeTLS := strings.TrimSpace(os.Getenv("DO_EDGE_TLS_ORIGINS")) == "1"
	// PrivateDBHost: reach pyx-main-db over the shared VPC private endpoint (the
	// mesh_app secret stores the public host).
	docs, err := catalog.AssembleDOBaseline(ctx, catalog.MustEmbedded(), in, secrets, catalog.DOBaselineOptions{PrivateDBHost: true, EdgeTLSOrigins: edgeTLS})
	if err != nil {
		return fmt.Errorf("assemble DO baseline: %w", err)
	}

	outDir := "cutover/generated"
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", outDir, err)
	}

	// backend.tf — S3 backend + required_providers + provider config. The DO token
	// and Spaces creds come from the environment (DIGITALOCEAN_TOKEN / SPACES_*),
	// NOT the file, so nothing secret is committed or rendered.
	backend := fmt.Sprintf(`terraform {
  backend "s3" {
    bucket = %q
    key    = %q
    region = %q
  }
  required_providers {
    digitalocean = {
      source  = "digitalocean/digitalocean"
      version = "~> 2.0"
    }
  }
}

# Provider credentials come from the environment (never committed):
#   DIGITALOCEAN_TOKEN                       (beta-DigitalOceanToken)
#   SPACES_ACCESS_KEY_ID / SPACES_SECRET_ACCESS_KEY (beta-DigitalOceanSpacesKeys)
provider "digitalocean" {
}
`, stateBucket, stateKey, stateRegion)
	if err := writeFile(filepath.Join(outDir, "backend.tf"), backend); err != nil {
		return err
	}

	// variables.tf — do_ssh_keys is passed at apply time (-var 'do_ssh_keys=["57496891"]').
	vars := `variable "do_ssh_keys" {
  description = "DigitalOcean SSH key IDs injected into every droplet template."
  type        = list(string)
}
`
	if err := writeFile(filepath.Join(outDir, "variables.tf"), vars); err != nil {
		return err
	}

	// estate.tf — the deterministic assembled resources.
	header := "# GENERATED by cutover/render.go — do NOT edit by hand.\n" +
		"# Reproduce: go run ./cutover/render.go  (see cutover/README.md)\n" +
		"# Source: catalog.DOBaselineInput(\"Frankfurt\",\"x86_64\",\"ubuntu\",\"1.30\")\n\n"
	estate := header + strings.Join(docs, "\n\n") + "\n"
	if err := writeFile(filepath.Join(outDir, "estate.tf"), estate); err != nil {
		return err
	}

	fmt.Printf("rendered %d resource documents to %s/estate.tf (+ backend.tf, variables.tf)\n", len(docs), outDir)
	fmt.Printf("next: (cd %s && terraform init && terraform plan -var 'do_ssh_keys=[\"57496891\"]')\n", outDir)
	return nil
}

func writeFile(path, content string) error {
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
