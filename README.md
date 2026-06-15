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
  `aws`, `gcp`, `digitalocean`.
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

### Round-trip testing (SPEC §6)

```sh
go build -o pyxnet-render ./cmd/pyxnet-render
examples/region-vpc/roundtrip.sh   # plan all 3; apply+verify+destroy where creds exist
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
