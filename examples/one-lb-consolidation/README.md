# One-LB consolidation — IaC reconciliation runbook

**pd-ONE-LB-CONSOLIDATION** — reconcile the live, off-contract (doctl) load-balancer
collapse into the provider contract and clear the resulting state drift.

> **Status:** CODE + PLAN only. Nothing in this runbook has been executed. Every
> `terraform`, `doctl`, and state command below is listed for review, not run.

---

## 1. What happened (live, off-contract)

A load-balancer consolidation was performed **live via `doctl`**, bypassing IaC:

| Live resource | Facts |
| --- | --- |
| `pyx-edge-lb` (LB) | id `ef708dea-27df-419c-ab01-c58010e276ac`, IP `188.166.192.241`, region `fra1`, `size_unit=1`, tag-target `pyx-edge`, forwarding `TCP:443 -> TCP:443`, health `TCP:443` |
| `pyx-edge-1` (droplet) | id `582230764`, `fra1`, `s-1vcpu-2gb`, `ubuntu-24-04-x64`, tag `pyx-edge`, ssh key `57496891` |
| `pyx-edge-2` (droplet) | id `582232546`, `fra1`, `s-1vcpu-2gb`, `ubuntu-24-04-x64`, tag `pyx-edge`, ssh key `57496891` |

