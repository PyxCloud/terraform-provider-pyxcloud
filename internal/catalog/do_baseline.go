package catalog

import (
	"context"
	"fmt"
	"strings"
)

// do_baseline.go — pd-MIG-CUTOVER-F2-02 (EPIC-AWS-TO-DO-MIGRATION).
//
// The AWS -> DigitalOcean cutover baseline (blue/green, no DNS/traffic, AWS
// untouched) was historically applied by re-rendering catalog HCL into a throw-
// away /tmp directory on every apply, with only the S3 state persisting
// (s3://pyxcloud-terraform-state/cutover/do-baseline-fra1.tfstate, 11 resources).
// That is not reproducible: re-applies and user_data changes are ephemeral.
//
// This file is the COMMITTED, deterministic source for that baseline. It renders
// the EXACT deployed estate so a `terraform plan` against the existing S3 state is
// clean, and it makes the mcp control-plane droplet DURABLE across replacement by
// baking a correct BOARD_DATABASE_URL (the mesh_app URL) into the launch template.
//
// WHY A DEDICATED ASSEMBLER (not the generic AssembleHCL scale-group path):
// the canonical `virtual-machine-scale-group` descends to a DOKS
// `digitalocean_kubernetes_cluster` node-pool on DigitalOcean (scalegroup.go).
// The live cutover estate was instead applied as `digitalocean_droplet_autoscale`
// groups (a droplet fleet with a CPU target, self-healing floor of 1). Rendering
// the DOKS shape against the existing droplet-autoscale state would plan a full
// destroy+recreate of every service — unacceptable for a live blue estate. This
// assembler therefore emits the droplet-autoscale shape that MATCHES state, so the
// harness is reconciled with reality and the plan stays additive (<= 1 change).
//
// The estate this reproduces (see the S3 state, serial 12):
//   - 1 VPC (passo-do-baseline-net, 10.0.1.0/24, fra1)
//   - 1 firewall (passo-do-baseline-sg): inbound 443, egress icmp/tcp/udp all
//   - 2 managed PG clusters (pyx-main-db, keycloak-db), pg 17, db-s-2vcpu-4gb, 2 nodes
//   - 6 droplet-autoscale groups: backend / mcp / obs / sast / sso / vpn
//   - no per-service public load balancers or platform certificate resources;
//     the private VPC edge SNI-routes VPN traffic to origin tags
//   - 1 Spaces bucket (pyx-artifacts-fra1) — the mcp/backend artifact store
//     (INCLUDED now that beta-DigitalOceanSpacesKeys exists)

// doBaselineName is the estate prefix used for the VPC/firewall names, matching
// the deployed state resource names so the plan is import-clean.
const doBaselineName = "passo-do-baseline"

// doBaselineSpacesBucket is the DO Spaces bucket the droplets fetch their release
// artifact from (aws s3 cp --endpoint-url against fra1.digitaloceanspaces.com).
const doBaselineSpacesBucket = "pyx-artifacts-fra1"

// doBaselineEnv is the deploy-environment token the FullServiceBootstraps var-model
// specs (mcp/sast/backend/vpn) and the sso literal spec use to derive their public
// hostnames (<env>-auth/<env>-api/<env>-mcp.*). Must stay in lockstep with
// doEdgeOrigins' staging-* hostnames (beta-* is retired; see doEdgeOrigins).
const doBaselineEnv = "staging"

// DOBaselineService is one droplet-autoscale group in the cutover baseline.
type DOBaselineService struct {
	// Name is the autoscale-group name and matches the deployed state.
	Name string
	// Tag is the droplet tag the firewall and private edge select on.
	Tag string
	// CPU / RAM are the requested sizing, resolved to a concrete droplet SKU by
	// the catalog (the SAME ResolveSKU path every VM uses) — never hand-picked.
	CPU int
	RAM int
	// Durable marks the mcp service whose user_data must carry the mesh_app
	// BOARD_DATABASE_URL so a droplet replacement re-bootstraps correctly.
	Durable bool
}

const (
	stagingFEServiceName = "staging-fe"
	stagingFEServiceTag  = "pyx-staging-fe"
)

// DOBaselineServices is the canonical ordered list of the six original cutover
// groups plus the private staging-fe standalone runtime. Deterministic (slice,
// not map) so the emitted HCL is stable.
func DOBaselineServices() []DOBaselineService {
	return []DOBaselineService{
		{Name: "backend", Tag: "pyx-backend", CPU: 2, RAM: 4},
		{Name: "mcp", Tag: "pyx-mcp", CPU: 2, RAM: 4, Durable: true},
		{Name: "obs", Tag: "pyx-obs", CPU: 4, RAM: 8},
		{Name: "sast", Tag: "pyx-sast", CPU: 2, RAM: 4},
		{Name: "sso", Tag: "pyx-sso", CPU: 2, RAM: 4},
		{Name: "vpn", Tag: "pyx-vpn", CPU: 2, RAM: 2},
		{Name: stagingFEServiceName, Tag: stagingFEServiceTag, CPU: 2, RAM: 2},
	}
}

// doEdgeOrigins is the per-hostname -> DO service origin map for private VPC
// edge routing, derived from the AWS shared-ALB host-header
// rules (beta-pyx-shared-alb): beta-auth -> keycloak_tg:8080 (sso),
// beta-api -> api_tg:8080 (backend), mcp.passo.build -> mcp_tg:8787 (mcp). The
// obs origin already carries its own nginx :443 (VPN-only) so it is NOT here.
// When DOBaselineOptions.EdgeTLSOrigins is set, each service below gets an nginx
// :443 terminator appended to its user_data so it can serve the VPC edge's SNI
// route. Deterministic slice.
type doEdgeOrigin struct {
	Service      string // matches DOBaselineService.Name
	Hostname     string // private-DNS FQDN the VPC edge routes to this origin
	UpstreamPort int    // local plain-HTTP service port
}

