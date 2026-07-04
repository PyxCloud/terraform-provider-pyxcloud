# load-balancer `stable_ip` — cost-correct stable-ingress descent (DigitalOcean)

A single-instance service that sits behind a load-balancer **only to have a fixed
public IP** (a stable Cloudflare origin) pays for a `digitalocean_loadbalancer`
(~$12/mo) that never actually balances. That is pure waste.

With `load_balancer.StableIP = true`, the DigitalOcean descent of the abstract
`load-balancer` component **degenerates to a free `digitalocean_reserved_ip`**
bound to the single target droplet instead of a paid balancer. TLS still terminates
on the droplet's own nginx `:443`, exactly as the estate already serves it — and
because a reserved IP is re-attachable, a re-roll / self-heal no longer breaks the
public DNS origin.

This is the **on-contract** expression that supersedes hand-written
`digitalocean_reserved_ip` HCL: the rule lives in the provider renderer
(`renderLBDOReservedIP`), so it applies consistently and a re-render never
re-emits the unnecessary load-balancers.

## Render + validate (plan-only, no apply)

```sh
go run ./cmd/pyxenv-render -in examples/load-balancer-stable-ip/topology.json \
  -out examples/load-balancer-stable-ip/rendered
cd examples/load-balancer-stable-ip/rendered
terraform init -backend=false && terraform validate   # Success
```

The rendered `pyx-05.tf` is a `digitalocean_reserved_ip` bound to
`digitalocean_droplet.sso-1` — **no `digitalocean_loadbalancer` anywhere.**

## Constraints

- DigitalOcean-only. A non-DO `stable_ip` is a hard plan-time error (on aws/gcp
  attach a `reserved-ip` component to the instance, or use a full load-balancer).
- Requires a single VM target (`target_kind = vm`, `target_name` set): a reserved
  IP binds one droplet and cannot front a tagged fleet / scale-group.
- For an **ASG-of-1** service (self-healing scale-group) the stable IP is instead
  reclaimed at boot via the reserved-IP self-claim `user_data` (see
  `do_baseline.go`); the static `droplet_id` binding here is for a fixed
  `virtual-machine`.

## Estate note (pd-lb-collapse)

The PyxCloud DO estate ran 6 load-balancers, all with 0/N healthy backends —
traffic reaches droplets directly via Cloudflare A-records, so the balancers moved
no bytes (~$70/mo of waste). Collapsing them is this component: the affected
single-instance services (sso, backend, mcp) become `load-balancer { stable_ip =
true }`, and vault (VPN-internal, 3 nodes) drops its LB in favour of VPN-internal
DNS. Expressing the estate through this abstraction — rather than raw
`digitalocean_reserved_ip` — is the goal of the estate→provider migration
(pd-TF-PROVIDER-BUILD-MIGRATION).
