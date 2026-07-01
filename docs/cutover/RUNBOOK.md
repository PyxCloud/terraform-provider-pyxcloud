# AWS → DigitalOcean cutover — operator RUNBOOK

`pd-MIG-CUTOVER-F0-04` (EPIC-AWS-TO-DO-MIGRATION)

Operator runbook for cutting the passo.build production estate from AWS to
DigitalOcean. This is a **checklist**, not prose — run it top-to-bottom. The
abstract estate is `internal/catalog/prod_estate.go`; the DO target foundation
is `internal/catalog/do_baseline.go`; the residual bespoke gaps are
`docs/cutover/BESPOKE-GAPS.md`. Read all three before executing F4.

The provider provisions the DO target (plannable today for everything except the
F1 gaps); it does **not** move bytes. DB **data movement is backend-sealed**
(GAP-6). This runbook wraps the sealed operation with the explicit operator
gates the provider cannot express (row-count + checksum verification), because
**the provider's data-safety guard is plan-time only**.

---

## 0. Targets (RTO / RPO / downtime budget)

| Target | Value | Notes |
| --- | --- | --- |
| **RTO** (max total downtime, cutover window) | **≤ 60 min** | Wall-clock from AWS write-freeze to DO serving 100% + green smoke. |
| **RPO — the 2 Postgres** (keycloak-db, pyx-main-db) | **0** (zero data loss) | Achieved by write-freeze on AWS **before** the final sync, so no writes exist that are not replicated. Cutover is not started until logical replication lag = 0. |
| **RPO — object storage (Spaces)** | **≤ 5 min** | Buckets are content-addressed / append-mostly; final `rclone sync` pass runs inside the freeze. |
| **Write-cutover window (F4)** — hard budget | **30 min** | Sub-budget of RTO. If not GREEN at **T+30 min** → **abort + roll back** (§5). |
| Read-only degradation tolerated during window | yes | AWS may serve read-only behind the freeze; users see maintenance banner. |

Freeze-window sub-budget (must fit inside the 30 min):

| Step | Budget |
| --- | --- |
| Freeze AWS writes + drain in-flight | 3 min |
| Final logical-replication sync to lag=0 | 8 min |
| Data-safety gate (row-count + checksum, both DBs) | 6 min |
| Promote DO Managed PG + repoint services | 5 min |
| DNS canary → 100% + E2E smoke | 8 min |

---

## 1. Phase map (F0 → F5) with go/no-go gates

Each phase ends with a **GATE**. Do not start the next phase until the gate is
GREEN. F4 and the F5 decommission are **approval-gated** (see §7).

| Phase | Name | Scope | GO/NO-GO gate |
| --- | --- | --- | --- |
| **F0** | Foundations | canonical estate (`prod_estate.go`), DO baseline (`do_baseline.go`), gap enumeration (`BESPOKE-GAPS.md`), this runbook. | `TestProdEstateTerraformValidate` GREEN for **both** providers (init + validate, plan-only). This runbook reviewed. |
| **F1** | Gap-fill | close the bespoke gaps that block the DO target: **GAP-1** static-site, **GAP-2** email, **GAP-6** DB data-movement proof, plus GAP-3/4/5. | GAP-1 (`static-site`) + GAP-2 (email path) render on DO; **GAP-6: F1-02 backend-sealed data-movement proven end-to-end in a rehearsal** (dump→restore→verify on a throwaway DO PG). No gate-passing without F1-02. |
| **F2** | Stateless blue/green | provision full DO estate (6 DOKS clusters, edge-lb, Spaces, registry, LGTM, Vault-HA, RabbitMQ, cert-manager) alongside live AWS. **No user traffic.** | DO estate `terraform apply` clean; all 6 services healthy on DOKS (`/q/health`); Keycloak up on DO with realms provisioned; internal smoke GREEN via VPN. |
| **F3** | Data migration (rehearsal + live-replicate) | stand up **logical replication** AWS PG → DO Managed PG for both DBs; let it catch up and run continuously. Rehearse a full dry-run cutover on a clone. | Replication **lag steady near 0** for ≥ 24 h; dry-run cutover on clone passed data-safety gate; rollback rehearsed once. |
| **F4** | **Cutover** (the write-cutover window) | the irreversible flip — freeze → final sync → verify → promote → repoint → DNS → smoke. See §3. **Approval-gated.** | E2E smoke GREEN on DO at 100% traffic within the 30-min budget; error rate ≤ baseline; canary Keycloak login succeeded. |
| **F5** | Decommission | keep AWS warm for the soak, then tear it down. | Soak GREEN for **7 days** (§6). **Approval-gated (F5-02)** before any AWS destroy. |

