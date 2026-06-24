# Migrating the hand-written platform modules to the abstract provider

**Board task:** `pd-DEP-MIGRATE-PLATFORM-MODULES` — *Migrate hand-written platform
TF modules (SSO / secrets / observability / VPN / backend) to the abstract
provider.* This doc is the multi-slice plan; **slice 1 (SSO bootstrap) is
implemented in this PR**, the rest are specified here for the follow-up PRs.

This is a `cx6` epic. It is deliberately split one platform module per slice so
each PR stays small, reviewable, and plan-only.

## Why this is more than "a scale-group of 1"

`internal/catalog/platform_asgs.go` already expresses the 5 platform services
(SSO / VPN / observability / SAST / backend) in the canonical vocabulary as
`virtual-machine-scale-group`s of 1 (self-heal floor), and the
`full-estate-do` dry-run already proves they descend to valid AWS **and**
DigitalOcean Terraform. That solved the *shape* of each service (size +
self-healing ASG/node-pool).

What it did **not** solve is the *substance*: the entire reason
`core/single-sign-on/main.tf` exists is its ~200-line `keycloak_user_data`
bootstrap — install Java + Keycloak, boot-fetch the providers/themes/realm
bundle, build the local HTTPS keystore, write `keycloak.conf` + the systemd
unit, `kc.sh build`, import the realm. A canonical scale-group with **no
bootstrap** boots a bare Ubuntu box. So "migrate the SSO module" = **port that
bootstrap into the abstract component**, not just declare a scale-group.

The bootstrap is welded to Terraform interpolations that only exist in that one
root module (`${aws_db_instance.db_instance.address}`, the `random_password`
resources, the SES SMTP IAM access key, `var.jit_vpn_sg_id`, `var.environment`).
That coupling is exactly why a naive copy can't be abstracted. The canonical
component **lifts each interpolation to a typed input** and, for the secrets,
references them **by Terraform variable name** (never the value) so nothing
sensitive enters the abstract topology or state.

## Slice 1 — SSO / Keycloak (this PR)

**Files:**
- `internal/catalog/platform_bootstrap_sso.go` — `SSOBootstrapSpec` (typed,
  provider-neutral) + `RenderSSOBootstrapUserData` (faithful port of the
  hand-written `keycloak_user_data` as parameterised cloud-init) +
  `SSOBootstrapVariableNames` (the plain/sensitive variable partition the caller
  declares).
- `internal/catalog/platform_asgs.go` — new
  `PlatformScaleGroupComponentsWithBootstrap(arch, os, k8sVersion, PlatformBootstraps)`
  threads the per-service bootstrap onto the scale-group launch template via the
  existing `AssembleScaleGroup.UserData` field. **No new translator / render
  path** — the existing scale-group renderer descends it (SPEC §1). The original
  `PlatformScaleGroupComponents` is unchanged (delegates with `nil` bootstraps),
  so the full-estate dry-run is byte-for-byte backward-compatible.
- `internal/catalog/platform_bootstrap_sso_test.go` — faithful-port assertions,
  the no-secret-inlined security invariant, the plain/sensitive partition, and
  the integration proof that the bootstrap lands on the `sso` scale-group (and
  only that one).

**Round-trip evidence (plan-only, no apply):** rendering an `aws`/`Dublin` SSO
env (`pyxcloud_environment` style, via `cmd/pyxenv-render`) emits an
`aws_launch_template` + `aws_autoscaling_group` + security group whose
`user_data = base64encode(<<-PYXUSERDATA … )` carries the full Keycloak
bootstrap with the 13 `${var.*}` references. With the matching `variable {}`
declarations (from `SSOBootstrapVariableNames`), `terraform init -backend=false`
+ `terraform validate` = **Success** against the real `hashicorp/aws` provider
schema. No cloud creds, nothing applied.

### What is intentionally NOT ported in slice 1
The hand-written module also runs **post-instance-refresh `kcadm` reconciliation**
(SSH to a discovered ASG instance, update the Vault OIDC client secret in realm
`pyx`). That is a Day-2 control-plane action, not boot-time cloud-init. It should
become a separate `scheduled-trigger`/operator step in a later slice (or move to
the backend `/api` reconcile path), not be smuggled into user_data. Tracked
below under "cross-cutting".