The two edge droplets run nginx as a **pure `stream {}` SNI host-router**
(`ssl_preread`, L4 passthrough) that maps the SNI hostname to the correct
upstream service droplet on `:443` — end-to-end TLS is preserved (the origin
terminates, not the edge). The exact bootstrap is `edge-router.cloudinit`
(mirrored into `topology.json`'s `UserData`).

> **nginx gotcha (already learned, encoded in the user_data):** `nginx.conf` MUST
> start with `include /etc/nginx/modules-enabled/*.conf;` or the stream module is
> not loaded and nginx dies with `unknown directive "stream"`.

**Deleted live:** the 5 old per-service LBs — `lb-sso`, `lb-backend`, `lb-mcp`,
`lb-sso-prod`, `edge-lb-prod`. **Kept:** `lb-vault-internal` (`10.0.1.16`).

---

## 2. On-contract expression — recommended approach

**Recommendation: reuse the existing primitives** (SPEC §1 "prefer the smallest
canonical component; resolve it down" — do NOT add a bespoke component when two
existing ones compose):

```
load-balancer (tag-targeted, TCP:443)  ->  virtual-machine-scale-group (SNI-router pool)
```

- **`load-balancer`** (`internal/catalog/loadbalancer.go`) already renders a
  `digitalocean_loadbalancer` with `droplet_tag`, TCP forwarding, and a TCP
  health check — exactly the live `pyx-edge-lb`. `TargetTag="pyx-edge"` selects
  the pool by tag.
- **`virtual-machine-scale-group`** (`internal/catalog/scalegroup.go`,
  `renderScaleGroupDO` in `internal/catalog/render.go`) already carries `Tag`,
  `SSHKeys`, and per-instance `UserData` — the only existing component that can
  stamp the `pyx-edge` tag, attach ssh key `57496891`, AND flow the nginx SNI
  bootstrap. `Min=Max=2` expresses the fixed 2-droplet self-healing pool.

**Why not a new `edge-router` / `ingress-router` component (option ii)?** It would
bundle the LB + pool + SNI map as first-class config, but the SNI routing map is
**region-only, pure text baked into `user_data`** — it introduces **zero new
provider capability**. A new component would mean a new spec+plan+translate+render
+assemble-arm+provider-surface+tests carrying no primitive the two existing
components don't already render. That is exactly the gold-plating SPEC §1 warns
against. The `edge_tls_terminator` is NOT a counter-example: it is a **per-origin**
local `:443` terminator snippet appended to a *service* droplet's user_data — a
central SNI host-router is a different shape and is fully expressed by the LB +
pool composition above.

**Why the fixed-VM (`virtual-machine`) path does NOT fit:** `renderVMDO`
hardcodes `tags = ["pyxcloud"]` and emits no `ssh_keys` — it cannot carry the
`pyx-edge` tag the LB targets nor the ssh key. The scale-group is the on-contract
fit. (See §6 gaps.)

### 2.1 The concrete topology

`topology.json` in this directory is a `catalog.AssembleInput` (Go field names,
no json tags). It renders — and `terraform validate`s — to the live shape:

```bash
go build -o /tmp/pyxenv-render ./cmd/pyxenv-render
/tmp/pyxenv-render -in examples/one-lb-consolidation/topology.json \
                   -out examples/one-lb-consolidation/rendered
cd examples/one-lb-consolidation/rendered
terraform init -backend=false && terraform validate   # => Success!
rm -rf .terraform .terraform.lock.hcl                 # clean up
```

The rendered `.tf` is checked in under `rendered/` for review. It emits:
`digitalocean_vpc.pyx-edge-net`, `digitalocean_firewall.pyx-edge-sg` (inbound
`tcp/443` from `0.0.0.0/0`,`::/0`), `digitalocean_droplet_autoscale.pyx-edge`
(min=max=2, `tags=["pyxcloud","pyx-edge"]`, `ssh_keys=["57496891"]`, the SNI
`user_data`), and `digitalocean_loadbalancer.pyx-edge-lb` (`droplet_tag="pyx-edge"`,
`TCP:443->443`, `healthcheck tcp/443`).

> **State-shape note (import vs. forward-manage):** the rendered pool is a
> `digitalocean_droplet_autoscale` group, whereas the live resources are **two
> discrete `digitalocean_droplet`s**. For a *forward-managed root* the autoscale
> group is strictly better (self-heals a lost edge). For *importing the exact live
> resources as-is* (§3), import the two droplets discretely — see the two import
> variants in §3.2. Pick one and keep the root consistent with it.

---

## 3. Reconciliation — adopt the live resources & clear drift

### 3.1 Drift #1 — `edge-lb-prod` deleted live but still in prod state

`edge-lb-prod` was **terraform-managed** in the PROD estate at
`src/pyxcloud-production` (raw HCL, state on DO Spaces
`pyxcloud-terraform-state-prod/production/do-fra1.tfstate`). It is now gone live
→ the state records a resource that no longer exists.

**Verified:**
- `main` still has `resource "digitalocean_loadbalancer" "edge-lb-prod"` in
  `.pyxcloud/environments/production/pyx_014.tf` (matches the stale state entry).
- Branch **`feat/lb-collapse-reserved-ips`** (1 commit `70699eb`, **not merged**)
  already replaces that file with a comment stub — the LB resource is removed and
  the note points to per-service `digitalocean_reserved_ip` origins.

**Two ways to reconcile (pick ONE — do NOT run both):**

- **(A) Apply the branch's removal (preferred — keeps code=state=reality).**
  Merge / check out `feat/lb-collapse-reserved-ips`, then the next inner apply
  destroys the already-gone LB out of state (a no-op destroy, since the API
  object is missing):
  ```bash
  # in src/pyxcloud-production, on feat/lb-collapse-reserved-ips
  cd .pyxcloud/environments/production
  terraform plan      # expect: 1 to destroy (digitalocean_loadbalancer.edge-lb-prod)
  terraform apply     # DO returns 404 on the LB -> terraform drops it from state cleanly
  ```
- **(B) Surgical `state rm` (only if you must NOT apply other pending diffs yet).**
  Detaches the resource from state without touching anything else; then land the
  branch later so the HCL matches:
  ```bash
  cd src/pyxcloud-production/.pyxcloud/environments/production
  terraform state rm 'digitalocean_loadbalancer.edge-lb-prod'
  ```
  > Note: (B) leaves `main`'s HCL still declaring the LB — a subsequent plan would
  > try to RE-CREATE it. (B) is only a stop-gap; you still must land the branch's
  > removal so HCL and state agree. (A) does both at once. **Prefer (A).**

### 3.2 Adopt `pyx-edge-lb` + the 2 edge droplets into a provider-managed root

The consolidated edge is NOT in any IaC. Stand up a provider-managed root from
`rendered/` (or a hand-authored equivalent) and `terraform import` the live
objects so future changes flow through the contract. DO import IDs: LB by UUID,
droplet by numeric id.

**Import the LB (either root shape):**
```bash
terraform import 'digitalocean_loadbalancer.pyx-edge-lb' \
  ef708dea-27df-419c-ab01-c58010e276ac
```

**Import the edge pool — variant (i): discrete droplets** (matches live exactly;
use a root that declares two `digitalocean_droplet` resources instead of the
autoscale group):
```bash
terraform import 'digitalocean_droplet.pyx-edge-1' 582230764
terraform import 'digitalocean_droplet.pyx-edge-2' 582232546
```

**Import the edge pool — variant (ii): the rendered autoscale group** (forward-
managed, self-healing). `digitalocean_droplet_autoscale` imports by its own id;
get it first, then import, and expect the group to adopt / reconcile the two
tagged droplets:
```bash
doctl compute droplet-autoscale list   # find the pyx-edge group id
terraform import 'digitalocean_droplet_autoscale.pyx-edge' <autoscale-group-id>
```
> If no autoscale group exists live (the droplets were created discretely via
> doctl), use **variant (i)** to import reality, OR create the autoscale group
> on-contract and let it converge — but that is a live change, out of scope here.

**Import the VPC + firewall** (if the root manages them; else reference by
`data`):
```bash
terraform import 'digitalocean_vpc.pyx-edge-net'      <vpc-uuid>
terraform import 'digitalocean_firewall.pyx-edge-sg'  <firewall-id>
```

After imports, `terraform plan` MUST be a **no-op** (or only cosmetic diffs like
tag ordering). Any structural diff means the HCL doesn't match reality — fix the
HCL, never apply blindly.

### 3.3 Firewall — add `tag:pyx-edge` on-contract

Live, three service firewalls had `tag:pyx-edge` added via doctl so the edge
droplets can reach the service origins on `:443`:

| Firewall | id |
| --- | --- |
| `sg-sso` | `0a0ee5c2-35e8-4669-b3f7-84435495c632` |
| `sg-backend` | `6b345462-f891-4ae9-8f91-ade89c531ab3` |
| `sg-mcp` | `8ceeed77-cf65-4f39-888f-fcc7e22c9f36` |

On-contract, the DO firewall renderer (`renderSGDO` in
`internal/catalog/render.go`) emits `inbound_rule { source_tags = [...] }` from a
`SecurityRule` whose **`SourceSG`** is set: it maps a canonical peer-SG name to a
DO tag `tfName(r.SourceSG)` (the AWS "allow from this security group" -> DO tag
migration). There is **no** dedicated `SourceTags` field — the tag scope is
reached through `SourceSG`. So an inbound `:443` from the edge pool is a
`SecurityRule{ SourceSG: "pyx-edge" }`, which renders `source_tags = ["pyx-edge"]`.
In an `AssembleInput` this is an `IngressRules` entry on each service env:
```jsonc
// on each of the sso / backend / mcp service environments:
"IngressRules": [
  { "Direction": "ingress", "Protocol": "tcp", "FromPort": 443, "ToPort": 443,
    "SourceSG": "pyx-edge" }
]
```
Land that in each service's topology and re-render so the firewall's
`tag:pyx-edge` source is owned by IaC rather than the live doctl edit.

> **Caveat:** `tfName()` is applied to the SG name before it becomes the tag, so
> the `SourceSG` value must be the tag string as it appears live (`pyx-edge`);
> confirm `tfName("pyx-edge") == "pyx-edge"` (it is — no chars are rewritten) so
> the rendered `source_tags` matches the live tag. If you need a tag that
> `tfName` would rewrite, that is a genuine gap (see §6.4).

### 3.4 Dangling firewall rules — cleanup (doctl, listed NOT run)

`sg-sso` and `sg-backend` still carry `load_balancer_uid` inbound rules pointing
at the **now-deleted** per-service LBs (`lb-sso`, `lb-sso-prod`, `lb-backend`).
These are dangling references. Inspect, then remove them (review the exact
`load_balancer_uid` values first — do not guess the UIDs):

```bash
# 1. INSPECT — find the dangling load_balancer_uid inbound rules:
doctl compute firewall get 0a0ee5c2-35e8-4669-b3f7-84435495c632 --format InboundRules -o json   # sg-sso
doctl compute firewall get 6b345462-f891-4ae9-8f91-ade89c531ab3 --format InboundRules -o json   # sg-backend

# 2. REMOVE each dangling rule (fill in the real port + the deleted LB's UID):
#    (doctl removes an inbound rule by its full spec; a load_balancer_uid rule:)
doctl compute firewall remove-rules 0a0ee5c2-35e8-4669-b3f7-84435495c632 \
  --inbound-rules "protocol:tcp,ports:443,load_balancer_uid:<DELETED_LB_UID>"
doctl compute firewall remove-rules 6b345462-f891-4ae9-8f91-ade89c531ab3 \
  --inbound-rules "protocol:tcp,ports:443,load_balancer_uid:<DELETED_LB_UID>"
```
> These rules reference LB UIDs that no longer resolve, so they are inert (they
> can't match traffic), but they are noise and should be cleared. Once the
> service firewalls are provider-managed (§3.3), the correct inbound source is
> `tag:pyx-edge`, not a `load_balancer_uid` — so the dangling rules must NOT be
> re-added to the IaC.

---

## 4. Post-reconciliation checklist

- [ ] `feat/lb-collapse-reserved-ips` landed (or `state rm`) — `edge-lb-prod`
      gone from prod state; `terraform plan` clean in the prod estate.
- [ ] `pyx-edge-lb` + edge pool imported into a provider-managed root; `plan` no-op.
- [ ] `tag:pyx-edge` inbound `:443` expressed on-contract on sso/backend/mcp SGs.
- [ ] Dangling `load_balancer_uid` rules removed from `sg-sso` / `sg-backend`.
- [ ] `lb-vault-internal` untouched (KEPT).

---

## 5. Files in this directory

| File | Purpose |
| --- | --- |
| `topology.json` | `catalog.AssembleInput` for `pyx-edge-lb` + the SNI-router pool |
| `rendered/pyx-*.tf` | provider output (validated `terraform init/validate`) |
| `README.md` | this runbook |

## 6. Provider-expressiveness gaps hit

1. **Fixed-VM DO render is not fleet-taggable.** `renderVMDO`
   (`internal/catalog/render.go`) hardcodes `tags = ["pyxcloud"]` and emits no
   `ssh_keys`. A `virtual-machine` component therefore cannot carry a
   fleet-selection tag (needed for `droplet_tag` LB targeting) or an ssh key. The
   `virtual-machine-scale-group` path (`renderScaleGroupDO`) supports both, so the
   pool must be modelled as a scale-group. **Fix (future):** thread `Tag`/`SSHKeys`
   through `VMSpec`/`VMPlan` and `renderVMDO`, mirroring the scale-group, so a
   fixed N-droplet tagged pool is expressible without the autoscale wrapper — which
   would also let a provider-managed root import the **discrete** live droplets on
   contract (see §3.2 variant i).
2. **No `size_unit` on the DO LB.** `renderLBDO` emits no `size_unit`, so it
   defaults to `1` (which happens to match the live `size_unit=1`). Fine today, but
   a larger edge would need `size_unit`/`size` threaded onto `LoadBalancerSpec`.
3. **State-shape mismatch on import.** The contract's DO pool is an autoscale
   group; the live pool is discrete droplets. Gap (1)'s fix removes this friction.
4. **DO firewall source-by-tag rides on `SourceSG` + `tfName`.** There is no
   first-class `SourceTags` scope on `SecurityRule`; source-by-DO-tag is reached
   only through `SourceSG`, and the value is passed through `tfName()` before it
   becomes the tag. That is fine for `pyx-edge` (`tfName` is a no-op on it), but a
   tag containing characters `tfName` rewrites (uppercase, dots, etc.) could NOT
   be expressed faithfully. **Fix (future):** add an explicit `SourceTags []string`
   scope on `SecurityRule` that renders verbatim to DO `source_tags` without the
   `tfName` rewrite, so arbitrary live tags are addressable on-contract.
