# PyxCloud Terraform Provider — Specification

> Task `pd-TF-SPEC` (epic `EPIC-TF-IAC`). This is the **gating** document: define the spec
> extensively first, then implement one component at a time with tests (`pd-TF-REGION-VPC` →
> `pd-TF-SG` → `pd-TF-EC2-VM` → `pd-TF-ASG` → `pd-TF-LB` → `pd-TF-MDB` → `pd-TF-S3` →
> `pd-TF-REST-LAMBDA`), for AWS + GCP + DigitalOcean first, then the wave‑2 providers.

## 1. Principles

1. **Abstract‑first (invert the process).** Every other PyxCloud surface starts from raw
   provider data and rolls it up. Here we do the opposite: the user declares infrastructure in
   PyxCloud's **canonical** vocabulary, and the provider **descends** that abstract model into a
   concrete provider's resources. We do not start from `aws_instance`; we start from
   `virtual-machine` and resolve it down.
2. **Keep the logic linear to how we already built it.** The translation reuses the **same core
   that sits between the backend and the DB** — the censused catalog tables (`region`,
   `virtual_machine`, `*_price`) and the existing `io.pyxcloud.devops.terraform` strategy layer +
   `terraform_parts` template library. The provider is a thin, declarative front end over that
   core; it must not fork a second translation engine.
3. **The `compare` object is the placement contract.** For each **macro logical place**, the
   compare object carries **both** the abstract PyxCloud region **and** the chosen cloud provider.
   The provider's "API implementation" is, literally, the translation of that compare selection
   into the target provider(s).
4. **The provider is also the migration engine.** Re‑pointing a macro place's provider in the
   compare object is a provider→provider migration; the provider plans and executes the cutover
   and the data move (DB, blob/object storage, secrets, queues) behind CRIU + rsync, hiding that
   complexity (`pd-TF-PROVIDER-MIGRATION`).
5. **No exotic products.** Cover the macro components that map cleanly across providers. Skip
   provider‑specific niche services.

## 2. Where it sits (architecture)

```
 pyx .tf (canonical)            PyxCloud Provider           PyxCloud BE  ── DB (censused tables)
 ┌───────────────────┐  CRUD   ┌──────────────────┐  HTTP  ┌──────────────────────────────────┐
 │ resource "pyxcloud │ ─────▶  │ schema + plan    │ ─────▶ │ /api/topology  (persist canonical) │
 │   _topology"       │         │ (abstract model) │        │ /api/compare   (PricingRanker)     │
 │ data "pyxcloud     │ ◀─────  │ translate via    │ ◀───── │ /api/translate (canonical→concrete │
 │   _compare"        │  state  │ BE catalog       │        │   via region/virtual_machine/...)  │
 └───────────────────┘         └──────────────────┘        │ devops/terraform strategies +      │
                                                            │ terraform_parts                    │
                                                            └──────────────────────────────────┘
```

The provider never embeds provider region maps or instance‑type tables — it asks the BE, which
reads the **same** catalog the wizard and Compare page use. The BE exposes (to be added where
missing): `POST /api/topology`, `GET/PUT/DELETE /api/topology/{id}`, `POST /api/compare`, and a
`POST /api/translate` that returns the concrete per‑provider resource plan for a canonical
topology + a compare selection.

## 3. The canonical model

### 3.1 Component vocabulary (canonical types)

Source of truth: `core/pyx-backend/.../service/pyxfile/TopologyInspector.java` and the catalog
tables in `core/database/sql/provider-catalog-inventory`.

| Canonical type | Catalog table | Implemented in component task |
|---|---|---|
| `network` / `vpc` | (derived) | `pd-TF-REGION-VPC` |
| `security-group` | (derived from expose rules) | `pd-TF-SG` |
| `virtual-machine` | `virtual_machine`, `virtual_machine_price` | `pd-TF-EC2-VM` |
| `virtual-machine-scale-group` | `virtual_machine` (+ autoscale flag) | `pd-TF-ASG` |
| `load-balancer` | `load_balancer`, `load_balancer_price` | `pd-TF-LB` |
| `managed-database` | `managed_database`, `managed_database_price` | `pd-TF-MDB` |
| `object-storage` / `blob-storage` | `blob_storage`, `blob_storage_price` | `pd-TF-S3` |
| `cache` | `cache` (+ price) | `pd-TF-REST-LAMBDA` |
| `managed-queue` / `message-queue` | `managed_queue` | `pd-TF-REST-LAMBDA` |
| `event-streaming` / `event-bus` | `event_streaming` | `pd-TF-REST-LAMBDA` |
| `dns-zone`, `cdn-service`, `waf-service` | resp. tables | `pd-TF-REST-LAMBDA` |
| `managed-kubernetes`, `container-service` | resp. tables | `pd-TF-REST-LAMBDA` |
| `serverless-function` (lambda) | `serverless_function` | `pd-TF-REST-LAMBDA` |
| `secrets-manager`, `access-policy` | resp. tables | `pd-TF-REST-LAMBDA` |

