# cutover/ — persistent DO cutover deploy harness

**pd-MIG-CUTOVER-F2-02 · EPIC-AWS-TO-DO-MIGRATION**

This is **THE** way to apply the AWS → DigitalOcean cutover baseline from now on.
It replaces the previous ad-hoc workflow (re-rendering catalog HCL into a throwaway
`/tmp` directory on every apply, where only the S3 state persisted). The estate is
now rendered **deterministically from committed source** into `cutover/generated/`
and applied against the **persistent S3 state**.

Blue/green: this touches **only** the DigitalOcean blue estate. No DNS, no traffic
cut, **AWS is untouched**.

## What it manages

The Frankfurt (`fra1`) cutover baseline — the exact 11-resource estate in the S3
state, **plus** the DO Spaces artifact bucket (now that Spaces keys exist):

| Resource | Name |
|---|---|
| `digitalocean_vpc` | `passo-do-baseline-net` (10.0.1.0/24) |
| `digitalocean_firewall` | `passo-do-baseline-sg` (inbound 443; egress icmp/tcp/udp) |
| `digitalocean_database_cluster` ×2 | `pyx-main-db`, `keycloak-db` (pg 17, db-s-2vcpu-4gb, 2 nodes) |
| `digitalocean_droplet_autoscale` ×6 | `backend` `mcp` `obs` `sast` `sso` `vpn` (self-healing floor of 1) |
| `digitalocean_loadbalancer` | `edge-lb` (fronts the `pyx-backend` tag on 443) |
| `digitalocean_spaces_bucket` | `pyx-artifacts-fra1` (release-artifact store) |

State: `s3://pyx-terraform-state/cutover/do-baseline-fra1.tfstate` on the
S3-compatible **DigitalOcean Spaces** endpoint `https://fra1.digitaloceanspaces.com`
(bucket versioning ON; locking via native S3 lockfile, `use_lockfile=true`). The
terraform `s3` backend reads the Spaces keys from `AWS_ACCESS_KEY_ID` /
`AWS_SECRET_ACCESS_KEY` at init/plan/apply time — export the
`beta-DigitalOceanSpacesKeys` values into those before running terraform.

> The state was migrated off AWS S3 (`s3://pyxcloud-terraform-state`, `eu-west-1`)
> onto Spaces via `terraform init -migrate-state`. The legacy AWS bucket is kept as
> a cold backup until the AWS-decommission step — do not delete it yet.

### Why droplet-autoscale, not DOKS

The generic `catalog.AssembleHCL` scale-group path descends to a DOKS
`digitalocean_kubernetes_cluster` node-pool on DigitalOcean. The **live** cutover
estate was applied as `digitalocean_droplet_autoscale` groups. Rendering the DOKS
shape against that state would plan a full destroy+recreate of every service. So
the harness calls `catalog.AssembleDOBaseline` (in
`internal/catalog/do_baseline.go`), which emits the droplet-autoscale shape that
**matches** the state, using the same `catalog.DOBaselineInput(...)` descriptor.
The plan against live state is additive (`0 to destroy`).

## Durable mcp

The `mcp` droplet is the board-OS control plane. Its bootstrap `user_data` now
sources `BOARD_DATABASE_URL` from **`beta-DO-pyx-main-db-url`** (the **mesh_app**
URL) — **not** `doadmin`, **not** `beta-DO-pyx-db-password`. The value is injected
at **render time** from AWS Secrets Manager (the droplet has no AWS role, so it
cannot fetch it at boot — same mechanism already used for `EMBED_TOKEN_SECRET`).
The host is rewritten to the DO managed-PG **private** VPC endpoint so the droplet
reaches the DB over the shared VPC. This makes the mcp **durable**: a droplet
replacement re-bootstraps against `mesh_app`, not the stale `doadmin/defaultdb`
URL that used to be baked into state. Kept intact: `:8787`, the systemd unit, and
the DO Spaces artifact fetch.

## Secrets — nothing secret is committed

`cutover/generated/` is git-ignored: the rendered `estate.tf` carries the
render-time-injected secrets in the mcp launch template. It is fully reproducible
from the committed catalog + secret sources, so it is never committed.

- **Injected at RENDER time** (Go-string literal, baked into `user_data`): the
  mesh_app `BOARD_DATABASE_URL` (mcp; no Vault leaf yet), and, when
  `DO_FULL_SERVICE_BOOTSTRAPS=1`, sso's `VaultOIDCSecret` / `RunnerPublicKey` (no
  Vault leaf provisioned for either — see the RISK note in
  `internal/catalog/platform_bootstrap_sso_do.go`).
- **Resolved by TERRAFORM at PLAN/APPLY time, directly from Vault**
  (EPIC-BOOTFETCH-AWS-SM-TO-VAULT, wave 2 — `data "vault_kv_secret_v2"` blocks,
  see `internal/catalog/vault_datasource.go`): the sast Spaces keys + DO API
  token, the mcp Spaces keys + `EMBED_TOKEN_SECRET`, and (when
  `DO_FULL_SERVICE_BOOTSTRAPS=1`) sso's keycloak-db URL/creds, bootstrap admin
  password, Spaces keys, and SMTP creds. Terraform authenticates to Vault via
  `VAULT_ADDR`/`VAULT_TOKEN` (or `VAULT_ROLE_ID`+`VAULT_SECRET_ID` via a CI OIDC
  login step) in the environment — no `-var`, no AWS Secrets Manager export, for
  any of these anymore.
