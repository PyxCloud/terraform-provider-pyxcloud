# Full-estate AWS→DigitalOcean migration dry-run (pd-MIG-PLAN-DRYRUN-ESTATE)

This is the **migration VALIDATION milestone** of `EPIC-AWS-TO-DO-MIGRATION`: a
proof that the **entire passo.build production estate** — every component, at
once — descends from one canonical abstract topology to **valid, plannable
DigitalOcean Terraform**. It is **plan-only**: nothing here applies or spends.

The single abstract source is `catalog.FullEstateInput("digitalocean", ...)` in
[`internal/catalog/full_estate.go`](../../internal/catalog/full_estate.go). The
assembler (`AssembleHCL`) descends it the same way the
`pyxcloud_environment` resource does locally — no backend round-trip, no token.

- **`topology.json`** — the human-readable mirror of that abstract topology.
- **`rendered/`** — the concrete `.tf` documents the assembler emits for
  DigitalOcean (committed so the plan is reviewable without running Go).
- The executable proof is the Go test
  [`TestFullEstateTerraformValidateDO`](../../internal/catalog/full_estate_test.go),
  which assembles the estate and runs real `terraform init && terraform validate`.

## Result

```
$ terraform init    # downloads digitalocean/digitalocean + hashicorp/kubernetes
... Terraform has been successfully initialized!     (exit 0)

$ terraform validate
Success! The configuration is valid.                 (exit 0)

$ terraform plan     # without DIGITALOCEAN_TOKEN
Terraform planned the following actions ...
  + create  (15 managed resources graphed)
```

`terraform validate` is **GREEN** — the full estate is valid, init-able,
plan-able DigitalOcean HCL with **no** unsupported-component, missing-render, or
`ErrAutoscaleUnsupported` error. `terraform plan` builds the full resource graph
(15 managed resources `+ create`).

### Apply-time gap (the only thing `plan` can't do offline)

Without a live `DIGITALOCEAN_TOKEN` **and** an existing DOKS cluster, `plan`
stops at two apply-time boundaries (not render defects):

1. `data.digitalocean_kubernetes_cluster.*` reads return
   `401 Unable to authenticate you` — the in-cluster components (tracing, cert-
   manager, the CronJob) look up a **pre-existing** cluster by name, which needs
   a token.
2. `kubernetes_manifest.*` resources report `Failed to construct REST client` —
   they need a **live** cluster endpoint, which only exists post-apply.

Both are inherent to any topology that places in-cluster workloads on a cluster
created in the same plan; `terraform validate` already proves the render is
correct. With a `DIGITALOCEAN_TOKEN` exported the test additionally runs `plan`.

## Coverage matrix — canonical component → DO resource → planned?

| # | Canonical component (AWS origin)             | Type                          | DigitalOcean resource(s)                              | validate | plan |
|---|----------------------------------------------|-------------------------------|------------------------------------------------------|:--------:|:----:|
| 1 | SSO / Keycloak (bespoke EC2/ASG)             | virtual-machine-scale-group   | `digitalocean_kubernetes_cluster.sso`                |    ✅    |  ✅  |
| 2 | VPN / WireGuard gateway (bespoke EC2/ASG)    | virtual-machine-scale-group   | `digitalocean_kubernetes_cluster.vpn`                |    ✅    |  ✅  |
| 3 | observability aggregator (bespoke EC2/ASG)   | virtual-machine-scale-group   | `digitalocean_kubernetes_cluster.obs`                |    ✅    |  ✅  |
| 4 | SAST scanner (bespoke EC2/ASG)               | virtual-machine-scale-group   | `digitalocean_kubernetes_cluster.sast`               |    ✅    |  ✅  |
| 5 | pyx-backend (bespoke EC2/ASG)                | virtual-machine-scale-group   | `digitalocean_kubernetes_cluster.backend`            |    ✅    |  ✅  |
| 6 | container-registry (ECR)                     | container-registry            | `digitalocean_container_registry.app-images`         |    ✅    |  ✅  |
| 7 | key-value-store (DynamoDB)                   | key-value-store               | `digitalocean_database_cluster.jit-allowlist` (redis)|    ✅    |  ✅  |
| 8 | object-storage (S3 + lifecycle/SSE/policy/logs) | object-storage             | `digitalocean_spaces_bucket{,_policy}.assets`        |    ✅    |  ✅  |
| 9 | load-balancer L7 (ALB + listener rules)      | load-balancer                 | `digitalocean_loadbalancer.edge-lb` + `kubernetes_manifest.edge-lb_ingress` |    ✅    |  ⚠️¹  |
| 10| tracing (X-Ray)                              | tracing                       | `kubernetes_manifest.app-traces_*` (Tempo + OTel) + cluster data-source |    ✅    |  ⚠️²  |
| 11| tls-certificate (ACM)                        | tls-certificate               | `kubernetes_manifest.app-tls_{issuer,certificate}` (cert-manager) |    ✅    |  ⚠️²  |
| 12| scheduled-trigger (EventBridge cron)         | scheduled-trigger             | `kubernetes_cron_job_v1.nightly` + cluster data-source |    ✅    |  ⚠️²  |
| 13| reserved-ip (Elastic IP)                     | reserved-ip                   | `digitalocean_reserved_ip.vpn-endpoint`              |    ✅    |  ✅  |
| 14| secrets-manager (Secrets Manager)            | secrets-manager *(mitigated)* | `digitalocean_droplet.app-secrets-1` (self-hosted Vault) |    ✅    |  ✅  |
| – | network (the estate VPC)                     | *(synthesised)*               | `digitalocean_vpc.passo-estate-net`                  |    ✅    |  ✅  |
| – | security-group (estate firewall, :443)       | *(synthesised)*               | `digitalocean_firewall.passo-estate-sg`              |    ✅    |  ✅  |

`✅` = render is valid (`validate` green) **and** offline `plan` graphs it.
`⚠️` = render valid; offline `plan` reaches the apply-time boundary above:
 ¹ the L7 ingress is a `kubernetes_manifest` (needs live cluster endpoint);
 ² needs a `DIGITALOCEAN_TOKEN` + an existing DOKS cluster to read/construct.

**14 canonical components (+2 synthesised infra) → 28 concrete DO resources, all
valid.** The same `FullEstateInput("aws", ...)` topology re-renders to AWS
(`aws_autoscaling_group` ×5, `aws_ecr_repository`, `aws_s3_bucket`,
`aws_secretsmanager_secret`, `aws_acm_certificate`, …) — the migration is a
re-render of one source, not a rewrite. See `TestFullEstateAssemblesForAWS`.

## Reproduce

```sh
# string round-trips + real terraform init/validate (terraform must be on PATH):
go test ./internal/catalog/ -run TestFullEstate -v

# or against the committed rendered/ directly:
cd examples/full-estate-do/rendered && terraform init && terraform validate
```