### 3.2 Abstract region + macro logical place

Region model (DB `region`): `macro_region` (e.g. "Europe") → `country` → `region_name`
(abstract, e.g. "Frankfurt") → `csp_region` (concrete, e.g. aws `eu-west-1`, gcp `europe-west1`,
do `ams3`), keyed by `csp`. The abstract unit the user picks is **`region_name`** (the pyx
region); the concrete `csp_region` is resolved by `RegionResolver` per chosen provider.

A **macro logical place** is a named placement group in the topology (the Pyxfile `place`). Each
place gets, in the compare object, `{ region: <pyx region_name>, provider: <csp> }`. Different
places may target different providers/regions — that is what enables partial / per‑tier
multi‑cloud and per‑place migration.

### 3.3 Compare object schema (the placement contract)

```hcl
data "pyxcloud_compare" "plan" {
  topology = pyxcloud_topology.app.canonical   # the abstract components
  candidates = [                               # what to price/select per place
    { place = "production", region = "Frankfurt", providers = ["aws","gcp","digitalocean"] },
  ]
}
# outputs (mirrors PricingRanker.ProviderCost): per (place, provider, region_name)
#   hourly_usd, monthly_usd, priceable; plus `selection` = chosen {provider, region} per place.
```

The **selection** (region_name + provider per place) is the input to translation. `priceable`
is false when the catalog has no complete price for that topology in that provider/region.

## 4. Translation contract (abstract → concrete)

For a canonical component in a place with selection `{provider, region_name}`:

1. Resolve `csp_region` = `region(region_name, provider).csp_region`.
2. Resolve the concrete SKU/service from the catalog table for that component+provider+region
   (e.g. `virtual_machine` row matching `{architecture, cpu, ram}` → provider instance `name`).
3. Emit the provider resource(s) using the `terraform_parts` template for
   `(csp, layer, block_type)`, composed in `ordinal` order — the existing
   `AbstractTerraformTemplate` strategy path. The provider returns these as its planned state.

Translation is **deterministic** and **catalog‑driven**: no hard‑coded provider maps in the
provider binary. Missing catalog data (e.g. a provider lacking a region/SKU) surfaces as a
clear plan‑time error, never a silent fallback.

## 5. Per‑component specification (wave‑1: AWS, GCP, DigitalOcean)

Each component below defines: the **abstract schema**, the **per‑provider target**, and the
**catalog source** that fills concrete values. Build them strictly in this order; each is gated
on the previous (`pd-TF-*` deps).

### 5.1 `pd-TF-REGION-VPC` — region + network/VPC
- **Abstract:** `place { region = "Frankfurt"; cidr = "10.0.0.0/16"; subnets = [...] }`.
- **AWS:** `aws_vpc` + `aws_subnet` (multi‑AZ from `csp_region`). **GCP:** `google_compute_network`
  + `google_compute_subnetwork`. **DO:** `digitalocean_vpc`.
- **Catalog:** `region` (region_name → csp_region; AZ/zone derivation per provider).

### 5.2 `pd-TF-SG` — security‑group / firewall
- **Abstract:** canonical `expose: [80,443]` + ingress/egress rules on a network.
- **AWS:** `aws_security_group(_rule)`. **GCP:** `google_compute_firewall`. **DO:**
  `digitalocean_firewall`. ASCII‑only descriptions (AWS regex), ≤ provider rule limits.

### 5.3 `pd-TF-EC2-VM` — virtual‑machine
- **Abstract:** `virtual-machine { architecture, cpu, ram, os }`, `count`.
- **AWS:** `aws_instance` (instance_type from catalog, AMI from os). **GCP:**
  `google_compute_instance` (machine_type, image). **DO:** `digitalocean_droplet` (size, image).
- **Catalog:** `virtual_machine` (match architecture/cpu/ram → provider `name`); OS image map.