- **Read by terraform at PLAN/APPLY time from the environment** (never
  rendered, unrelated to Vault): the DO API token and the Spaces provider
  credentials used by the `digitalocean` provider block itself
  (`DIGITALOCEAN_TOKEN` / `SPACES_ACCESS_KEY_ID` / `SPACES_SECRET_ACCESS_KEY`).

## Reproducible workflow

Prereqs: `go`, `terraform` (>= 1.11 for `use_lockfile`), `aws` (creds for Secrets
Manager), `python3`. Run from the repo root (`terraform-provider-pyxcloud/`). The
state backend is DigitalOcean Spaces (S3-compatible); its credentials are the
Spaces keys exported into `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` (step 1).

### 1. Export credentials from Secrets Manager

```bash
# --- render-time secrets (baked into the mcp user_data) ---
export DO_BOARD_DATABASE_URL="$(aws secretsmanager get-secret-value \
  --secret-id beta-DO-pyx-main-db-url --query SecretString --output text)"      # mesh_app URL
export DO_MCP_EMBED_TOKEN="$(aws secretsmanager get-secret-value \
  --secret-id beta/passobuild-mcp-embed-token --query SecretString --output text)"

SPACES_JSON="$(aws secretsmanager get-secret-value \
  --secret-id beta-DigitalOceanSpacesKeys --query SecretString --output text)"
export DO_SPACES_ACCESS_KEY="$(echo "$SPACES_JSON" | python3 -c 'import sys,json;print(json.load(sys.stdin)["access_key"])')"
export DO_SPACES_SECRET_KEY="$(echo "$SPACES_JSON" | python3 -c 'import sys,json;print(json.load(sys.stdin)["secret_key"])')"

# --- terraform provider credentials (read at plan/apply, never rendered) ---
export DIGITALOCEAN_TOKEN="$(aws secretsmanager get-secret-value \
  --secret-id beta-DigitalOceanToken --query SecretString --output text)"
export SPACES_ACCESS_KEY_ID="$DO_SPACES_ACCESS_KEY"
export SPACES_SECRET_ACCESS_KEY="$DO_SPACES_SECRET_KEY"

# --- terraform s3 backend credentials = the SAME Spaces keys (state lives on Spaces) ---
export AWS_ACCESS_KEY_ID="$DO_SPACES_ACCESS_KEY"
export AWS_SECRET_ACCESS_KEY="$DO_SPACES_SECRET_KEY"
```

### 2. Render (deterministic)

```bash
go run ./cutover/render.go
# -> writes cutover/generated/{backend.tf,variables.tf,estate.tf}
```

### 3. Init (Spaces backend) + plan/apply

```bash
cd cutover/generated
terraform init                                              # DigitalOcean Spaces backend (fra1)
terraform plan  -var 'do_ssh_keys=["57496891"]'            # expect: 0 to destroy
terraform apply -var 'do_ssh_keys=["57496891"]'
```

### Cloudflare-edge cutover origins (F4-prep, opt-in)

To make each prod hostname servable via Cloudflare→DO origin, set
`DO_EDGE_TLS_ORIGINS=1` before rendering. This appends an nginx `:443` TLS
terminator (the `obs` pattern, `internal/catalog/edge_tls_terminator.go`) to the
Cloudflare-routed origins — `sso` (beta-auth.pyxcloud.io→8080), `backend`
(beta-api.pyxcloud.io→8080), `mcp` (mcp.passo.build→8787) — so Cloudflare `Full`
can terminate public TLS and proxy to each origin. Off by default (0 change to
the base estate). See `docs/cutover/CLOUDFLARE-CUTOVER.md` for the full DNS-flip
change-set, probes, and rollback.

```bash
export DO_EDGE_TLS_ORIGINS=1
go run ./cutover/render.go
cd cutover/generated && terraform init
terraform apply -target=digitalocean_droplet_autoscale.sso \
                -target=digitalocean_droplet_autoscale.backend \
                -target=digitalocean_droplet_autoscale.mcp \
                -var 'do_ssh_keys=["57496891"]'   # rolls each origin; 0 destroy of PG/VPC
```

### Durable mcp apply (targeted)

```bash
cd cutover/generated
terraform plan  -target=digitalocean_droplet_autoscale.mcp -var 'do_ssh_keys=["57496891"]'   # <= 1 change, 0 destroy
terraform apply -target=digitalocean_droplet_autoscale.mcp -var 'do_ssh_keys=["57496891"]'
```

Or via the Makefile (assumes the env exports above): `make -C cutover plan`,
`make -C cutover apply`, `make -C cutover plan-mcp`, `make -C cutover apply-mcp`,
`make -C cutover validate`.

## Exact reproducible apply command for the remaining services

After the durable mcp apply, the remaining, non-secret convergence (the Spaces
bucket + firewall droplet-tag association) is applied with the **same** command,
without the `-target`:

```bash
cd cutover/generated
terraform apply -var 'do_ssh_keys=["57496891"]'
```

## Verify a droplet

```bash
doctl compute droplet list --format Name,PublicIPv4,Tags,Status --no-header | grep pyx-mcp
ssh -i ~/.ssh/id_ed25519 root@<mcp-public-ip> \
  'curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8787/health; \
   systemctl is-active passobuild-mcp; \
   grep BOARD_DATABASE_URL /etc/passobuild-mcp.env'
```

Healthy = `200`, `active`, and the `BOARD_DATABASE_URL` shows `mesh_app@...`.
The service log line `Mesh coordination enabled (postgres, auth=true)` confirms
the mesh_app DB connection.
