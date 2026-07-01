# AWS → DigitalOcean cutover: residual bespoke gaps

`pd-MIG-CUTOVER-F0-03` (EPIC-AWS-TO-DO-MIGRATION)

This is the authoritative list of production components that, when the canonical
full-prod estate (`internal/catalog/prod_estate.go`) is translated AWS → DO,
have **no DigitalOcean catalog mapping today** — i.e. they cannot descend to a
DO resource through `AssembleHCL`. Each is present in the AWS **source** render
and deliberately **excluded from the DO target** render (so the DO plan is a
clean, plannable plan-only artefact rather than a hard plan-time error). This
list drives phase **F1** (build the missing mappings).

## How the estate is rendered

One abstract source, two concrete renders:

| Render | Constructor | Contents | `terraform validate` (plan-only) |
| --- | --- | --- | --- |
| **Source (AWS)** | `ProdEstateInput("aws", …)` | everything, incl. the AWS-only bespoke components below | **GREEN** |
| **Target (DO)** | `ProdEstateInput("digitalocean", …)` | the same topology **minus** the bespoke gaps below | **GREEN** |

Proven by `TestProdEstateTerraformValidate` (init + validate, both providers).

## What DOES descend to DO cleanly (for reference — NOT gaps)

The bulk of prod already has a first-class DO mapping and is in the DO target:

- 6 platform services (sso / vpn / obs / sast / backend / mcp) → 6 DOKS clusters (node-pools)
- 2 Managed Postgres (keycloak-db 100 GB, pyx-main-db 80 GB) → `digitalocean_database_cluster` (pg 17)
- ~18 S3 buckets → `digitalocean_spaces_bucket` (S3-compatible: versioning + AES256 SSE + lifecycle + access-logs parity)
- shared L7 edge LB → `digitalocean_loadbalancer` + DOKS Ingress (`kubernetes_manifest`)
- container registry (ECR) → `digitalocean_container_registry`
- JIT key-value store (DynamoDB) → DO Managed Redis (`digitalocean_database_cluster`)
- tracing (X-Ray) → Grafana Tempo + OTel operator on DOKS
- monitoring (CloudWatch + SNS) → the LGTM stack on DOKS (kube-prometheus-stack + Loki + Grafana + Alertmanager)
- TLS (ACM) → cert-manager + Let's Encrypt on DOKS
- scheduled-trigger (EventBridge cron) → DOKS `CronJob`
- reserved-ip (Elastic IP) → `digitalocean_reserved_ip`
- prod queue (SQS) → RabbitMQ Cluster Operator on DOKS (B1 operator pattern)
- secrets-manager (Secrets Manager) → Vault-HA operator on DOKS (B4 auto-alias)
- VPC + firewall → `digitalocean_vpc` + `digitalocean_firewall` (synthesised by `AssembleHCL`)

---

## The gaps

### GAP-1 — the 3 frontends (marketing / console / vibe): AWS Amplify → no DO static-site component

- **Component / prod resource:** the three static frontends served historically via **AWS Amplify** static hosting (`aws_amplify_app` / `aws_amplify_branch`) — the marketing site, the console SPA, and the vibe SPA.
- **Why no DO mapping:** the provider has **no `static-site` / static-hosting catalog component** at all. DigitalOcean has no first-class managed equivalent of Amplify's build-and-host-a-SPA-on-a-CDN primitive. The built bundles already have a home in object-storage (`app-assets`, `pyx-frontend`, `vibe-assets` buckets, which DO migrate to Spaces), so the gap is specifically the **managed static-site HOSTING + CDN wrapper**, not the asset storage.
- **In the estate:** the built bundles are modelled as `object-storage` (present in BOTH renders). The Amplify hosting wrapper is **not modelled** (there is no component to model it with) — it is the gap.
- **Proposed target (F1-01):** a **new `static-site` catalog component** → DigitalOcean Spaces static hosting + Cloudflare CDN (origin = the Spaces bucket, TLS + cache at Cloudflare). This is a new resource type, so it is F1 work — not invented here.

### GAP-2 — transactional email (SES): AWS-only, no DO equivalent

- **Component / prod resource:** the SES sending domain `passo.build` (`aws_ses_domain_identity` + `aws_ses_domain_dkim`), modelled as the canonical `email` component (`email-sender`).
- **Why no DO mapping:** `email.go` (`TranslateEmail`) is **AWS-only by design** — it hard-errors on any non-AWS provider (`"only AWS (SES) is supported; … has no managed transactional-email primitive"`). DigitalOcean has no managed transactional-email service.
- **In the estate:** present in the AWS source render (`aws_ses_domain_identity`); **excluded from the DO target** (`prodBespokeAWSOnlyComponents`, gated to AWS).
- **Proposed target (F1-05):** route email to an **external provider** (SendGrid / Postmark / Amazon SES cross-cloud from the DO estate). This needs either a new `email` render path targeting a third-party API, or an accepted decision to keep SES as a cross-cloud dependency. Either way it is external to DO — F1 work.

### GAP-3 — AWS secret rotation Lambda: bespoke, out-of-band