func doEdgeOrigins() []doEdgeOrigin {
	// staging estate canonical hostnames (beta-* is retired). Deleting the beta-*
	// DNS while these still said beta- is what broke the running staging edge
	// (auth/api origins + the frontend backend-proxy UPSTREAM_BASE) — keep these
	// in lockstep with the staging DNS. Prod uses the un-prefixed names via the
	// native pyxcloud_environment path (pyxcloud-production), not this harness.
	return []doEdgeOrigin{
		{Service: "sso", Hostname: "staging-auth.pyxcloud.io", UpstreamPort: 8080},
		{Service: "backend", Hostname: "staging-api.pyxcloud.io", UpstreamPort: 8080},
		{Service: "mcp", Hostname: "staging-mcp.passo.build", UpstreamPort: 8787},
	}
}

// edgeOriginByService returns the doEdgeOrigin for a service name, or nil if the
// service is not an edge-routed origin (obs/sast/vpn).
func edgeOriginByService(svcName string) *doEdgeOrigin {
	for _, o := range doEdgeOrigins() {
		if o.Service == svcName {
			o := o
			return &o
		}
	}
	return nil
}

// doBaselineEgressRules is the shared outbound rule set (icmp/tcp/udp all) every
// baseline firewall carries.
func doBaselineEgressRules() string {
	return `
  outbound_rule {
    protocol              = "icmp"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }
  outbound_rule {
    protocol              = "tcp"
    port_range            = "1-65535"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }
  outbound_rule {
    protocol              = "udp"
    port_range            = "1-65535"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }`
}

// DOBaselineInput is the catalog-native descriptor for the cutover baseline.
// It mirrors the AssembleInput surface (name/provider/region/components) so the
// harness reads the same way as the generic estate, while AssembleDOBaseline
// renders the droplet-autoscale shape that matches the live S3 state.
//
// region is the DO slug ("fra1"); arch/os/kubernetesVersion take the canonical
// defaults when empty (kubernetesVersion is unused by the droplet path but kept
// for signature parity with FullEstateInput / the migration dry-run).
func DOBaselineInput(region, arch, os, kubernetesVersion string) AssembleInput {
	arch = strings.TrimSpace(arch)
	if arch == "" {
		arch = "x86_64"
	}
	os = strings.TrimSpace(os)
	if os == "" {
		os = "ubuntu"
	}
	comps := make([]AssembleComponent, 0, len(DOBaselineServices()))
	for _, s := range DOBaselineServices() {
		comps = append(comps, AssembleComponent{
			Name: s.Name,
			Type: "virtual-machine-scale-group",
			ScaleGroup: &AssembleScaleGroup{
				Architecture: arch,
				CPU:          itoa(s.CPU),
				RAM:          itoa(s.RAM),
				OS:           os,
				Min:          1, Max: 1, Desired: 1,
				Health:            HealthEC2,
				KubernetesVersion: strings.TrimSpace(kubernetesVersion),
			},
		})
	}
	return AssembleInput{
		Name:       doBaselineName,
		Provider:   ProviderDigitalOcean,
		Region:     region,
		CIDR:       "10.0.1.0/24",
		Subnets:    []string{"10.0.1.0/24"},
		Expose:     []int{443},
		Components: comps,
	}
}

// DOBaselineSecrets carries the render-time-injected credentials so NOTHING
// secret is committed. Every field is fetched from AWS Secrets Manager by the
// harness (cutover/render.go) at render time and baked into the launch template,
// exactly as EMBED_TOKEN_SECRET already was — the droplet has no AWS role, so it
// cannot fetch these itself.
type DOBaselineSecrets struct {
	// SpacesAccessKey / SpacesSecretKey are beta-DigitalOceanSpacesKeys — the S3
	// (SigV4) client credentials used ONLY to pull the release artifact from DO
	// Spaces at boot. Also used for the Spaces bucket provider config.
	SpacesAccessKey string
	SpacesSecretKey string
	// BoardDatabaseURL is beta-DO-pyx-main-db-url (the mesh_app URL) — the DURABLE
	// fix: BOARD_DATABASE_URL now points at mesh_app on pyx-main-db, NOT doadmin /
	// defaultdb / beta-DO-pyx-db-password. Injected here so a droplet replacement
	// re-bootstraps with the correct DB identity.
	BoardDatabaseURL string
	// EmbedTokenSecret is beta/passobuild-mcp-embed-token — the MCP embed token,
	// injected at render time (unchanged behaviour, kept durable).
	EmbedTokenSecret string
	// DigitalOceanToken (beta-DigitalOceanToken) + McpReservedIP let the mcp droplet
	// CLAIM a stable DO reserved IP to itself on boot. With Cloudflare pointing at the
	// reserved IP, an autoscale roll no longer needs a DNS repoint: the fresh droplet
	// reassigns the reserved IP to itself. Both empty => claim step is skipped (no-op).
	DigitalOceanToken string
	McpReservedIP     string

	// --- SSO (Keycloak) secrets (pd-MIG-CUTOVER durable edge) ---
	// EPIC-BOOTFETCH-AWS-SM-TO-VAULT (wave 2): most sso secrets (keycloak-db URL/
	// creds, the bootstrap admin password, DO Spaces keys, SMTP creds) are now
	// resolved by Terraform directly from Vault via `data "vault_kv_secret_v2"`
	// blocks rendered alongside the sso user_data (see
	// SSODOBootstrapSpec.SSODOVaultDataSources in platform_bootstrap_sso_do.go) —
	// they are NO LONGER fields here. Only the two secrets with no provisioned
	// Vault leaf remain literal-injected-at-render, exactly as before:
	SSOVaultOIDCSecret string // pyx Vault OIDC client secret (file vault) — no Vault KV leaf provisioned yet
	SSORunnerPublicKey string // deploy-runner stable SSH public key (optional) — the only related leaf holds a private_key, do not wire blindly
	SSOSenderEmail     string // passo.build SES From address (optional; not a secret, kept for convenience)
}