---

## 2. Dependencies / prerequisites (F1 gaps that block cutover)

Do **not** enter F4 until these are cleared:

- [ ] **GAP-1 (F1-01) — FE static hosting.** The 3 frontends (marketing / console / vibe) have **no DO static-site component**. Their built bundles already migrate as Spaces buckets (`app-assets`, `pyx-frontend`, `vibe-assets`), but the **managed hosting + CDN wrapper** (Amplify replacement = Spaces static + Cloudflare CDN) must exist and serve before flip, or the SPAs 404 on DO.
- [ ] **GAP-2 (F1-05) — transactional email (SES).** `email.go` is AWS-only (hard-errors on DO). passo.build SES sending must either be re-homed to an external provider (SendGrid/Postmark) **or** an explicit decision made to keep SES as a **cross-cloud dependency** from the DO estate. Confirm which, and that password-reset / verification email works from DO **before** flip.
- [ ] **GAP-6 (F1-02) — DB data movement is backend-sealed.** Data movement is **not** terraform. F1-02 must have **proven** the sealed dump→restore→verify→flip runbook end-to-end in a rehearsal (F3). This runbook's §3/§4 assume F1-02 exists and is trusted. **No F4 without a passed F1-02 rehearsal.**
- [ ] GAP-3 (secret rotation): DO uses **Vault-HA** native rotation (no Lambda) — confirm Vault-HA rotating on DO. GAP-4 (host routing): DOKS Ingress backends distinct hosts natively — confirm `admin`/`app`/`mcp`.passo.build route to sso/backend/mcp on DO. GAP-5 (DO Project): organisational only, non-blocking.

---

## 3. F4 cutover sequence (the write-cutover window) — step by step

Pre-window (T-24h → T-0, **no downtime**):
- [ ] Replication healthy, lag ≈ 0 for both DBs (from F3).
- [ ] DO estate fully healthy (F2 gate still GREEN).
- [ ] Cloudflare: DO edge-lb origin staged, **grey-clouded / 0% weighted** (ready, not live).
- [ ] Maintenance window announced (§7); status page armed.
- [ ] Rollback owner + approver on the bridge; AWS confirmed **warm** (do not scale down).
- [ ] **F4-01 approval obtained** to begin the freeze (§7).

Window (T-0, clock starts — 30-min budget):

