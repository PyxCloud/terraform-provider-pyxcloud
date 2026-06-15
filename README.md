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
| `region`     | string | required — macro-region, e.g. `EU West`            |
| `components` | list   | required — nested blocks (below)                   |

Each `components` block: `name` (req), `type` (req), `count` (opt, default 1),
and an optional `vm { architecture, cpu, ram, os_name }` sizing block.

Implements full CRUD (Create / Read / Update / Delete) against the client
interface.

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

Do **not** run `terraform apply` against real cloud or the live API — this MVP
is for compile/demo only.