// UsePrivateDBHost, when true, rewrites the BoardDatabaseURL host to the DO
// managed-PG PRIVATE endpoint (private-<host>) so the mcp droplet reaches the DB
// over the shared VPC rather than the public internet. The mesh_app secret stores
// the public host; same-VPC connectivity wants the private one. Default (zero
// value = false) uses the URL verbatim; the harness enables it.
func (s DOBaselineSecrets) privateURL(enable bool) string {
	u := strings.TrimSpace(s.BoardDatabaseURL)
	if !enable || u == "" {
		return u
	}
	// postgres://user:pw@HOST:port/db?query — rewrite HOST only if not already private.
	at := strings.LastIndex(u, "@")
	if at < 0 {
		return u
	}
	head, tail := u[:at+1], u[at+1:] // tail = host:port/db?query
	if strings.HasPrefix(tail, "private-") {
		return u
	}
	return head + "private-" + tail
}

// DOBaselineOptions tunes the render. Zero value is the harness default.
type DOBaselineOptions struct {
	// PrivateDBHost rewrites the mesh_app URL to the private VPC endpoint (same
	// VPC as the droplets). The harness sets this true.
	PrivateDBHost bool
	// EdgeTLSOrigins, when true, appends an nginx :443 TLS terminator (the obs
	// pattern, see edge_tls_terminator.go) to each private edge-routed origin
	// service (sso/backend/mcp per doEdgeOrigins).
	// A service that had no user_data (backend/sso in the base harness) gets a
	// standalone terminator script; mcp gets the terminator appended after its
	// durable bootstrap. Off by default (0 change to the base estate).
	EdgeTLSOrigins bool
	// FullServiceBootstraps, when true, renders the COMPLETE per-service DO
	// bootstrap for EVERY service (mcp/sso/obs/sast/backend/vpn) via the catalog
	// Render*DO* functions, so a droplet self-heal/roll boots the real service
	// rather than a bare box. This is the DURABLE render (pd-MIG-CUTOVER-F5): the
	// committed source of truth for what each droplet template must contain.
	//
	// When set, the var-model services (mcp/obs/sast/backend/vpn) reference their
	// secrets as ${var.<x>} (resolved by terraform at apply from -var, sourced from
	// Secrets Manager), while sso inlines its secret values from DOBaselineSecrets.
	// EdgeTLSOrigins is implied for sso/backend/mcp (the :443 terminator is appended
	// to their full bootstrap). Off by default so the legacy mcp-only render is
	// unchanged.
	FullServiceBootstraps bool
	// LBTermination is retained for input compatibility only. Staging no longer
	// renders any service load balancer/certificate, regardless of this value.
	// TLS terminates at the private origins reached through the VPC edge.
	LBTermination bool
	// VaultHA, when true, appends the 3-node Raft Vault droplet cluster
	// (vaultha_droplet_do.go) to the baseline: 3 fixed digitalocean_droplet nodes
	// with a block volume each, a private-only :8200/:8201 firewall, cloud-auto-join
	// peer discovery by DO tag, and a configurable seal stanza (AWS-KMS bridge by
	// default). This is Phase 0 of the Vault-HA-on-DO migration
	// (pd-MIG-VAULT-HA-HARDEN). OFF BY DEFAULT so it never perturbs the existing
	// baseline render (0 change to the deployed estate). The harness gates it on the
	// DO_VAULT_HA=1 env flag; VaultHASpec/VaultHASecrets below carry the render-time
	// seal + auto-join credentials (never committed, never in tf state).
	VaultHA bool
	// VaultHASpec tunes the Vault cluster (seal mode, reserved IPs). Only consulted
	// when VaultHA is true. Zero value -> AWS-KMS bridge seal, no reserved IPs.
	VaultHASpec VaultDropletSpec
}