1. **Freeze AWS writes** — put pyx-backend + Keycloak into **read-only / maintenance** mode on AWS (app-level write-freeze + revoke write on AWS PG roles). Drain in-flight requests. Post maintenance banner. *This is the RPO=0 guarantee: no write may exist after this point that is not replicated.*
2. **Final logical-replication sync** — let replication drain to **lag = 0** on both keycloak-db and pyx-main-db. Also run the final `rclone sync` pass S3 → Spaces for the mutable buckets. Wait for confirmed **lag = 0**.
3. **Data-safety gate — VERIFY (mandatory, §4)** — run row-count + checksum verification on **both** DBs, AWS vs DO. **Must be identical.** This is the last reversible checkpoint. If it fails → **abort, un-freeze AWS** (§5), no harm done.
4. **Promote DO Managed PG** — stop replication; promote DO keycloak-db + pyx-main-db to standalone primaries (accept writes). *(Provider provisioned the clusters; promotion is the backend-sealed operation.)*
5. **Repoint services** — point the 6 DOKS services at the DO PG endpoints (already wired in `do_baseline.go`); ensure Keycloak on DO points at DO keycloak-db; roll the deployments; confirm `/q/health` GREEN on all 6.
6. **Cloudflare DNS staged flip** — orange-cloud + weight the DO edge-lb origin: **canary 5% → 25% → 100%**, pausing ~60–90 s at each step to watch error rate / latency. Hosts: `admin.passo.build`→sso, `app.passo.build`→backend, `mcp.passo.build`→mcp (GAP-4 parity via DOKS Ingress).
7. **E2E smoke on DO** — at 100%: Keycloak **canary login** (see §5 auth), backend health + a real API round-trip, MCP handshake, each SPA loads (GAP-1), a password-reset email sends (GAP-2), an object read/write to Spaces. **All GREEN → cutover accepted.**

**Point of no return:** reached at the **end of step 4 once step 5 has resumed writes on the DO databases** — i.e. DO PG is promoted to primary **and** services have written to it. Before that (through step 3, and even step 4 if no DO write has landed) rollback is clean: un-freeze AWS, revert DNS. **After** DO has accepted production writes, rolling back to AWS means **losing the writes taken on DO** (AWS is stale) — no longer a clean revert; it becomes a forward-fix or a reverse-migration. Treat steps 5–7 as committed.

---

## 4. Data-safety gates (mandatory before the irreversible DB write-cutover)

The provider's data-safety guard is **plan-time only** (it blocks a destructive
plan; it cannot verify bytes). The operator MUST add these runtime checks. Run
in F4 step 3, **inside the freeze, before promotion (step 4)** — this is the last
reversible point.

For **each** of keycloak-db and pyx-main-db:

- [ ] **Replication lag = 0** confirmed (no un-applied WAL / subscription backlog).
- [ ] **Row-count parity** — per table, `COUNT(*)` on AWS == DO. Zero-tolerance mismatch on any table. Focus critical tables: Keycloak `user_entity`, `credential`, `user_session`, `realm`; pyx-main app tables.
- [ ] **Checksum parity** — per critical table, an ordered content checksum (e.g. `md5(string_agg(t::text, ',' ORDER BY pk))` or `pg_dump --data-only | sha256`) AWS == DO.
- [ ] **Sequence high-water marks** match (avoid PK collisions post-promote).
- [ ] **Extensions / roles / grants** present on DO (schema drift check).

**GATE:** any mismatch → **do not promote**. Abort and un-freeze AWS (§5). All
checks GREEN → proceed to promote (step 4).

---

## 5. Rollback — triggers & procedure (per phase)

General principle: **AWS is kept warm** through F2–F4 and the F5 soak. Rollback =
revert DNS + point services back to AWS + un-freeze AWS. This is clean **only
before** the point-of-no-return (§3).

| Phase | Abort signal | Rollback procedure |
| --- | --- | --- |
| F1 | gap not closeable / F1-02 rehearsal fails | stay on AWS; re-scope gap; no prod impact. |
| F2 | DO estate won't apply / service unhealthy | `terraform destroy` the DO blue estate; AWS untouched. |
| F3 | replication lag won't converge / dry-run data-safety fails | tear down subscription; fix; re-rehearse. AWS untouched. |
| **F4 (steps 1–3, pre-promote)** | data-safety gate fails; lag won't reach 0; freeze exceeds budget; any red before promote | **un-freeze AWS** (restore write access on AWS PG + app), drop the banner, leave DNS on AWS (never flipped). **Zero data loss** — AWS took all writes. |
| **F4 (steps 6–7, DNS live but PRE point-of-no-return)** | canary error-rate spike / auth failures / smoke red, **and DO has not yet accepted writes** | revert Cloudflare weights to **AWS 100%**; repoint services to AWS; un-freeze AWS. Clean revert. |
| **F4 (post point-of-no-return: DO promoted + writing)** | severe failure after DO accepted prod writes | **NOT a clean revert.** Options: (a) forward-fix on DO (preferred — data lives on DO now); (b) reverse-migrate DO→AWS (re-replicate the delta back), which reintroduces downtime. Escalate to approver before any AWS fail-back — AWS is stale and would lose DO writes. |
| F5 | soak regression within 7 days | AWS still warm → same F4-DNS rollback path **only if** DO writes can be reconciled back; otherwise forward-fix. Do not destroy AWS until F5-02 approved. |

