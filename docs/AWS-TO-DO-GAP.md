# AWS → DigitalOcean Migration — Authoritative Gap Analysis

> Task `pd-MIG-DO-GAP-ANALYSIS` (epic `EPIC-AWS-TO-DO-MIGRATION`), repo
> `terraform-provider-pyxcloud`. **PLAN-ONLY** status document — no applies.
>
> This is the single source of truth for "what is the AWS→DO migration status of the
> PyxCloud provider". It enumerates every AWS-managed service the PyxCloud platform uses,
> maps each to its DigitalOcean-native resource or its CNCF/de-facto-OSS replacement,
> records the implementation status against the live catalog code, lists the remaining
> gaps with their recommended fallback, and gives a readiness verdict for a full DO cutover.
>
> Cross-checked against `internal/catalog/*` at the HEAD of this branch (commits up to
> `#75` — workload-identity + Vault-HA). Every "DONE" row names the catalog file that
> implements it; this is ground truth, not aspiration.

## 0. How to read this

The provider is **abstract-first**: the user declares a canonical component (e.g.
`virtual-machine`, `object-storage`, `tracing`) and the provider *descends* it to the
chosen cloud (`internal/catalog/assemble.go`). For each canonical type there are three
possible DO outcomes:

| Outcome | Meaning |
|---|---|
| **DO-native** | DigitalOcean has a managed primitive; the catalog renders it directly (`digitalocean_*`). |
| **Operator-pattern** | No DO-managed equivalent; replaced by a CNCF/OSS project run **on DOKS** as a Kubernetes **operator** = upstream Helm chart (CORE) + our custom CRs (EXTRA). Per the owner directive and `SPEC.md §4.1`. |
| **Mitigation (single-VM)** | Last-resort fallback in `mitigation.go`: self-host the OSS image on one droplet via cloud-init. Used when a type is `Mitigatable` **and** `!NativelySupported(type, "digitalocean")`. Some are marked `degraded`. |
| **Unsupported / AWS-only** | The component has no DO path at all; a place targeting DO with this component fails at plan time (clean error) or the component is AWS-specific by nature. |

Routing is decided in `assemble.go`:
`if Mitigatable(c.Type) && !NativelySupported(c.Type, provider) → mitigateComponent(...)`.
Operator-pattern components (`monitoring`, `tracing`, `vault-ha`, `tls-certificate`,
`workload-identity`) are **explicit canonical component types** the user declares — they
are the *managed, supersede-the-mitigation* answer for their AWS service, and are listed
in `nativeSupport` (or are non-mitigatable) so the single-VM fallback is **not** taken.

---

## 1. The gap matrix (every AWS service the platform uses)

Legend: ✅ DONE · ⚙️ operator-pattern (DONE) · 🩹 mitigation-only (single-VM, works but unmanaged) · ⛔ unsupported/AWS-only.

### 1.1 Compute / network / storage — DO-native, full parity