// AssembleDOBaseline renders the cutover baseline as concrete terraform documents
// that MATCH the deployed S3 state (droplet-autoscale shape). This is the
// committed, deterministic replacement for the ad-hoc /tmp renderer.
//
// The mcp droplet's user_data is made durable: BOARD_DATABASE_URL is sourced from
// secrets.BoardDatabaseURL (the mesh_app URL from beta-DO-pyx-main-db-url),
// injected at render time — so a future roll (droplet replacement) re-bootstraps
// against mesh_app, not the stale doadmin/defaultdb URL that was baked into state.
func AssembleDOBaseline(ctx context.Context, cat Catalog, in AssembleInput, secrets DOBaselineSecrets, opts DOBaselineOptions) ([]string, error) {
	if in.Provider != ProviderDigitalOcean {
		return nil, fmt.Errorf("do-baseline: provider must be digitalocean, got %q", in.Provider)
	}
	if strings.TrimSpace(in.Region) == "" {
		return nil, fmt.Errorf("do-baseline: region is required")
	}
	if secrets.SpacesAccessKey == "" || secrets.SpacesSecretKey == "" {
		return nil, fmt.Errorf("do-baseline: Spaces keys are required (beta-DigitalOceanSpacesKeys)")
	}
	if secrets.BoardDatabaseURL == "" {
		return nil, fmt.Errorf("do-baseline: BoardDatabaseURL is required (beta-DO-pyx-main-db-url / mesh_app)")
	}
	if secrets.EmbedTokenSecret == "" {
		return nil, fmt.Errorf("do-baseline: EmbedTokenSecret is required (beta/passobuild-mcp-embed-token)")
	}

	// Resolve the abstract region_name (e.g. "Frankfurt") to the concrete DO slug
	// (fra1) via the catalog — the SAME resolution every component uses. All DO
	// resource `region` attributes and SKU/image lookups use the concrete slug.
	regionRow, err := cat.ResolveRegion(ctx, in.Region, ProviderDigitalOcean)
	if err != nil {
		return nil, fmt.Errorf("do-baseline: resolve region %q: %w", in.Region, err)
	}
	csp, cspRegion := regionRow.CSP, regionRow.CSPRegion
	region := cspRegion
	var docs []string

	// 1. VPC (matches passo-do-baseline-net).
	docs = append(docs, fmt.Sprintf(`resource "digitalocean_vpc" %q {
  name     = %q
  region   = %q
  ip_range = %q
}`, doBaselineName+"-net", doBaselineName+"-net", region, in.CIDR))

	// 2. Firewall (matches passo-do-baseline-sg): staging has no public service
	// ingress. The private VPC edge is the only caller allowed onto origin TLS.
	tags := make([]string, 0, len(DOBaselineServices()))
	for _, s := range DOBaselineServices() {
		if s.Name != stagingFEServiceName {
			tags = append(tags, s.Tag)
		}
	}
	var chunk []string
	for i, tag := range tags {
		chunk = append(chunk, tag)
		if len(chunk) == 5 || i == len(tags)-1 {
			suffix := ""
			if len(tags) > 5 && i >= 5 {
				suffix = fmt.Sprintf("-%d", (i/5)+1)
			}
			docs = append(docs, fmt.Sprintf(`resource "digitalocean_firewall" %q {
  name = %q
  tags = %s

  inbound_rule {
	protocol    = "tcp"
	port_range  = "443"
	source_tags = ["pyx-edge"]
  }
%s
}`, doBaselineName+"-sg"+suffix, doBaselineName+"-sg"+suffix, hclStringList(chunk), doBaselineEgressRules()))
			chunk = nil
		}
	}

	// staging-fe is a private origin: only the VPC edge routers may reach its
	// TLS listener. It must never inherit the shared public :443 rule.
	docs = append(docs, fmt.Sprintf(`resource "digitalocean_firewall" %q {
  name = %q
  tags = [%q]

  inbound_rule {
    protocol    = "tcp"
    port_range  = "443"
    source_tags = ["pyx-edge"]
  }

  inbound_rule {
    protocol         = "tcp"
    port_range       = "22"
    source_addresses = [%q]
  }
%s
}`, doBaselineName+"-"+stagingFEServiceName+"-sg", doBaselineName+"-"+stagingFEServiceName+"-sg",
		stagingFEServiceTag, in.CIDR, doBaselineEgressRules()))

	// 3. Managed PG clusters (pyx-main-db + keycloak-db), pg 17, node_count = 1.
	for _, db := range []string{"pyx-main-db", "keycloak-db"} {
		size := "db-s-1vcpu-2gb"
		if db == "pyx-main-db" {
			size = "db-s-2vcpu-4gb"
		}
		docs = append(docs, fmt.Sprintf(`resource "digitalocean_database_cluster" %q {
  name                 = %q
  engine               = "pg"
  version              = "17"
  size                 = %q
  region               = %q
  node_count           = 1
  private_network_uuid = digitalocean_vpc.%s.id
  tags                 = ["pyxcloud"]
}`, db, db, size, region, doBaselineName+"-net"))
	}

	// 4. Droplet-autoscale groups (6 services), sized via the catalog SKU resolver.
	for _, svc := range DOBaselineServices() {
		row, err := cat.ResolveSKU(ctx, csp, cspRegion, "x86_64", svc.CPU, svc.RAM)
		if err != nil {
			return nil, fmt.Errorf("do-baseline: resolve SKU for %s (%dvCPU/%dGiB): %w", svc.Name, svc.CPU, svc.RAM, err)
		}
		img, err := cat.ResolveImage(ctx, csp, cspRegion, "ubuntu", "24.04", "x86_64")
		if err != nil {
			return nil, fmt.Errorf("do-baseline: resolve image for %s: %w", svc.Name, err)
		}
		userData := ""
		heredocEscaped := false // FullServiceBootstraps var-model needs $$-escaping, not $$-everything.
		switch {
		case opts.FullServiceBootstraps:
			// DURABLE render: the COMPLETE per-service bootstrap so a self-heal/roll
			// boots the real service. Var-model services keep ${var.<x>} refs (resolved
			// at apply from -var); sso inlines its secret values from DOBaselineSecrets.
			ud, err := renderFullServiceBootstrap(svc.Name, secrets, opts)
			if err != nil {
				return nil, err
			}
			userData = ud
			heredocEscaped = true
		case svc.Durable:
			// Legacy mcp-only durable render (literal mesh_app URL, reserved-IP claim,
			// re-fetch-on-restart). Kept for the base harness path (FullServiceBootstraps off).
			userData = renderMCPUserData(secrets, opts)
			// Append the origin :443 terminator when requested. The VPC edge is an
			// SNI router, so TLS always terminates on the private origin.
			if opts.EdgeTLSOrigins {
				snip, terr := edgeTerminatorFor(svc.Name)
				if terr != nil {
					return nil, terr
				}
				if snip != "" {
					userData = userData + "\n\n" + snip
				}
			}
		case opts.EdgeTLSOrigins:
			// Base harness (no full bootstrap): standalone terminator for sso/backend.
			snip, terr := edgeTerminatorFor(svc.Name)
			if terr != nil {
				return nil, terr
			}
			if snip != "" {
				userData = "#!/bin/bash\nset -euo pipefail\n" + snip
			}
		}
		udBlock := ""
		if userData != "" {
			body := userData
			if heredocEscaped {
				// Var-model bootstraps: escape bash ${...} but PRESERVE ${var.<x>} so
				// terraform interpolates the injected secrets. Indent for the heredoc.
				body = indentPreserveVars(escapeBashExpansionsForHeredoc(userData))
			} else {
				body = indentUserData(userData)
			}
			udBlock = fmt.Sprintf("\n    user_data = <<-USERDATA\n%s\n    USERDATA\n", body)
		}
		docs = append(docs, fmt.Sprintf(`resource "digitalocean_droplet_autoscale" %q {
  name = %q

  config {
    min_instances             = 1
    max_instances             = 1
    target_cpu_utilization    = 0.6
  }

  droplet_template {
    size               = %q
    region             = %q
    image              = %q
    ssh_keys           = var.do_ssh_keys
    vpc_uuid           = digitalocean_vpc.%s.id
    tags               = [%q]
    with_droplet_agent = true%s
  }
}`, svc.Name, svc.Name, row.Name, region, img.Image, doBaselineName+"-net", svc.Tag, udBlock))
	}

	// 5. No per-service load balancers or platform certificates are rendered for
	// staging. Private DNS sends VPN clients to the VPC edge, which SNI-routes to
	// these origins; origin firewalls admit only the pyx-edge tag.

	// 6. Spaces bucket (pyx-artifacts-fra1) — the release-artifact store. INCLUDED
	//    now that beta-DigitalOceanSpacesKeys exists; the spaces provider creds come
	//    from the same secret (wired in the backend/provider block by the harness).
	docs = append(docs, fmt.Sprintf(`resource "digitalocean_spaces_bucket" "artifacts" {
  name   = %q
  region = %q
  acl    = "private"

  lifecycle {
    prevent_destroy = true
  }
}`, doBaselineSpacesBucket, region))

	// 7. Vault-HA 3-node Raft droplet cluster (Phase 0, pd-MIG-VAULT-HA-HARDEN).
	//    OPT-IN via opts.VaultHA (harness DO_VAULT_HA=1). Off => 0 change to the base
	//    estate. Sized via the SAME catalog SKU/image resolvers every service uses.
	if opts.VaultHA {
		vaultRow, err := cat.ResolveSKU(ctx, csp, cspRegion, "x86_64", 2, 4)
		if err != nil {
			return nil, fmt.Errorf("do-baseline: resolve SKU for vault (2vCPU/4GiB): %w", err)
		}
		vaultImg, err := cat.ResolveImage(ctx, csp, cspRegion, "ubuntu", "24.04", "x86_64")
		if err != nil {
			return nil, fmt.Errorf("do-baseline: resolve image for vault: %w", err)
		}
		vspec := opts.VaultHASpec
		if strings.TrimSpace(vspec.Name) == "" {
			vspec.Name = vaultDropletTag
		}
		vspec.Region = region
		vspec.Size = vaultRow.Name
		vspec.Image = vaultImg.Image
		vspec.VPCRef = "digitalocean_vpc." + doBaselineName + "-net.id"
		vaultDocs, verr := RenderVaultDropletCluster(vspec)
		if verr != nil {
			return nil, fmt.Errorf("do-baseline: vault-ha cluster: %w", verr)
		}
		docs = append(docs, vaultDocs...)
	}

	// 8. Vault `data "vault_kv_secret_v2"` blocks (+ shared provider) for the
	//    FullServiceBootstraps render-time secrets sourced from Vault
	//    (sast/mcp/EMBED_TOKEN/sso — EPIC-BOOTFETCH-AWS-SM-TO-VAULT wave 2). Only
	//    emitted when FullServiceBootstraps actually rendered those services.
	if opts.FullServiceBootstraps {
		docs = append(docs, DOBaselineVaultDataSources()...)
	}

	return docs, nil
}

