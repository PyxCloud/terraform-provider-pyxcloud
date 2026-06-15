# terraform-provider-pyxcloud

A Terraform provider for the [PyxCloud](https://passo.build) platform, built on
the modern [terraform-plugin-framework](https://github.com/hashicorp/terraform-plugin-framework).

> **Status: MVP scaffold (`pd-FEAT-TF-PROVIDER`).** The resource and data-source
> schemas are real and well-modeled. Network calls against the live PyxCloud API
> are **stubbed** behind a client interface (in-memory storage + synthetic
> pricing) so the provider compiles, vets, tests, and demos without touching the
> network or any cloud. See [What's stubbed](#whats-stubbed-vs-real).

## The PyxCloud abstraction: canonical topology

PyxCloud describes infrastructure with a **canonical topology** — a
provider-independent model of an infrastructure stack. The same model the
product's wizard builds and the console **Compare** page prices across
providers. A topology is a list of typed **components** with **sizing**, pinned
to a deployment **provider** and an abstract **macro-region**:

- **Component types** (mirrors backend `TopologyInspector` / `PricingRanker`):
  `virtual-machine`, `virtual-machine-scale-group`, `managed-database`,
  `load-balancer`, `cache`, `object-storage`, `blob-storage`, ...
- **VM sizing** (mirrors `properties.virtual-machine.type.*` / `os.osName`):
  `architecture`, `cpu`, `ram`, `os_name`.
- **Providers** (mirrors vibe-frontend `ENABLED_LAUNCH_PROVIDERS`):
  `aws`, `gcp`, `digitalocean`, `oracle` (wave-2 Oracle Cloud / OCI).
- **Macro-region**: abstract region such as `EU West`, `US East`, `Asia` —
  resolved to a concrete CSP region at deploy time.

## Provider configuration

```hcl
provider "pyxcloud" {
  endpoint = "https://passo.build"  # default
  # token  = "..."                  # OAuth/SSO bearer; or export PYXCLOUD_TOKEN
}
```

Auth is an OAuth/SSO-issued bearer token, matching how the platform
authenticates. It can be supplied via the `token` attribute or the
`PYXCLOUD_TOKEN` environment variable.

## Resource: `pyxcloud_topology`

Manages a canonical topology.

| Attribute    | Type   | Notes                                              |
| ------------ | ------ | -------------------------------------------------- |
| `id`         | string | computed, server-assigned                          |
| `name`       | string | required                                           |
| `provider`   | string | required — `aws` \| `gcp` \| `digitalocean`        |
| `region`     | string | required — abstract pyx `region_name`, e.g. `Frankfurt` |
| `components` | list   | required — nested blocks (below)                   |

Each `components` block: `name` (req), `type` (req), `count` (opt, default 1),
and an optional `vm { architecture, cpu, ram, os_name }` sizing block.

Implements full CRUD (Create / Read / Update / Delete) against the client
interface.

### Network / VPC (`pd-TF-REGION-VPC`)

A topology can declare an **abstract network** for its place. The provider
descends it to a concrete VPC + subnets via the **region catalog** — no provider
region maps are baked into the provider binary.

```hcl
resource "pyxcloud_topology" "web" {
  name     = "web-stack"
  provider = "aws"        # aws | gcp | digitalocean
  region   = "Frankfurt"  # abstract pyx region_name

  network = {
    cidr    = "10.0.0.0/16"
    subnets = ["10.0.1.0/24", "10.0.2.0/24", "10.0.3.0/24"]
  }
}

output "plan" { value = pyxcloud_topology.web.network_plan }
```

**Catalog-driven resolution.** `region` (the abstract pyx `region_name`) +
`provider` resolve to a concrete `csp_region` straight from the `region` catalog
(the same table the wizard / Compare page use — `RegionResolver` server-side):

| `region` (abstract) | aws            | gcp            | digitalocean |
| ------------------- | -------------- | -------------- | ------------ |
| `Frankfurt`         | `eu-central-1` | `europe-west3` | `fra1`       |
| `Dublin`            | `eu-west-1`    | —              | —            |
| `Amsterdam`         | —              | `europe-west4` | `ams3`       |

A region with no entry for a provider is a **hard plan-time error**, never a
silent fallback.

**Computed `network_plan`** (the concrete translation surfaced back into state):

| Field           | Notes                                                       |
| --------------- | ---------------------------------------------------------- |
| `csp_region`    | catalog-resolved concrete region                           |
| `resource_type` | `aws_vpc` \| `google_compute_network` \| `digitalocean_vpc` |
| `subnets[]`     | `{ name, cidr, zone }` — multi-AZ zone derived per provider |

Per-provider targets emitted:

- **AWS** — `aws_vpc` + `aws_subnet` (one per subnet, spread multi-AZ:
  `eu-central-1a/b/c`).
- **GCP** — `google_compute_network` + `google_compute_subnetwork` (regional).
- **DigitalOcean** — `digitalocean_vpc` (region-scoped, no sub-zones).

The structured `network_plan` is rendered to concrete cloud-provider HCL by
[`cmd/pyxnet-render`](cmd/pyxnet-render) for the round-trip tests below.

### Security-group / firewall (`pd-TF-SG`)

A topology can declare an **abstract security-group** attached to its place's
`network`: a canonical `expose` port shorthand plus explicit ingress/egress
rules. The provider descends it to the concrete firewall resources per provider
— same catalog-driven, structured-plan → render pattern as the network.

```hcl
resource "pyxcloud_topology" "web" {
  name     = "production"
  provider = "aws"        # aws | gcp | digitalocean
  region   = "Frankfurt"

  network = { cidr = "10.0.0.0/16", subnets = ["10.0.1.0/24"] }

  security_group = {
    description = "web tier"        # sanitised to ASCII at plan time
    expose      = [80, 443]         # TCP ingress from 0.0.0.0/0 + ::/0

    rules = [
      { direction = "ingress", protocol = "tcp", from_port = 22, to_port = 22, cidrs = ["10.0.0.0/16"] },
      { direction = "ingress", protocol = "tcp", from_port = 8080, to_port = 8080, source_sg = "lb" },
      { direction = "egress",  protocol = "all", cidrs = ["0.0.0.0/0"] },
    ]
  }
}

output "sg_plan" { value = pyxcloud_topology.web.security_group_plan }
```

- **`expose`** is the canonical shorthand: each listed port is opened TCP ingress
  from anywhere (IPv4 + IPv6), expanded into explicit rules at plan time.
- **Rules** are scoped to either `cidrs` **or** `source_sg` (a peer
  security-group reference) — mutually exclusive. Protocols: `tcp`, `udp`,
  `icmp`, `all`.

**ASCII-only descriptions.** AWS rejects non-ASCII security-group descriptions
(this caused a real incident), so the description is **ASCII-sanitised at plan
time** for every provider — non-ASCII runes are stripped before render.

**Provider limits** are enforced as **hard plan-time errors** (never a silent
trim): AWS ≤ 60 rules/direction, GCP ≤ 100 rules/firewall, DO ≤ 50
rules/direction. DigitalOcean has no `all` protocol, so an `all` rule on DO is a
hard plan-time error — declare explicit `tcp`/`udp`/`icmp` rules instead.

Per-provider targets emitted:

- **AWS** — `aws_security_group` + one `aws_security_group_rule` per rule
  (IPv4/IPv6 CIDRs split into `cidr_blocks` / `ipv6_cidr_blocks`; `source_sg` →
  `source_security_group_id`).
- **GCP** — `google_compute_firewall`, one per direction (GCP firewalls are
  direction-scoped); ingress CIDRs → `source_ranges`, egress → `destination_ranges`.
- **DigitalOcean** — `digitalocean_firewall` with `inbound_rule` / `outbound_rule`
  blocks (attaches to droplets/tags, not a VPC).

Run [`examples/security-group/roundtrip.sh`](examples/security-group) to plan all
three and apply→verify→destroy where creds exist. The DO harness uses a separate
fixture (`sg-do.json`) since DO has no `all` protocol.

### Virtual-machine (`pd-TF-EC2-VM`)

A topology can declare an **abstract virtual-machine** placed in its `network`
(region+VPC) and attached to its `security_group`. The user sizes it in
PyxCloud's canonical vocabulary — `architecture`, `cpu`, `ram`, `os` — and the
provider **resolves it down** to a concrete provider instance type and image
from the catalog. Same catalog-driven, structured-plan → render pattern as the
network and security-group.

```hcl
resource "pyxcloud_topology" "app" {
  name     = "production"
  provider = "aws"        # aws | gcp | digitalocean
  region   = "Dublin"

  network        = { cidr = "10.0.0.0/16", subnets = ["10.0.1.0/24"] }
  security_group = { expose = [80, 443] }

  virtual_machine = {
    architecture = "x86_64"   # x86_64 | arm64
    cpu          = 2          # abstract vCPU
    ram          = 4          # abstract GiB
    os           = "ubuntu"   # ubuntu | debian (os_version optional; defaults ubuntu 24.04 / debian 12)
    count        = 2          # -> N instances
  }
}

output "vm_plan" { value = pyxcloud_topology.app.virtual_machine_plan }
```

**Catalog-driven SKU resolution (the crux).** `{provider, csp_region,
architecture, cpu, ram}` resolves to a concrete instance `name` from the
**`virtual_machine`** catalog (`vm_catalog.csv`, a snapshot of the live table for
the wave-1 test regions). It is an **exact** cpu/ram match; among ties the
general-purpose / burstable family wins (deterministic, not a hard-coded map).
A no-match is a **hard plan-time error** that lists the nearest available sizes —
never a silent fallback to a different size. Example resolutions:

| canonical sizing | AWS (eu-west-1) | GCP (europe-west3) | DO (fra1) |
|---|---|---|---|
| x86_64, 2 vCPU, 4 GiB | `t3.medium` | `e2-medium` | `s-2vcpu-4gb` |
| arm64, 2 vCPU, 4 GiB | `t4g.medium` | _(no arm in snapshot)_ | _(x86 only)_ |
| x86_64, 2 vCPU, 1 GiB | `t3.micro` | `e2-micro` | `s-1vcpu-1gb` |

**OS → image mapping** comes from the `virtual_machine_operating_system` catalog
(`vm_os_catalog.csv`): AWS rows carry the real per-region **AMI id**, DO rows the
real **image slug** (`ubuntu-24-04-x64`, `debian-12-x64`). GCP ubuntu has no
usable id in the catalog (the live `csp_os_name` is empty / a pinned URL), so GCP
uses the stable **image-family** form (`ubuntu-os-cloud/ubuntu-2404-lts-amd64`,
`debian-cloud/debian-12`) per SPEC §5.3. An unsupported os/version is a hard
plan-time error.

Per-provider targets emitted (`count` → N instances, wired to the sibling
subnet + security-group):

- **AWS** — `aws_instance` (`instance_type` + `ami` from catalog; `subnet_id`,
  `vpc_security_group_ids` reference the network/SG resources).
- **GCP** — `google_compute_instance` (`machine_type` + boot-disk `image`; zonal,
  zone derived as `<csp_region>-a`; `network` + `subnetwork` references).
- **DigitalOcean** — `digitalocean_droplet` (`size` + `image` + `region`,
  `vpc_uuid` references the VPC).

Run [`examples/virtual-machine/roundtrip.sh`](examples/virtual-machine) to plan
all three and apply→verify→destroy where creds exist. AWS uses `vm-aws.json`
(Dublin → `eu-west-1`, where the snapshot has instance types); gcp/do use
`vm.json` (Frankfurt → `europe-west3` / `fra1`).

### Load-balancer (`pd-TF-LB`)

A topology can declare an **abstract load-balancer** placed in its `network`
(spread multi-AZ across its subnets) and attached to its `security_group`. The
user declares `listeners` (port + protocol), a `target` (the autoscaled
`scale_group` fleet or a fixed `virtual_machine`), a `health_check`, and optional
`stickiness`; the provider **resolves it down** to each cloud's standard
load-balancer resources. Same catalog-driven (region resolution + multi-AZ
zones), structured-plan → render pattern as the other components.

```hcl
resource "pyxcloud_topology" "app" {
  name     = "production"
  provider = "aws"        # aws | gcp | digitalocean
  region   = "Dublin"

  network        = { cidr = "10.0.0.0/16", subnets = ["10.0.1.0/24", "10.0.2.0/24"] }
  security_group = { expose = [80, 443] }
  scale_group    = { name = "web", cpu = 2, ram = 4, min = 2, max = 6, health = "elb" }

  load_balancer = {
    listeners    = [{ port = 80, protocol = "http" }, { port = 443, protocol = "https" }]
    health_check = { protocol = "http", port = 80, path = "/" }
    stickiness   = true
    target_kind  = "scale-group"   # scale-group (default) | vm
    # target_name defaults to the sibling scale_group / virtual_machine name
  }
}

output "lb_plan" { value = pyxcloud_topology.app.load_balancer_plan }
```

Per-provider targets emitted (the target group/backend is wired onto the ASG/MIG
so the autoscaled fleet registers automatically; a `vm` target attaches the
fixed instance):

- **AWS** — `aws_lb` (application LB, internet-facing, multi-subnet) +
  `aws_lb_target_group` + an `aws_lb_listener` per listener (+ a `lb_cookie`
  `stickiness` block when requested). A `scale-group` target gets an
  `aws_autoscaling_attachment` (target-group ARN onto the ASG); a `vm` target
  gets an `aws_lb_target_group_attachment`. The LB also emits the internet-egress
  wiring an internet-facing ALB needs but the network component does not own — an
  `aws_internet_gateway` + a public `aws_route_table` + per-subnet associations.
  ALB listener rules respect the **≤ 5 condition-value** per-rule quota (a breach
  is a hard plan-time error); descriptions stay ASCII.
- **GCP** — a regional `google_compute_health_check` + `google_compute_region_backend_service`
  (the MIG instance group is the `backend`; `GENERATED_COOKIE` session affinity
  when sticky) + a `google_compute_forwarding_rule` per listener.
- **DigitalOcean** — `digitalocean_loadbalancer` with a `forwarding_rule` per
  listener, a `healthcheck`, and `sticky_sessions` (cookies) when requested,
  targeting droplets by the `pyxcloud` tag. DigitalOcean has no native VM
  autoscaling, so a DO LB fronts a fixed/managed droplet set (no scale-group).

The catalog has no `load_balancer` SKU table for wave-1, so the LB **shape** is
provider-standard; the catalog still drives region resolution and the multi-AZ
zone spread (`load_balancer(_price)` can fill SKU/tier later without changing the
contract). A missing/unavailable region is a hard plan-time error.

Run [`examples/load-balancer/roundtrip.sh`](examples/load-balancer) to plan all
three and apply→verify→destroy where creds exist. AWS uses `lb-aws.json` (Dublin
→ `eu-west-1`, ASG min=1/max=1 `t3.micro` + a single HTTP listener to minimise
cost — a load balancer costs money, so the harness destroys immediately); gcp/do
use `lb.json` (Frankfurt → `europe-west3` / `fra1`).

### Managed-database (`pd-TF-MDB`)

A topology can declare an **abstract managed-database** placed in its `network`
(its subnets become the DB subnet group, spread multi-AZ) and reachable from its
`security_group`. The user declares `engine` (`postgres`/`mysql`), `version`,
sizing (`cpu`, `ram`), `storage_gb`, `ha`, and `encrypted`; the provider
**resolves it down** to each cloud's managed-database resource. The concrete DB
instance class comes from the `managed_database` catalog (a missing sizing/region
is a hard plan-time error — never an invented class).

```hcl
resource "pyxcloud_topology" "app" {
  name     = "production"
  provider = "aws"        # aws | gcp | digitalocean
  region   = "Frankfurt"

  network        = { cidr = "10.0.0.0/16", subnets = ["10.0.1.0/24", "10.0.2.0/24"] }
  security_group = { name = "production-db", rules = [{ direction = "ingress", protocol = "tcp", from_port = 5432, to_port = 5432, cidrs = ["10.0.0.0/16"] }] }

  managed_database = {
    engine     = "postgres"   # postgres (default) | mysql
    version    = "16"
    cpu        = 2
    ram        = 4
    storage_gb = 50
    ha         = true         # Multi-AZ / REGIONAL / 2-node cluster
    encrypted  = true
    # deletion_protection / skip_final_snapshot default to the production-safe
    # values (protection ON, final snapshot taken). The TEST round-trip sets
    # deletion_protection = false + skip_final_snapshot = true ONLY for clean
    # teardown — that override is test-only.
  }
}

output "db_plan" { value = pyxcloud_topology.app.managed_database_plan }
```

Per-provider targets emitted:

- **AWS** — `aws_db_subnet_group` (multi-AZ across the region's subnets) +
  `aws_db_instance` (RDS). `storage_encrypted`, `multi_az` (HA),
  `deletion_protection`, and a **final snapshot** (`skip_final_snapshot = false`
  + a `final_snapshot_identifier`) — the production-safe defaults. Instance class
  from the catalog; the app `security_group` is wired via `vpc_security_group_ids`.
- **GCP** — `google_sql_database_instance` with `settings { tier, disk_size,
  availability_type = REGIONAL when HA }`, private-network IP config, backups, and
  `deletion_protection`.
- **DigitalOcean** — `digitalocean_database_cluster` (`size` from the catalog,
  `node_count = 2` when HA, region + private VPC). DO clusters are encrypted at
  rest by default and have no in-place deletion-protection flag, so that intent is
  carried as a `lifecycle { prevent_destroy = true }` guard.

**Data-safety guard (why MDB is special).** On RDS (and the GCP/DO analogues)
some attribute changes are **not** applied in place — they **force a replacement**
of the instance, which **destroys the data**. The 2026-06-15 RDS data-loss
incident was exactly this: a flag flip Terraform happily planned as a replace,
silently dropping a production database. The provider diffs the prior plan
(state) against the new plan at **plan time** (`ModifyPlan`) and raises a hard
plan-time **error** — never a silent replace — when any of these change on an
**existing** DB:

- `encrypted` (RDS `storage_encrypted` is immutable; enabling it goes via
  copy-snapshot-with-KMS → restore)
- `engine` (downgrade / cross-engine change)
- `identifier` (DB name change)
- storage-type / class-family change

The error directs the operator to a **snapshot-restore migration** (snapshot →
restore into a new DB with the desired settings → cut over → retire the old one).
A fresh create and in-place changes (same-family resize, storage increase, HA
toggle) pass.

Run [`examples/managed-database/roundtrip.sh`](examples/managed-database) to plan
all three and apply→verify→destroy where creds exist. AWS uses `db-aws.json`
(Frankfurt → `eu-central-1`, smallest `db.t3.micro` postgres + 20 GiB minimum,
with the visible test-only teardown override — RDS costs money and takes minutes,
so the harness destroys immediately); gcp/do use the production `db.json`
(Frankfurt → `europe-west3` / `fra1`).

### Object/blob storage (`pd-TF-S3`)

A topology can declare an **abstract object/blob-storage** bucket placed in its
region. The user declares `name`, `versioning`, and `public` (default `false`);
the provider **resolves it down** to each cloud's object-store resource. There is
no sizing catalog — a bucket is region/location-scoped and billed per-usage — so
the only catalog lookup is the region (`region_name` + provider → `csp_region`;
a missing region is a hard plan-time error).

```hcl
resource "pyxcloud_topology" "app" {
  name     = "production"
  provider = "aws"        # aws | gcp | digitalocean
  region   = "Frankfurt"

  object_storage = {
    name       = "app-assets"
    versioning = true
    public     = false     # PRIVATE BY DEFAULT — enforces the public-access-block
    # force_destroy defaults to false (refuse to drop a non-empty bucket). The
    # TEST round-trip sets it true ONLY for clean teardown — that override is
    # test-only.
  }
}

output "bucket_plan" { value = pyxcloud_topology.app.object_storage_plan }
```

**Private by default (security invariant, SPEC §5.7).** `public` defaults to
`false`, and the provider **never** emits a world-readable bucket by default:

- **AWS** — `aws_s3_bucket` + `aws_s3_bucket_versioning` +
  `aws_s3_bucket_public_access_block`. When not public, **all four** block flags
  (`block_public_acls`, `block_public_policy`, `ignore_public_acls`,
  `restrict_public_buckets`) are `true`, so an errant ACL/policy can never expose
  the bucket.
- **GCP** — `google_storage_bucket` with `uniform_bucket_level_access = true`
  (no per-object ACLs) and, when not public, `public_access_prevention = enforced`.
- **DigitalOcean** — `digitalocean_spaces_bucket` with `acl = private` (only
  `public-read` when explicitly public), region-mapped, versioning block.

Making a bucket public is an **explicit opt-in** (`public = true`).

**Globally-unique-safe bucket naming.** S3/GCS/Spaces share a global (or
per-provider-global) bucket namespace, so a bare logical name would collide
across accounts/regions/providers. The concrete `bucket_name` is derived
deterministically: the logical name is lower-cased and reduced to the DNS-bucket
charset `[a-z0-9-]`, then suffixed with a short hex hash of
`(csp | csp_region | name)` and clamped to the 63-char cross-provider limit. The
**same** logical name in two regions/providers yields **distinct** global names,
and the derivation is pure/stable so plans are idempotent (e.g. `app-assets` →
`app-assets-05c5013263` for AWS Frankfurt).

Run [`examples/object-storage/roundtrip.sh`](examples/object-storage) to plan all
three and apply→verify→destroy where creds exist. AWS uses `storage-aws.json`
(Frankfurt → `eu-central-1`, with the visible test-only `force_destroy = true`
override so a just-created bucket tears down cleanly); verification reads back
`get-bucket-versioning` (Enabled) and `get-public-access-block` (all four flags
true). gcp/do use the production `storage.json` (Frankfurt → `europe-west3` /
`fra1`).

### Round-trip testing (SPEC §6)

```sh
go build -o pyxnet-render ./cmd/pyxnet-render
examples/region-vpc/roundtrip.sh        # network: plan all 3; apply+verify+destroy where creds exist
examples/security-group/roundtrip.sh    # security-group
examples/virtual-machine/roundtrip.sh   # virtual-machine
examples/scale-group/roundtrip.sh       # virtual-machine-scale-group
examples/load-balancer/roundtrip.sh     # load-balancer
examples/managed-database/roundtrip.sh  # managed-database
examples/object-storage/roundtrip.sh    # object/blob storage
```

The harness generates the concrete `.tf` from the canonical fixture
([`examples/region-vpc/place.json`](examples/region-vpc/place.json)), runs
`terraform plan` for aws/gcp/do, and does a real `apply` → verify → `destroy`
for any provider whose test creds are present (missing creds are skipped
**explicitly**, never silently).

## Data source: `pyxcloud_compare`

Prices a canonical topology across candidate `(provider, region)` pairs and
returns per-candidate cost **cheapest-first** — the Terraform analogue of the
console Compare page (backend `PricingRanker.rank`).

**Inputs:** `name` (opt), `components` (same shape as the resource),
`candidates` (list of `{ provider, region }`).

**Outputs:**

- `results` — list of `{ provider, region, hourly_usd, monthly_usd, priceable }`,
  cheapest first.
- `cheapest` — the single cheapest priceable candidate.

```hcl
data "pyxcloud_compare" "options" {
  components { name = "app" type = "virtual-machine" count = 3
    vm { architecture = "x86_64" cpu = "2" ram = "4" os_name = "ubuntu" } }
  components { name = "db" type = "managed-database" }

  candidates { provider = "aws"          region = "EU West" }
  candidates { provider = "gcp"          region = "EU West" }
  candidates { provider = "digitalocean" region = "EU West" }
}

output "cheapest" { value = data.pyxcloud_compare.options.cheapest }
```

A full example lives in [`examples/main.tf`](examples/main.tf).

## What's stubbed vs real

| Real                                                    | Stubbed (MVP)                                          |
| ------------------------------------------------------- | ----------------------------------------------------- |
| Provider / resource / data-source **schemas**           | Topology persistence (in-memory map, not the API)     |
| Full CRUD wiring + state mapping                         | Pricing (deterministic synthetic cost, not live SP)   |
| Canonical model (matches backend vocabulary)            | HTTP transport / bearer auth (interface only)         |
| Provider config + env fallback + cheapest-first ranking | `Compare` against the live console pricing endpoint   |

All stubs sit behind `internal/client.Client`; a future `HTTPClient`
implementation backs the same interface with real calls (see `// TODO` markers).

## Build, test, validate

```sh
go mod tidy
go build ./...
go vet ./...
go test ./...
```

The `pd-TF-REGION-VPC` network component **is** validated against real cloud in a
disposable test environment (see [Round-trip testing](#round-trip-testing-spec-6));
the topology/compare CRUD against the live PyxCloud API remains stubbed.