| AWS service | Canonical type | DO target (resource) | Status | Implemented in |
|---|---|---|---|---|
| VPC | `network` / `vpc` | `digitalocean_vpc` (region-scoped) | ✅ | `network.go` |
| Security Group | `security-group` | `digitalocean_firewall` (≤50 rules/dir; no `all` proto) | ✅ | `securitygroup.go` |
| EC2 instance | `virtual-machine` | `digitalocean_droplet` | ✅ | `virtualmachine.go` |
| EBS volume | `block-storage` | `digitalocean_volume` + `digitalocean_volume_attachment` | ✅ | `blockstorage.go` |
| EIP | `reserved-ip` | `digitalocean_reserved_ip` | ✅ | `reservedip.go` |
| Auto Scaling Group | `virtual-machine-scale-group` | `digitalocean_kubernetes_cluster` w/ autoscaling `node_pool` (DO has no VM-ASG; `min_nodes≥1` keeps the self-healing ASG-of-1) | ✅ | `scalegroup.go` |
| ALB / NLB | `load-balancer` | `digitalocean_loadbalancer`; **L7** host/path rules descend to DOKS ingress | ✅ | `loadbalancer.go` |
| RDS | `managed-database` | `digitalocean_database_cluster` (+ data-safety replacement guard) | ✅ | `manageddatabase.go` |
| ElastiCache (Redis) | `cache` | `digitalocean_database_cluster` (engine=redis) | ✅ | `cache.go` |
| S3 | `object-storage` / `blob-storage` | `digitalocean_spaces_bucket` (versioning, AES256 SSE, lifecycle, policy, logs) | ✅ | `objectstorage.go` |
| ECR | `container-registry` | `digitalocean_container_registry` | ✅ | `containerregistry.go` |
| AMI | `image` | DO image slug / snapshot | ✅ | `image.go` |
| EKS | `managed-kubernetes` | `digitalocean_kubernetes_cluster` (DOKS) | ✅ | `kubernetes.go` |
| Route 53 (DNS) | `dns-zone` | `digitalocean_domain` (and provider-independent `cloudflare_dns_record`) | ✅ | `edge.go`, `cloudflare.go` |
| CloudWatch Synthetics | `synthetics` / `uptime-check` | `digitalocean_uptime_check` | ✅ | `synthetics.go` |
| Lambda | `serverless-function` | `digitalocean_app` (functions component) | ✅ | `serverless.go` |
| EventBridge cron | `scheduled-trigger` / `cron-job` | DOKS `kubernetes_cron_job_v1` | ✅ | `scheduledtrigger.go`, `render_scheduledtrigger.go` |
| DynamoDB | `key-value-store` / `kv-store` | `digitalocean_database_cluster` (engine=redis, as a KV store) | ✅ | `keyvaluestore.go`, `render_keyvaluestore.go` |

### 1.2 Managed services with no DO equivalent — replaced via the operator pattern (DONE)

These are the AWS-managed observability/secrets/identity services. DO has nothing
equivalent, so they are run **on DOKS** following `SPEC.md §4.1`: CORE = the upstream
operator's Helm chart (`helm_release`), EXTRA = our custom resources (`kubernetes_manifest`
of the operator CRDs, `depends_on` the CORE). The shared helper is `operator.go`
(`renderHelmRelease` / `renderOperatorCR` / `renderOperatorComponent`).

