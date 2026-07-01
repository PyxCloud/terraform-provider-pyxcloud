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

State: `s3://pyxcloud-terraform-state/cutover/do-baseline-fra1.tfstate`
(backend region `eu-west-1`).

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
from the committed catalog + Secrets Manager, so it is never committed.

- **Injected at RENDER time** (into `user_data`): Spaces keys, EMBED token, and the
  mesh_app `BOARD_DATABASE_URL`.
- **Read by terraform at PLAN/APPLY time** (from the environment, never rendered):
  the DO API token and the Spaces provider credentials.

## Reproducible workflow

Prereqs: `go`, `terraform`, `aws` (creds for the S3 backend + Secrets Manager),
`python3`. Run from the repo root (`terraform-provider-pyxcloud/`).

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
```

### 2. Render (deterministic)

```bash
go run ./cutover/render.go
# -> writes cutover/generated/{backend.tf,variables.tf,estate.tf}
```

### 3. Init (S3 backend) + plan/apply

```bash
cd cutover/generated
terraform init                                              # S3 backend
terraform plan  -var 'do_ssh_keys=["57496891"]'            # expect: 0 to destroy
terraform apply -var 'do_ssh_keys=["57496891"]'
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