// mcpUserDataTemplate is the durable mcp bootstrap. %[1]s = Spaces access key,
// %[2]s = Spaces secret key, %[3]s = Spaces bucket, %[4]s = BOARD_DATABASE_URL
// (mesh_app), %[5]s = EMBED token. It keeps :8787, systemd, and the Spaces
// artifact fetch identical to the deployed unit; the ONLY durability change is
// that BOARD_DATABASE_URL is the mesh_app URL, injected at render time.
const mcpUserDataTemplate = `#!/bin/bash
# mcp board-OS MCP server — DigitalOcean droplet bootstrap (F2-02, durable).
# BOARD_DATABASE_URL sources beta-DO-pyx-main-db-url (mesh_app), injected at
# render time (no AWS role on the box), same mechanism as EMBED_TOKEN_SECRET.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
log() { echo "[mcp-bootstrap] $*"; }

log "apt update + base deps"
apt-get update -y
apt-get install -y ca-certificates curl unzip tar coreutils

if ! command -v aws >/dev/null 2>&1; then
  log "install aws cli v2 (spaces client)"
  arch="$(dpkg --print-architecture)"
  case "$arch" in
    amd64) awsurl="https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip" ;;
    arm64) awsurl="https://awscli.amazonaws.com/awscli-exe-linux-aarch64.zip" ;;
    *)     awsurl="https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip" ;;
  esac
  curl -fsSL "$awsurl" -o /tmp/awscliv2.zip
  (cd /tmp && unzip -q awscliv2.zip && ./aws/install --update)
fi

log "claim reserved IP to self (stable Cloudflare origin, survives autoscale roll)"
RESERVED_IP='%[7]s'
if [ -n "$RESERVED_IP" ]; then
  DROPLET_ID="$(curl -fsS http://169.254.169.254/metadata/v1/id || true)"
  if [ -n "$DROPLET_ID" ]; then
    curl -fsS -X POST \
      -H "Authorization: Bearer %[6]s" -H "Content-Type: application/json" \
      "https://api.digitalocean.com/v2/reserved_ips/$RESERVED_IP/actions" \
      -d "{\"type\":\"assign\",\"droplet_id\":$DROPLET_ID}" \
      || log "reserved-ip assign failed (continuing; Cloudflare may need a manual repoint)"
  fi
fi

log "create service user + dirs"
id passobuild-mcp >/dev/null 2>&1 || useradd --system --home /opt/passobuild-mcp --shell /usr/sbin/nologin passobuild-mcp
install -d -m 0755 -o passobuild-mcp -g passobuild-mcp /opt/passobuild-mcp
install -d -m 0755 -o passobuild-mcp -g passobuild-mcp /var/log/passobuild-mcp

log "write artifact fetch script (re-run on every service start for in-place deploys)"
# The fetch is a root-owned script (0700) with the Spaces creds embedded, so systemd can re-pull the
# latest artifact in ExecStartPre. That makes a plain reboot/restart a full deploy — the deploy-mcp-do
# workflow publishes a new tar.gz then reboots the droplet, and self-heal always boots the current
# binary — WITHOUT changing the droplet IP (no Cloudflare DNS repoint, no rebuild).
cat > /usr/local/bin/passobuild-mcp-fetch <<'FETCHEOF'
#!/usr/bin/env bash
set -euo pipefail
export AWS_ACCESS_KEY_ID='%[1]s'
export AWS_SECRET_ACCESS_KEY='%[2]s'
export AWS_DEFAULT_REGION='fra1'
aws s3 cp --endpoint-url https://fra1.digitaloceanspaces.com \
  s3://%[3]s/beta/mcp.tar.gz /tmp/mcp.tar.gz
unset AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY
tar -xzf /tmp/mcp.tar.gz -C /opt/passobuild-mcp --strip-components=1
chown -R passobuild-mcp:passobuild-mcp /opt/passobuild-mcp
chmod +x /opt/passobuild-mcp/passobuild-mcp
FETCHEOF
chmod 0700 /usr/local/bin/passobuild-mcp-fetch
chown root:root /usr/local/bin/passobuild-mcp-fetch

log "initial artifact fetch"
/usr/local/bin/passobuild-mcp-fetch

log "write /etc/passobuild-mcp.env"
umask 027
cat > /etc/passobuild-mcp.env <<'ENVEOF'
NODE_ENV=production
PYXCLOUD_MCP_HTTP_PORT=8787
PYXCLOUD_MCP_PUBLIC_URL=https://staging-mcp.passo.build
PYXCLOUD_MCP_AUTH_ISSUER_URL=https://staging-auth.pyxcloud.io/realms/passobuild
PYXCLOUD_MCP_AUTH_AUDIENCE=https://staging-mcp.passo.build/mcp,passobuild-mcp
PYXCLOUD_MCP_SERVICE_COMMAND_B64=Li9wYXNzb2J1aWxkLW1jcA==
# DURABLE: mesh_app on pyx-main-db (beta-DO-pyx-main-db-url), NOT doadmin/defaultdb.
BOARD_DATABASE_URL=%[4]s
BOARD_ADMIN_ROLES=board-admin
BOARD_DECOMPOSE_MIN_COMPLEXITY=6
BOARD_VERIFY_MIN_COMPLEXITY=9
BOARD_OPTIMIZE_MIN_COMPLEXITY=6
EMBED_TOKEN_SECRET=%[5]s
ENVEOF
chown root:passobuild-mcp /etc/passobuild-mcp.env
chmod 0640 /etc/passobuild-mcp.env
umask 022

log "write start wrapper + systemd unit"
cat > /usr/local/bin/passobuild-mcp-start <<'WRAPEOF'
#!/usr/bin/env bash
set -euo pipefail
cd /opt/passobuild-mcp/app 2>/dev/null || cd /opt/passobuild-mcp
command="$(printf '%%s' "${PYXCLOUD_MCP_SERVICE_COMMAND_B64:-}" | base64 --decode 2>/dev/null || true)"
if [ -z "$command" ]; then
  echo "PYXCLOUD_MCP_SERVICE_COMMAND is not set." >&2
  exit 78
fi
exec bash -lc "$command"
WRAPEOF
chmod 0755 /usr/local/bin/passobuild-mcp-start

cat > /etc/systemd/system/passobuild-mcp.service <<'UNITEOF'
[Unit]
Description=passo.build remote MCP server
After=network-online.target
Wants=network-online.target

[Service]
User=passobuild-mcp
Group=passobuild-mcp
EnvironmentFile=/etc/passobuild-mcp.env
WorkingDirectory=/opt/passobuild-mcp
# Re-fetch the latest artifact before every start (runs as root via '+'), so reboot/self-heal = deploy.
ExecStartPre=+/usr/local/bin/passobuild-mcp-fetch
ExecStart=/usr/local/bin/passobuild-mcp-start
Restart=always
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ReadWritePaths=/opt/passobuild-mcp /var/log/passobuild-mcp
StandardOutput=append:/var/log/passobuild-mcp/service.log
StandardError=append:/var/log/passobuild-mcp/service.log

[Install]
WantedBy=multi-user.target
UNITEOF

log "enable + start"
systemctl daemon-reload
systemctl enable passobuild-mcp.service
systemctl restart passobuild-mcp.service
log "bootstrap done"`