**Auth-specific fail-back note:** if rollback happens after any Keycloak session
was minted on DO, those sessions won't validate against AWS Keycloak. Accept a
forced re-login on fail-back (users re-authenticate against AWS). This is a
degraded but safe state — **lockout is the worst case, avoid it** (§6).

---

## 6. Auth-specific care (Keycloak realm continuity)

Keycloak lockout is the worst-case failure — treat SSO with extra care.

- [ ] **Realm continuity** — DO Keycloak has the same realms (`passobuild`, etc.), clients, identity brokers, and signing keys as AWS **before** flip. Verify the realm export/import as part of the keycloak-db data migration (the users/sessions live in keycloak-db; realm config must match).
- [ ] **Canary login on DO before the flip** — during F4 step 7 (and rehearsed in F3): perform a **real login against DO Keycloak** (via `admin.passo.build` canary weight or a direct/`Host`-header test) **before** ramping DNS to 100%. A failed canary login **aborts the ramp** (still pre-point-of-no-return at step 6).
- [ ] **Signing-key parity** — token signing keys must be identical AWS↔DO so tokens minted either side validate during the ramp (prevents mid-flip auth churn).
- [ ] **Session tolerance** — expect active sessions to survive if keycloak-db (which holds `user_session`) migrated with row+checksum parity (§4). If not, communicate a forced re-login in the maintenance notice.
- [ ] **Fail-back auth** — see §5 note: post-DO-write rollback forces re-login against AWS. Acceptable; **never leave users locked out** — if in doubt, hold on DO and forward-fix.

---

## 7. Comms / ownership & approvals

**Approval-gated steps** (require the named approver's sign-off before executing):

| Gate | Step | Approver |
| --- | --- | --- |
| **F4-01** | Begin the cutover window (freeze AWS writes) | Migration owner + on-call SRE lead |
| **F4-02** | **Promote DO Managed PG** (step 4 — the point-of-no-return step) | Migration owner + DBA (data-safety gate §4 must be GREEN) |
| **F4-03** | Ramp DNS canary → 100% (step 6) | Migration owner (after canary login §6 GREEN) |
| **F5-02** | Destroy AWS estate (decommission) | Migration owner + platform lead — only after 7-day soak GREEN |

Comms:
- [ ] **Maintenance-window notice** ≥ 48 h ahead: status page + email + in-app banner. State the read-only window (≤ 60 min RTO), possible forced re-login, and the rollback stance.
- [ ] **T-0 / T-100% / T-accepted** posts to status page and the incident bridge.
- [ ] Bridge staffed for the whole F4 window: migration owner, SRE on-call, DBA, auth owner, approver.
- [ ] Post-cutover all-clear once §3 step 7 smoke is GREEN; keep the bridge on standby through the first hours of soak.

---

## 8. Post-cutover soak & decommission (F5)

- [ ] Soak on DO for **7 days**, AWS kept warm (do not destroy).
- [ ] Watch error rate, latency, auth success, DB health, Spaces I/O against the pre-cutover baseline.
- [ ] Confirm SES/email (GAP-2) and the 3 SPAs (GAP-1) steady in production.
- [ ] After a GREEN soak and **F5-02 approval** → `terraform destroy` the AWS source estate. Keep final AWS PG snapshots + a Spaces/S3 backup archive retained per policy before destroy.