## Remaining slices (follow-up PRs, same pattern)

The pattern is fixed: **`Render<Service>BootstrapUserData(spec)` → typed inputs
+ `${var.*}` secret refs → thread via `PlatformBootstraps` → round-trip
`terraform validate`.**

### Slice 2 — VPN / WireGuard
- **Source:** `core/internal-vpn/wireguard/main.tf` (+ `jit-backing/`).
- **Bootstrap:** install WireGuard, render `wg0.conf` (server private key from a
  `${var.wg_private_key}` secret, peer CIDR, listen port), enable
  `wg-quick@wg0`, set up the JIT add-peer hook. The stable public endpoint is the
  already-canonical `reserved-ip` component (full estate `vpn-endpoint`).
- **Inputs:** `wg_private_key` (sensitive), `wg_address`, `wg_listen_port`,
  `wg_peers` (or the JIT table name). Health = `ec2` (UDP gateway, no LB) — already
  set in `PlatformServices()`.
- **Note:** the JIT-allowlist DynamoDB + gc-lambda (`jit-backing/`) map to the
  existing `key-value-store` + `serverless-function` components — wire them in the
  estate, don't re-implement.
- **Security/lockout risk:** VPN is owner-gated for deploy. PR is plan-only.

### Slice 3 — Observability (`obs`)
- **Source:** `core/single-sign-on/observability.tf` + the platform observability
  aggregator.
- **Bootstrap:** install the CloudWatch agent / the LGTM aggregator. On AWS the
  alarms/log-groups are already the `monitoring` component
  (`cloudwatch.go`); on DigitalOcean it is the LGTM operator stack
  (`render_monitoring_lgtm.go`). The `obs` **box** bootstrap (agent install +
  scrape config) is the new piece; most of observability is already first-class.
- **Inputs:** scrape targets, the CW-agent config object key, retention.

### Slice 4 — Backend (`backend`)
- **Source:** `core/pyx-backend/infrastructure/main.tf` (+ `sast-asg.tf`,
  `deploy_backend.tf`). NB: `infrastructure/pyxenv/rendered/` is **already**
  provider-rendered output — the backend is the furthest-along migration; this
  slice formalises its bootstrap.
- **Bootstrap:** the native-binary pull + systemd unit (`t3.large`, EBS 50GB,
  user_data pulls the native image). The ALB attach is the existing
  `attach-to-existing-alb` component (host-header `beta-api`); KMS + the 4 log
  groups are `kms` + `monitoring`.
- **Inputs:** native artifact URL/version, the deploy-target ALB listener ARN.
- **SAST (`sast`)** is a sibling batch-worker scale-group — a thin bootstrap
  (Semgrep/SonarQube install), same pattern.

### Cross-cutting (not a per-service slice)
- **Secrets:** the platform secrets are already the `secrets-manager` component
  (`secrets.go`, AWS-native) / self-hosted `vault-ha` (`vaultha.go`, the DO
  mitigation). The migration work here is **wiring** the per-service secret
  *variables* (the `${var.kc_*}` etc.) to those sources, not a new component.
- **Day-2 reconciliation** (the SSO `kcadm` OIDC-secret step, ASG instance-refresh
  hooks): model as `scheduled-trigger`/operator steps, separate from boot-time
  user_data.
- **Cloudflare DNS** for each service: the existing `dns` component
  (`cloudflare.go`).

## Cutover (per module, when all slices land — USER-GATED)
1. Express the module's full topology as a `pyxcloud_environment` (or the
   `FullEstate*` constructor) including its `PlatformBootstraps` entry.
2. `terraform plan` against the real account with env credentials (Mode A —
   `AWS_*` / `CLOUDFLARE_API_TOKEN`, exactly like today's scripts). Plan-only.
3. Review the diff vs the live hand-written module; greenfield + DNS cutover (no
   state import), matching the backend BE-API cutover plan.
4. Retire the hand-written `*.tf`.

**Nothing in this doc or PR applies, spends, or mutates live infra.** Deploy +
cutover is the operator's gate (security/VPN/SSO are owner-gated).