func renderMCPUserData(s DOBaselineSecrets, opts DOBaselineOptions) string {
	return fmt.Sprintf(mcpUserDataTemplate,
		s.SpacesAccessKey,
		s.SpacesSecretKey,
		doBaselineSpacesBucket,
		s.privateURL(opts.PrivateDBHost),
		s.EmbedTokenSecret,
		s.DigitalOceanToken,
		s.McpReservedIP,
	)
}

// indentUserData prepares the bootstrap script for a terraform <<-USERDATA
// heredoc: it ESCAPES the terraform template sequences "${" and "%{" (which the
// shell uses for parameter/arithmetic expansion) to "$${" / "%%{" so terraform
// treats the whole body as a literal, then indents each line by 6 spaces for a
// deterministic, gofmt-stable rendering.
func indentUserData(s string) string {
	s = strings.ReplaceAll(s, "${", "$${")
	s = strings.ReplaceAll(s, "%{", "%%{")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l == "" {
			continue
		}
		lines[i] = "      " + l
	}
	return strings.Join(lines, "\n")
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

// edgeTerminatorFor returns the nginx :443 TLS terminator snippet for a service
// reached through the private VPC edge (sso/backend/mcp per doEdgeOrigins), or
// "" if the service is not an edge origin.
func edgeTerminatorFor(svcName string) (string, error) {
	for _, o := range doEdgeOrigins() {
		if o.Service != svcName {
			continue
		}
		snippet, err := RenderEdgeTLSTerminatorSnippet(EdgeTLSTerminator{Hostname: o.Hostname, UpstreamPort: o.UpstreamPort})
		if err != nil {
			return "", fmt.Errorf("do-baseline: edge terminator for %s: %w", svcName, err)
		}
		return snippet, nil
	}
	return "", nil
}

