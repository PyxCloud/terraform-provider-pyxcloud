# Platform ASGs as canonical scale-groups (pd-MIG-PORT-ASGS-CANONICAL)

The PyxCloud platform services — **SSO** (Keycloak), the **VPN** gateway
(WireGuard), the **observability** aggregator, the **SAST** scanner, and the
**backend** (`pyx-backend`) — were historically each a bespoke, per-cloud
autoscaling group (hand-rolled AWS launch templates + `aws_autoscaling_group`,
with no single abstract source of truth).

This migrates them to the **canonical** vocabulary: each platform service is a
`virtual-machine-scale-group` of **1** (`min = desired = 1`, self-healing) over
the existing scale-group translator. The abstract topology is the single source;
the provider **descends** it (SPEC §1):

| Provider     | A scale-group of 1 becomes                                            | Self-heal                       |
| ------------ | -------------------------------------------------------------------- | ------------------------------- |
| DigitalOcean | `digitalocean_kubernetes_cluster` node-pool, `auto_scale = true`     | `min_nodes = 1` (DOKS floor)    |
| AWS          | `aws_launch_template` + `aws_autoscaling_group`                      | `min_size = 1`, instance refresh |

The droplet/instance **size** comes from the same `virtual_machine` SKU
resolution every VM uses (`vm_catalog.csv`) via the requested CPU/RAM — never a
hand-picked instance type. `2vCPU/2GiB` (VPN) is chosen so it resolves on both
DigitalOcean (`s-2vcpu-2gb`) and AWS (`t3.small`).

## Authoritative mapping

The executable mapping is in Go (dependency-free, plan-only, no backend):

- `internal/catalog/platform_asgs.go` — `PlatformServices()` (the sizing table) and
  `PlatformScaleGroupComponents()` (the 5 canonical `AssembleComponent` scale-groups).
- `internal/catalog/platform_asgs_test.go` — the **plan-only round-trip** proving the
  5 services emit valid DigitalOcean resources (DOKS node-pools, `min_nodes = 1`)
  *and* valid AWS resources (5 `aws_autoscaling_group`), from the same topology.

`platform.json` here is the human-readable mirror of that mapping.