| AWS service | Canonical type | OSS replacement (CORE Helm chart → EXTRA CRs) | Status | Implemented in |
|---|---|---|---|---|
| X-Ray (tracing) | `tracing` | `opentelemetry-operator` + `tempo-operator` → `OpenTelemetryCollector` + `TempoStack` | ⚙️ ✅ | `tracing.go`, `render_tracing.go` |
| ACM (TLS certs) | `tls-certificate` | `cert-manager` (Helm, `installCRDs=true`) → `ClusterIssuer` + `Certificate` (Let's Encrypt) | ⚙️ ✅ | `tlscertificate.go`, `render_tlscertificate.go` |
| CloudWatch metrics+logs + SNS alarms | `monitoring` | `kube-prometheus-stack` (Prometheus Operator + Grafana + Alertmanager) + `grafana/loki` → `ServiceMonitor`/`PodMonitor`, `PrometheusRule` (CW alarms → Prometheus alerts via Alertmanager, replacing SNS), Grafana `Loki`/`Tempo` datasources | ⚙️ ✅ | `cloudwatch.go`, `render_monitoring_lgtm.go` |
| Secrets Manager + KMS (managed answer) | `vault-ha` | `hashicorp/vault` Helm in **HA Raft** mode + **Transit auto-unseal** (also installs the Vault Secrets Operator) → `VaultConnection` + `VaultAuthGlobal` per auth method. AWS seal peer = `aws_kms_key` + `aws_secretsmanager_secret`. | ⚙️ ✅ | `vaultha.go`, `render_vaultha.go` |
| IAM role / instance profile | `workload-identity` | Vault identity on DO (DO has no IAM-role primitive). EXTRA only (Vault CORE owned by `vault-ha`): `VaultPolicy` per inline policy + `VaultAuth` role; delivered via **approle** (droplet cloud-init, response-wrapped SecretID, no static keys) or **kubernetes** (DOKS SA bound to a Vault k8s-auth role). Short-lived tokens. | ⚙️ ✅ | `workloadidentity.go`, `render_workloadidentity.go` |

A component flips the provider pins automatically: any `helm_release` sets `needsHelm`
(→ `hashicorp/helm`) and any `kubernetes_manifest` sets `needsKubernetes`
(→ `hashicorp/kubernetes`) in `assemble.go`; a component advertises Helm via `RendersHelm`
on its plan. `securitybaseline.go` derives least-privilege egress + production-safe
secrets recovery defaults across the assembled topology.

### 1.3 Services with **no managed DO path** — single-VM mitigation only

`mitigation.go` self-hosts the canonical OSS substitute on **one droplet** (Docker via
cloud-init). This is a **working** but **unmanaged** fallback (no HA, no operator
lifecycle, single point of failure). DO is absent from `nativeSupport` for these types,
so this path is taken automatically. Targets (from `selfHostRecipes`):

| AWS service | Canonical type | DO mitigation (single-VM image) | Status |
|---|---|---|---|
| SQS | `managed-queue` / `message-queue` | `rabbitmq:3` | 🩹 mitigation-only |
| Kinesis | `event-streaming` / `event-bus` | `redpanda` (Kafka-compatible) | 🩹 mitigation-only |
| WAFv2 | `waf-service` / `waf` | `owasp/modsecurity-crs:nginx` (**degraded**) | 🩹 mitigation-only |
| Secrets Manager (raw component) | `secrets-manager` | `hashicorp/vault:latest` (single droplet) | 🩹 mitigation-only — *superseded by `vault-ha` ⚙️ when the user declares the operator component* |
| KMS / encryption-key (raw component) | `kms` / `encryption-key` | `hashicorp/vault:latest` Transit (single droplet) | 🩹 mitigation-only — *superseded by `vault-ha` ⚙️ Transit* |
| Lambda (if `serverless-function` not on DO Apps) | `serverless-function` | `nuclio` FaaS (**degraded**) — note DO Apps Functions is the native path | 🩹 fallback |
| (generic k8s) | `managed-kubernetes` / `container-service` | `k3s` (**degraded**) — DOKS is the native path | 🩹 fallback |
| (generic LB) | `load-balancer` | `haproxy:2.9` (**degraded**) — `digitalocean_loadbalancer` is the native path | 🩹 fallback |
| CloudFront (origin not Spaces) | `cdn-service` / `cdn` | `varnish:7` (**degraded**) — see CDN note below | 🩹 fallback |

### 1.4 Unsupported / AWS-only on DO

| AWS service | Canonical type | DO status | Implemented in |
|---|---|---|---|
| Managed Prefix List | `prefix-list` | ⛔ no DO primitive; CIDRs are **inlined** into `digitalocean_firewall` rules instead (functionally covered) | `prefixlist.go`, `securitygroup.go` |
| SES (email) | `email-service` / `email` | ⛔ AWS-only in `nativeSupport`; mitigation recipe exists (SMTP relay) but SES itself is not reproduced; **passo.build uses SES directly** | `ses.go` |
| IAM (raw policies/roles) | `iam` | ⛔ AWS-only; the DO answer is `workload-identity` (Vault), not a 1:1 IAM port | `iam.go` |
| KMS (raw, hard mode) | `kms` | ⛔ hard plan-time error on DO in `kms.go` (managed crypto); the DO answer is Vault Transit via `vault-ha`/mitigation | `kms.go`, `secrets.go` |
| WAFv2 (managed, edge) | `waf-service` | ⛔ no DO-native WAF; only the degraded ModSecurity mitigation (or front with Cloudflare WAF) | `edge.go`, `mitigation.go` |
| CloudFront (full CDN) | `cdn-service` | ⚠️ partial — `digitalocean_cdn` only fronts a **Spaces origin**; arbitrary-origin CDN is not covered (Varnish mitigation or Cloudflare) | `edge.go` |

---

## 2. Remaining gaps & recommended fallback

Ordered by cutover impact. "Recommended CNCF/de-facto fallback" follows the **operator
pattern** wherever the substitute ships a k8s operator (per the owner directive); only
truly stateless/simple cases stay on a managed/edge service.

1. **Message queue (SQS) — gap: HA.** Today: single-VM RabbitMQ. *Recommended:* RabbitMQ
   **Cluster Operator** (`rabbitmq-cluster-operator` Helm → `RabbitmqCluster` CR) on DOKS,
   replacing the single droplet. New canonical type `managed-queue` operator component.

2. **Event streaming (Kinesis) — gap: HA.** Today: single-VM Redpanda. *Recommended:*
   **Strimzi** (Kafka operator, CNCF) → `Kafka` CR, **or** the **Redpanda Operator** →
   `Redpanda` CR, on DOKS. Operator-pattern component.

3. **WAF (WAFv2) — gap: real WAF + edge.** Today: degraded single-VM ModSecurity.
   *Recommended (preferred):* front the load-balancer/ingress with **Cloudflare WAF**
   (already a first-class provider here — `cloudflare.go`); the platform already terminates
   at Cloudflare. *Alternative (in-cluster):* ModSecurity/Coraza as an ingress-nginx
   plugin on DOKS. Decision: prefer Cloudflare edge WAF, drop the droplet path.

4. **CDN (CloudFront, arbitrary origin) — gap: non-Spaces origins.** `digitalocean_cdn`
   covers Spaces only. *Recommended:* **Cloudflare CDN** for arbitrary origins
   (already wired), keep `digitalocean_cdn` for Spaces assets.

5. **Email (SES) — gap: no DO equivalent.** *Recommended:* keep using **AWS SES**
   cross-cloud (SES is a global API, not region/cloud-coupled — passo.build already does),
   or swap to a SaaS sender (Postmark/Resend/SendGrid) behind the `email-service` type.
   This is the one service where staying on AWS is the right answer.

6. **Raw `secrets-manager` / `kms` / `encryption-key` components — gap: mitigation still
   single-VM.** These work but should **route to the `vault-ha` operator component**
   automatically when present, so a user who declares a bare `secrets-manager` on DO gets
   the HA Vault, not a lone droplet. *Recommended:* in `assemble.go`, alias the raw
   secrets/kms types onto the `vault-ha` rendering on DO (the operator CORE already exists).

7. **DynamoDB / KV (DynamoDB streams, single-digit-ms at scale) — gap: semantic fidelity.**
   Mapped to Redis today, which covers KV but not DynamoDB streams / global tables.
   *Recommended:* document the semantic limits; for stream semantics, pair with the
   event-streaming operator. Acceptable for the platform's current KV usage.

8. **Managed Prefix List — gap: none functionally.** Inlining CIDRs into firewall rules
   covers the use case; the only risk is the DO 50-rule/direction cap with large CIDR sets.
   *Recommended:* keep inlining; add a plan-time warning when inlined CIDRs approach the cap.

---

## 3. Readiness verdict — full DO cutover

**Verdict: the platform's *core* topology can cut over to DigitalOcean today
(plan-only-validated); the *managed-service tail* is the residual risk.**

What is **ready** (no blocker):
- All compute/network/storage/DB/cache/registry/k8s/LB/DNS/serverless/cron/KV primitives
  are DO-native with parity (§1.1).
- All observability + secrets + identity managed services are replaced by **HA operator-
  pattern** components on DOKS (§1.2): tracing, TLS, monitoring (LGTM), Vault-HA,
  workload-identity. These are the heavy lifts and they are **done**.
- A full-estate **plan-only AWS→DO dry-run** already exists (`full_estate.go`,
  `pd-MIG-PLAN-DRYRUN-ESTATE`, commit `#72`).

What is **blocking a *clean* (no-degraded, no-stay-on-AWS) full cutover:**
- **B1 — Queue/stream are single-VM (not HA).** SQS→RabbitMQ and Kinesis→Redpanda run on
  one droplet. For any prod workload using queues/streams this is a durability/availability
  gap. *Unblock:* the RabbitMQ-Cluster-Operator and Strimzi/Redpanda-Operator components
  (gaps 1–2). **This is the top blocker.**
- **B2 — WAF has no managed DO answer.** Must be resolved by routing through **Cloudflare
  WAF** (preferred) rather than the degraded droplet (gap 3). Policy decision needed.
- **B3 — SES has no DO equivalent.** Email stays on AWS SES (or a SaaS). This is a
  *deliberate* cross-cloud dependency, not a defect — but it means "100% off AWS" is not the
  goal for email. Document as accepted.
- **B4 — Raw secrets/kms still fall to a single-VM** unless the user explicitly declares
  `vault-ha`. *Unblock:* auto-route the raw types onto the Vault-HA operator on DO (gap 6) —
  small change, removes a footgun.
- **B5 — CDN for non-Spaces origins** needs Cloudflare (gap 4). Minor; most assets are Spaces.

**Bottom line:** a DO cutover of the PyxCloud control plane + stateful core is
**plan-ready now**. The remaining work to reach a *no-degraded* cutover is bounded and
known: ship the **queue + stream operators (B1)**, **commit to Cloudflare for WAF+CDN
(B2/B5)**, **auto-route raw secrets/kms to Vault-HA (B4)**, and **accept SES as a
cross-cloud dependency (B3)**. None of these is a re-architecture; all four follow the
established operator-pattern / edge-provider conventions already in the codebase.

### 3.1 Implementation task list (recommended next epic items)

| ID (suggested) | Work | Pattern | Priority |
|---|---|---|---|
| `pd-MIG-QUEUE-OPERATOR` | RabbitMQ Cluster Operator component (HA queue) | operator | P0 (B1) |
| `pd-MIG-STREAM-OPERATOR` | Strimzi/Redpanda operator component (HA stream) | operator | P0 (B1) |
| `pd-MIG-WAF-CLOUDFLARE` | Route `waf-service` through Cloudflare WAF; retire droplet ModSecurity | edge | P1 (B2) |
| `pd-MIG-SECRETS-ROUTE-VAULTHA` | Auto-alias raw `secrets-manager`/`kms`/`encryption-key` → `vault-ha` on DO | routing | P1 (B4) |
| `pd-MIG-CDN-CLOUDFLARE` | Cloudflare CDN for arbitrary origins; keep `digitalocean_cdn` for Spaces | edge | P2 (B5) |
| `pd-MIG-EMAIL-DECISION` | Decide SES-stay vs SaaS sender for `email-service`; document | policy | P2 (B3) |
| `pd-MIG-PREFIXLIST-CAP-WARN` | Plan-time warning when inlined CIDRs approach DO 50-rule cap | UX | P3 |

---

## 4. Evidence

- Catalog ground truth read at HEAD of `version/0.1.x/docs/sp-do-gap-analysis`
  (`origin/main` @ `#75`): every ✅/⚙️ row names the implementing file in `internal/catalog/`.
- Routing logic verified in `internal/catalog/assemble.go`
  (`Mitigatable(c.Type) && !NativelySupported(...)` → `mitigateComponent`) and
  `internal/catalog/mitigation.go` (`nativeSupport` map + `selfHostRecipes`).
- Operator-pattern convention per `SPEC.md §4.1` and the global design directive
  *"AWS-managed-service replacements must follow the k8s operator pattern: upstream core +
  agent extra"* (2026-06-23).
- `go build ./...` green at the time of writing (no code change in this PR — docs only).
- Migration history: commits `#66`–`#75` (registry/reserved-ip/image, firewall/objstore/
  cert-manager, scale-group→DOKS, L7+tracing, full-estate dry-run, operator convention,
  LGTM monitoring, workload-identity + Vault-HA).