// doBaselineBackendSpec is the deterministic backend bootstrap spec (all-default
// variable names) so the DURABLE render is byte-stable and self-documenting.
func doBaselineBackendSpec() BackendBootstrapSpec {
	return (BackendBootstrapSpec{Environment: doBaselineEnv}).withDefaults()
}

// doBaselineSSOSpec is the ONE deterministic sso bootstrap spec for the DURABLE
// render, shared by renderFullServiceBootstrap (which renders the user_data)
// and DOBaselineVaultDataSources (which must declare the exact same set of
// `data "vault_kv_secret_v2"` leaves the rendered user_data references — most
// importantly SMTPKVPath, which is conditional). Defining it once here means
// the two can never drift out of sync (a real bug this wave: SMTPKVPath was
// set in the render call but not mirrored into the data-source declaration,
// producing a `terraform validate` "Reference to undeclared resource" error).
func doBaselineSSOSpec(secrets DOBaselineSecrets) SSODOBootstrapSpec {
	return SSODOBootstrapSpec{
		Environment:           doBaselineEnv,
		DomainName:            "pyxcloud.io",
		VaultOIDCSecret:       secrets.SSOVaultOIDCSecret,
		RunnerPublicKey:       secrets.SSORunnerPublicKey,
		SMTPKVPath:            "infra/staging/sso/smtp",
		PassobuildSenderEmail: secrets.SSOSenderEmail,
	}
}

// renderFullServiceBootstrap renders the COMPLETE DigitalOcean bootstrap for one
// service (pd-MIG-CUTOVER-F5 durable render). mcp/obs/sast now source their
// secrets from Vault `data "vault_kv_secret_v2"` blocks (EPIC-BOOTFETCH-AWS-SM-
// TO-VAULT); sso does too EXCEPT its two unmigrated fields (VaultOIDCSecret,
// RunnerPublicKey), still inlined from DOBaselineSecrets. For the three
// private edge origins (sso/backend/mcp) the nginx :443 terminator is appended
// so the droplet template carries BOTH the service and its edge in one boot.
func renderFullServiceBootstrap(svcName string, secrets DOBaselineSecrets, opts DOBaselineOptions) (string, error) {
	var ud string
	var err error
	switch svcName {
	case "mcp":
		ud, err = RenderMcpDOUserData(McpDOBootstrapSpec{Environment: doBaselineEnv})
	case "sso":
		ud, err = RenderSSODOBootstrapUserData(doBaselineSSOSpec(secrets))
	case "obs":
		ud, err = RenderOBSDOBootstrapUserData(OBSDOBootstrapSpec{})
	case "sast":
		ud, err = RenderSastDOBootstrapUserData(SastDOBootstrapSpec{Environment: doBaselineEnv})
	case "backend":
		ud, err = RenderBackendDOUserData(doBaselineBackendSpec())
	case "vpn":
		ud, err = RenderVPNBootstrapUserData(VPNBootstrapSpec{Environment: doBaselineEnv})
	case stagingFEServiceName:
		ud, err = RenderStagingFEDOBootstrapUserData(StagingFEDOBootstrapSpec{})
	default:
		return "", fmt.Errorf("do-baseline: no full bootstrap for service %q", svcName)
	}
	if err != nil {
		return "", fmt.Errorf("do-baseline: render %s bootstrap: %w", svcName, err)
	}
	// Private staging TLS terminates on the origin after the VPC edge SNI route.
	snip, terr := edgeTerminatorFor(svcName)
	if terr != nil {
		return "", terr
	}
	if snip != "" {
		ud = ud + "\n\n" + snip
	}
	return ud, nil
}