### 5.4 `pd-TF-ASG` — virtual‑machine‑scale‑group
- **Abstract:** `virtual-machine-scale-group { min, max, desired, health }` over a VM spec.
- **AWS:** `aws_launch_template` + `aws_autoscaling_group` (ELB health). **GCP:**
  `google_compute_instance_template` + `google_compute_region_instance_group_manager` +
  autoscaler. **DO:** droplet autoscale pool / managed via API. (Mirror the proven MCP/SSO ASG.)

### 5.5 `pd-TF-LB` — load‑balancer
- **Abstract:** `load-balancer { listeners, target = <place/asg>, stickiness }`.
- **AWS:** `aws_lb` (ALB) + `aws_lb_target_group` + listener. **GCP:** forwarding rule + backend
  service + health check. **DO:** `digitalocean_loadbalancer`.
- **Catalog:** `load_balancer(_price)`.

### 5.6 `pd-TF-MDB` — managed‑database
- **Abstract:** `managed-database { engine, version, size, storage, ha }`.
- **AWS:** `aws_db_instance` (RDS). **GCP:** `google_sql_database_instance`. **DO:**
  `digitalocean_database_cluster`. **Data‑safety:** storage/encryption changes that force
  replacement must be blocked at plan time (the data‑loss guard pattern); encryption changes go
  through snapshot‑restore, never in‑place flag flips.
- **Catalog:** `managed_database(_price)`.

### 5.7 `pd-TF-S3` — object/blob storage
- **Abstract:** `object-storage { name, versioning, public=false }`.
- **AWS:** `aws_s3_bucket` (+ public‑access‑block). **GCP:** `google_storage_bucket`. **DO:**
  `digitalocean_spaces_bucket`.

### 5.8 `pd-TF-REST-LAMBDA` — remaining macro components
Same pattern for: `cache` (ElastiCache / Memorystore / DO managed Redis), `managed-queue` /
`message-queue` (SQS / Pub‑Sub / —), `event-streaming` (Kinesis / Pub‑Sub), `dns-zone`
(Route53 / Cloud DNS / DO domains), `cdn-service` (CloudFront / Cloud CDN), `waf-service`,
`managed-kubernetes` (EKS / GKE / DOKS), `secrets-manager` (Secrets Manager / Secret Manager),
and finally `serverless-function` (Lambda / Cloud Functions / DO Functions). No exotic products.

## 6. Test methodology (mandatory per component)

Each `pd-TF-*` component is **not done** until it round‑trips against the real cloud in a
disposable test environment, then cleans up:

1. Generate the provider config from a canonical fixture for **each** of AWS/GCP/DO.
2. `terraform apply` against the **local‑accessible test environment** — for AWS use the
   `pyxcloudtest` profile; GCP/DO use the test credentials.
3. Verify the concrete resource exists and matches the canonical intent (describe/get via CLI).
4. **`terraform destroy` immediately** — leave no test resources running. Assert clean teardown.
5. Capture evidence (apply/destroy logs, the resolved concrete values) in the mesh checkpoint.

CI runs steps 1 + plan + a schema/unit pass on every PR; the real apply/destroy (steps 2–4) is
a gated job using the test profiles, never prod. No silent skips — if a provider's test creds
are absent, the job logs the skip explicitly.

## 7. Roadmap

- **Wave 1 (now):** AWS, GCP, DigitalOcean — components in the order in §5, one PR per component,
  each tested per §6 before the next starts.
- **Wave 2 (`pd-TF-PROVIDERS-WAVE2`, only after wave‑1 is certain):** Azure, Ubicloud, OVH,
  Oracle, IBM, Linode/Akamai, Alibaba, StackIt — same abstract‑first set, same test method.
- **Migration engine (`pd-TF-PROVIDER-MIGRATION`):** switching a place's provider in the compare
  object plans a cutover + data migration (DB dump/restore, blob sync, secrets re‑seal, queue
  drain), masking CRIU + rsync; idempotent, resumable, with a data‑safety interlock.

## 8. Open questions (resolve during `pd-TF-REGION-VPC`)

- Final shape of `POST /api/translate` (does it return rendered `.tf`, or a structured resource
  plan the provider renders?). Prefer a structured plan so the provider owns rendering/state.
- State model: does `pyxcloud_topology` own child resources (one composite) or do we expose
  per‑component resources? Start composite (one topology = one place graph), revisit if needed.
- Auth: the provider uses an SSO‑issued bearer (`PYXCLOUD_TOKEN`); confirm the BE accepts it for
  the `/api/topology|compare|translate` endpoints (passobuild realm).
