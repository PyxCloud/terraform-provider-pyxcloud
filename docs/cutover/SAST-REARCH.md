# AWS → DigitalOcean cutover: the SAST scanner re-architecture

`pd-MIG-CUTOVER-F2-02` (sast; EPIC-AWS-TO-DO-MIGRATION).

## Why SAST is not a plain user_data port

The other platform services (SSO, VPN, obs, backend, MCP) are long-lived
servers: a scale-group of 1 that boots, installs its runtime, and serves. Their
DO migration is a straight cloud-init port (see `platform_bootstrap_sso.go`).

**SAST is different — it is dispatch-driven, self-terminating batch compute.**
On AWS (`pyx-backend` `infrastructure/sast-asg.tf`) the flow is:

1. The **backend** enqueues a repository to **S3** under
   `scan-jobs/<job>/input/repo.zip`.
2. The backend calls `autoscaling:SetDesiredCapacity = 1` on the SAST **ASG** to
   wake a runner (the ASG idles at desired-capacity **0**).
3. The runner boots, **pulls the scanner super-image from ECR**, polls S3 for
   pending jobs (an `input/repo.zip` with no `output/semgrep_output.json`), runs
   **Semgrep + OSV**, writes results back to S3 under
   `scan-jobs/<job>/output/{semgrep,osv}_output.json`.
4. When the queue drains, the runner calls
   `autoscaling:SetDesiredCapacity = 0` **on itself**, scaling the ASG back to
   zero (pay-per-scan).

So SAST leans on **three AWS primitives** that have no drop-in DO equivalent:
**ECR** (image), **S3** (job queue), and **ASG SetDesiredCapacity** (dispatch +
scale-to-zero).

## The DO model (this PR — provider side)

We keep the **contract identical** (the `scan-jobs/<job>/input|output/…` key
layout, the Semgrep+OSV super-image, self-scale-down at the end) and swap the
three primitives for their DO equivalents. This is expressed in the canonical
vocabulary — the SAST service stays a `virtual-machine-scale-group` of 1
(`platform_asgs.go`), and the DO-specific runner is threaded onto its
`UserDataByProvider["digitalocean"]` so **only a DigitalOcean placement** gets
the DO runner while AWS keeps the ECR/S3/ASG runner. No new component, no forked
topology.

| Concern | AWS (today) | DigitalOcean (this PR) |
|---|---|---|
| Scanner image | ECR `${env}-pyx-sast` | DO Container Registry `registry.digitalocean.com/pyx-registry/pyx-sast:latest` |
| Registry auth | `ecr get-login-password` (instance role) | `docker login registry.digitalocean.com` with a **DO registry read token** (the DO API token works as both user + password) |
| Job queue | S3 `${env}-pyx-sast-scan-io` | **Spaces** bucket `pyx-sast-jobs-fra1` (default; `pyx-artifacts-fra1` is the documented reuse alternative) via the **S3-compatible endpoint** `https://<region>.digitaloceanspaces.com` with **Spaces access/secret keys** |
| Job/result IO | `aws s3 …` | `aws --endpoint-url <spaces> s3 …` (same key layout) |
| Dispatch / scale | `autoscaling:SetDesiredCapacity` | **DO droplet_autoscale API** (`PUT /v2/droplets/autoscale/<pool>`) |

Implementation:

- `internal/catalog/platform_bootstrap_sast_do.go` —
  `RenderSastDOBootstrapUserData(SastDOBootstrapSpec)` renders the runner
  cloud-init. Faithful port of `sast_runner_user_data`: install docker, log in +
  pull the DO registry image, poll Spaces, run Semgrep + OSV, upload results to
  Spaces, self-scale-down via the DO autoscale API.
- `internal/catalog/platform_asgs.go` —
  `PlatformScaleGroupComponentsWithProviderBootstrap(...)` +
  `PlatformBootstrapsByProvider` thread the DO runner onto
  `sast.UserDataByProvider["digitalocean"]`.