// DOBaselineVariableNames returns the deterministic, deduplicated set of Terraform
// variable names the DURABLE render (FullServiceBootstraps) still references via
// ${var.<x>} across the var-model services (mcp/obs/backend/vpn — the board DB
// URL for mcp has no Vault leaf yet; obs/backend/vpn secrets are unmigrated).
// The harness (cutover/render.go) emits a `variable "<x>" {}` declaration for
// each so the rendered estate.tf is self-contained and `terraform validate`s;
// the values are supplied at apply time via -var from Secrets Manager. sso is
// excluded (it inlines its two unmigrated secret values, not variable refs).
//
// sast is EXCLUDED here (EPIC-BOOTFETCH-AWS-SM-TO-VAULT, wave 2): its secrets
// are now Vault `data "vault_kv_secret_v2"` references — see
// DOBaselineVaultDataSources.
func DOBaselineVariableNames() []string {
	seen := map[string]bool{}
	var out []string
	add := func(names ...[]string) {
		for _, group := range names {
			for _, n := range group {
				if n == "" || seen[n] {
					continue
				}
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	mp, ms := (McpDOBootstrapSpec{Environment: doBaselineEnv}).McpDOBootstrapVariableNames()
	op, os_ := (OBSDOBootstrapSpec{}).OBSDOBootstrapVariableNames()
	bp, bs := doBaselineBackendSpec().BackendBootstrapVariableNames()
	vp, vs := (VPNBootstrapSpec{Environment: doBaselineEnv}).VPNBootstrapVariableNames()
	fp, fs := (StagingFEDOBootstrapSpec{}).StagingFEDOBootstrapVariableNames()
	add(mp, ms, op, os_, bp, bs, vp, vs, fp, fs)
	return out
}

// DOBaselineVaultDataSources returns the deterministic, deduplicated set of
// `data "vault_kv_secret_v2"` HCL blocks (+ the shared vault provider block)
// the DURABLE render (FullServiceBootstraps) needs across sast/mcp/sso
// (EPIC-BOOTFETCH-AWS-SM-TO-VAULT, wave 2). The harness (cutover/render.go)
// appends these to the rendered estate.tf so it is self-contained and
// `terraform validate`s — no operator-populated -var for these leaves anymore;
// Terraform reads Vault directly at apply time.
//
// Uses doBaselineSSOSpec(DOBaselineSecrets{}) — a zero-value secrets struct —
// because NONE of the KV path fields SSODOVaultDataSources reads (KCDBURLKVPath
// etc., all fixed defaults) depend on secrets content; only the two unmigrated
// literal fields (VaultOIDCSecret/RunnerPublicKey) do, and those are irrelevant
// here. This MUST stay the same spec shape renderFullServiceBootstrap uses for
// sso (see doBaselineSSOSpec's doc comment for the bug this guards against).
func DOBaselineVaultDataSources() []string {
	seen := map[string]bool{}
	var docs []string
	add := func(groups ...[]string) {
		for _, group := range groups {
			for _, doc := range group {
				if seen[doc] {
					continue
				}
				seen[doc] = true
				docs = append(docs, doc)
			}
		}
	}
	add(
		(SastDOBootstrapSpec{Environment: doBaselineEnv}).SastDOVaultDataSources(),
		(McpDOBootstrapSpec{Environment: doBaselineEnv}).McpDOVaultDataSources(),
		doBaselineSSOSpec(DOBaselineSecrets{}).SSODOVaultDataSources(),
	)
	// NOTE: no `required_providers`/`provider "vault"` block is emitted here — a
	// Terraform module may have only ONE required_providers block (a second one
	// is a hard "Duplicate required providers configuration" error), and this
	// harness's backend.tf (cutover/render.go) already owns that block for the
	// `digitalocean` provider. The caller (cutover/render.go) is responsible for
	// merging `vault` into that SAME required_providers block whenever these data
	// sources are non-empty (see full's handling there), exactly as it already
	// merges kubernetes/helm/cloudflare in the generic AssembleHCL path.
	return docs
}

// indentPreserveVars indents each line of an already-heredoc-escaped user_data
// body by 6 spaces for the <<-USERDATA heredoc, WITHOUT re-escaping ${...} (the
// caller has already run escapeBashExpansionsForHeredoc, which preserves
// ${var.<x>} and escapes bash ${...} to $${...}). Unlike indentUserData it does
// NOT touch ${ / %{ sequences, so ${var.<x>} survives for terraform to interpolate.
func indentPreserveVars(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l == "" {
			continue
		}
		lines[i] = "      " + l
	}
	return strings.Join(lines, "\n")
}