- **Component / prod resource:** native AWS Secrets Manager rotation (`aws_secretsmanager_secret_rotation`), which references a rotation **Lambda ARN** (`var.rotation_lambda_arn`) supplied out of band.
- **Why it's a gap / not modelled with rotation on:** the rotation Lambda is a **bespoke function**, not part of the abstract topology, and the ARN is an out-of-band input. Emitting the rotation resource makes the AWS plan depend on an undeclared variable. The `app-secrets` component therefore sets `RotationDays = 0` (no native-AWS rotation resource).
- **On DO this is a non-issue:** the DO target aliases `secrets-manager` to the **Vault-HA operator**, which performs rotation natively via its own leases — no Lambda involved. So the gap only exists on the AWS side.
- **Proposed target (F1):** either keep rotation as an out-of-band AWS concern (declare `var.rotation_lambda_arn` + supply the Lambda bespoke), or — the migration answer — rely on **Vault-HA rotation** on DO (already the DO render), which removes the Lambda entirely post-cutover.

### GAP-4 — AWS L7 host-based routing to DISTINCT backend services: RESOLVED (pd-MIG-CUTOVER-F1-04)

- **Component / prod resource:** the shared ALB's host-header routing — `admin.passo.build` → sso, `app.passo.build` → backend, `mcp.passo.build` → mcp — i.e. one listener routing distinct hosts to **distinct backend target groups**.
- **Original gap on AWS:** the load-balancer renderer forwarded a host-matched `aws_lb_listener_rule` to the LB's **single default `aws_lb_target_group`** and did **not synthesise a per-service target group** for a rule's `TargetName`. Per-host *distinct-service* routing referenced undeclared target groups on AWS and failed `validate`, so the estate carried the per-host `TargetName` routing only on the DO placement.
- **Fix (F1-04):** the AWS LB renderer now synthesises a **distinct `aws_lb_target_group` per rule `TargetName`** (`<TargetName>_tg`) — with the same health-check/stickiness shape as the default TG — plus an `aws_autoscaling_attachment` wiring that service's scale-group ASG (`<TargetName>_asg`) onto its own target group. Each host `aws_lb_listener_rule` forwards to its per-service TG; rules without a `TargetName` still forward to the LB default TG. The admin-VPN `source_ip` gate is preserved. See `renderLBAWS` / `distinctRuleTargetNames` in `internal/catalog/render.go`.
- **DO parity (unchanged):** the DOKS Ingress (`kubernetes_manifest`) backends a distinct service per host natively (`sso-svc` / `backend-svc` / `mcp-svc`). The estate now carries the per-host `TargetName` routing on **both** providers (`prod_estate.go`), and both AWS and DO renders are `terraform validate` GREEN with per-host distinct targets.
- **Status:** RESOLVED. Verified by `TestProdEstateAssemblesForAWS` / `TestProdEstateAssemblesForDO` (per-host TG + ingress-service assertions) and `TestProdEstateTerraformValidate` (both providers GREEN).

### GAP-5 — DigitalOcean Project envelope: no catalog component

- **Component / prod resource:** the DigitalOcean **Project** (`digitalocean_project`), the account-level resource-grouping envelope that would group every DO resource in the target estate.
- **Why no mapping:** there is **no `project` / resource-group catalog component** (carried over from the F0-01 baseline note). The DO network boundary is already covered by the synthesised `digitalocean_vpc`; the Project grouping is purely organisational and has no existing resource type to reuse.
- **In the estate:** not modelled (no component exists). The VPC provides the real network boundary; the Project is a follow-up.
- **Proposed target (F1):** a new `project` / resource-group component → `digitalocean_project` (+ `digitalocean_project_resources` associating the estate's resources). New resource type — F1.

### GAP-6 (data, not infra) — database DATA movement: backend-sealed

- **Component / prod resource:** the actual **DATA** in the two Postgres clusters (keycloak-db, pyx-main-db). The *clusters* migrate cleanly (GAP-free — `digitalocean_database_cluster`); the **data movement** (dump/restore, logical replication, cutover) does not.
- **Why it's out of scope of the provider:** data movement is a **backend-sealed operation** (a controlled runbook: snapshot → restore → verify → flip), never expressed as terraform in this provider. The provider provisions the target cluster; it does not move bytes.
- **Proposed target (F1-02):** the backend-sealed DB data-movement runbook (out of the provider's scope by design).

---

## Summary table

| Gap | Prod component | AWS today | DO status | Proposed F1 target |
| --- | --- | --- | --- | --- |
| GAP-1 | 3 frontends (marketing/console/vibe) | Amplify static hosting | no `static-site` component | **F1-01**: `static-site` → Spaces static + Cloudflare CDN |
| GAP-2 | transactional email | SES (`aws_ses_domain_identity`) | AWS-only, hard-errors on DO | **F1-05**: external provider (SendGrid/Postmark) or SES cross-cloud |
| GAP-3 | AWS secret rotation lambda | `aws_secretsmanager_secret_rotation` + bespoke Lambda | N/A (Vault-HA rotates natively) | out-of-band Lambda, or Vault-HA rotation on DO |
| GAP-4 | ALB host→distinct-service routing | multi-TG L7 rules | works via DOKS Ingress | **RESOLVED (F1-04)**: AWS LB renderer synthesises per-`TargetName` target groups; both providers validate GREEN |
| GAP-5 | DO Project envelope | (n/a) | no `project` component | new `project` component → `digitalocean_project` |
| GAP-6 | Postgres DATA (not clusters) | in-cluster data | clusters migrate; data does not | **F1-02**: backend-sealed data-movement runbook |