- `internal/catalog/assemble.go` — declares the runner's four out-of-band secret
  variables (`variable {}` blocks, `sensitive = true`) when a DO scale-group's
  bootstrap references them, so the rendered `.tf` is self-contained and
  `terraform validate`s.

### Secrets (never inlined)

All wired by Terraform variable name (operator wires to Vault / secret source):

- `do_spaces_access_key` / `do_spaces_secret_key` — Spaces S3-compatible keys.
- `do_registry_token` — DO registry read token for `docker login`.
- `do_api_token` — DO API token for the droplet_autoscale self-scale-down call.

## LIMITATION: DO droplet_autoscale cannot scale to zero

A DO `digitalocean_droplet_autoscale` pool requires **`min_instances >= 1`** — it
**cannot idle at zero** the way an AWS ASG can. `TranslateScaleGroup` already
lifts a zero min to 1 for DO, and the DO API rejects `min_instances = 0`.

Consequences:

- **"Scale down" means back to the floor (1), not to 0.** The runner's
  self-scale-down target is `SastDOBootstrapSpec.ScaleDownTo` (default **1**,
  clamped up from any 0/negative value).
- **Cost:** the SAST pool is **always-on at one small droplet** rather than
  pay-per-scan-at-zero. The canonical sizing (2 vCPU / 4 GiB, resolved from the
  catalog) keeps that floor cheap; a single always-on `s-2vcpu-4gb`-class droplet
  is the accepted cost of the DO model. If pay-per-scan-at-zero is required
  later, the alternatives are (a) a DO Function / App Platform job (no persistent
  droplet) or (b) create/destroy the pool per batch via the backend — both are
  larger changes deferred out of this PR.

## FOLLOW-UP: pyx-backend dispatch change (cross-repo, NOT in this PR)

The **backend** currently dispatches SAST against AWS and must change for a DO
target. This is a **`pyx-backend` code change**, flagged here as a follow-up
task — it is **not** implemented in this (terraform-provider-pyxcloud) PR.

Today (AWS) the backend:

1. Enqueues `repo.zip` to **S3** (`aws s3 …` / SDK `PutObject`) under
   `scan-jobs/<job>/input/repo.zip`.
2. Wakes the runner via
   **`autoscaling:SetDesiredCapacity(<env>-sast-runner, 1)`**.
3. Polls **S3** for `output/semgrep_output.json` + `osv_output.json`.

On DO it must instead:

1. Enqueue `repo.zip` to **Spaces** (S3-compatible SDK pointed at
   `https://<region>.digitaloceanspaces.com` with Spaces keys) — **same
   `scan-jobs/<job>/…` key layout**, so only the endpoint/credentials change.
2. Wake the runner via the **DO droplet_autoscale API**
   (`PUT /v2/droplets/autoscale/<pool_id>` setting `min/max` up to at least 1)
   instead of `SetDesiredCapacity`. Because the pool floor is already **1** (see
   the limitation above), the "wake" step is a no-op in the always-on model — the
   runner is already polling — so the backend can optionally **drop the explicit
   wake** on DO and rely on the always-on runner picking up the job. To keep
   pay-per-batch-ish behaviour it can bump `max` and let the pool scale on CPU.
3. Poll **Spaces** for the output objects (same keys).

Suggested shape: a provider-abstraction seam in the backend's SAST dispatcher
(`SastDispatcher` with `aws` / `digitalocean` implementations) selected by the
target estate's provider, so the queue client (S3 vs Spaces) and the scale client
(ASG vs droplet_autoscale, or no-op) are swapped together. Track as a dedicated
`pd-MIG-CUTOVER-F2-02-backend` task.

## Verification (this PR)

- `go build ./...` and `go vet ./...` — exit 0.
- Unit tests: `RenderSastDOBootstrapUserData` faithful-port + no-inlined-secrets +
  scale-down-floor + variable partitioning + the provider-bootstrap wiring
  (`platform_bootstrap_sast_do_test.go`).
- **`terraform init && validate` GREEN** for a DO estate whose sast scale-group
  carries the DO runner (`platform_bootstrap_sast_do_validate_test.go`).
- **PR-only. No apply.**
