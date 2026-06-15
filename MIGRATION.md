# PyxCloud Provider→Provider Migration Engine — Specification

> Task `pd-TF-PROVIDER-MIGRATION` (epic `EPIC-TF-IAC`). Gating design doc — like `SPEC.md` for the
> components, this is written and agreed **before** implementation. Triggered when the `compare`
> object switches the chosen provider for a macro logical place: the provider plans and executes
> the cutover + data move, **masking all of CRIU + rsync + DB/blob/secrets/queue migration**.

## 0. The non-negotiable: the migration know-how is an industrial secret

The *logic* of a successful migration — the exact sequencing of CRIU checkpointing, rsync delta
strategy, DB dump/restore + cutover ordering, blob/object sync, secret re-sealing, queue
drain/replay, DNS cutover, consistency verification, and rollback — is PyxCloud's moat. It MUST
NOT be readable:

- in the open/inspectable Terraform **provider binary**, nor
- on the **runner / CI host** that executes the migration.

Therefore the engine is split across a hard **trust boundary**.

## 1. Trust boundary

| Party | Trust | Sees the migration logic? |
|---|---|---|
| Terraform provider binary | UNtrusted (inspectable) | **No** — thin opaque client only |
| Runner / CI host | UNtrusted | **No** — launches the sealed runtime, sees only ciphertext + status |
| **PyxCloud backend** | Trusted (the moat) | **Yes** — owns the planner; never ships plaintext logic |
| **Confidential runtime** (attested enclave) | Trusted *only when attested* | Decrypts + runs the bundle **in sealed memory**; host cannot read it |

## 2. Components

### 2.1 Provider (open, opaque client)
On a place's provider switch, the provider:
1. Generates an **ephemeral keypair** (X25519, per migration run; private key lives only in
   process memory and is zeroized at end — forward secrecy, no replay).
2. `POST /api/migration/plan` → `{ sourceTopology+provider, targetProvider, place, ephemeralPubKey,
   attestationEvidence }`.
3. Receives a **sealed opaque execution bundle** (ciphertext; the provider can neither read nor
   reconstruct the steps).
4. Hands the bundle to the **confidential runtime**, polls status, surfaces progress/rollback to
   Terraform state. **The provider never decrypts the bundle or sees a single migration step.**

The provider exposes only: `migration { enabled, confidential_runtime = "local-tee" |
"confidential-container", attestation_endpoint, max_duration, dry_run }`. No migration steps,
scripts, or ordering live in the provider source.

### 2.2 Backend (the moat — secret planner)
Holds the migration planner. Given source/target it computes the full orchestration and emits it
as a **sealed execution bundle**:
- The bundle is an **opaque, compiled/serialized** step program (not human-readable HCL/scripts).
- It is **encrypted such that only an attested confidential runtime can decrypt it** — sealed to
  `KDF(ephemeralPubKey) ⊕ attestation-bound release key` (see §3).
- Short-lived, **scoped source+target cloud credentials** for the data move are delivered the same
  sealed way (never to the provider/runner in plaintext).
The planner logic stays entirely server-side; only ciphertext crosses the boundary.

### 2.3 Confidential runtime (executes opaquely; the part the runner "invokes but cannot see")
Pluggable substrate — exactly the two options in the directive:
- **`local-tee`** — SW confidential computing on the runner: the bundle runs inside a
  memory-encrypted sandbox (a WASM module in a sealed VM, or a hardware TEE — AMD SEV‑SNP /
  Intel TDX / SGX — when the host has one). Decryption happens only inside the enclave; the host
  OS / runner operator cannot read enclave memory.
- **`confidential-container`** (the "ZKC eseguito dal runner") — the runner launches a
  confidential container (AWS Nitro Enclave / GCP Confidential Space / Azure Confidential
  Containers) that **remote-attests to the backend**; the BE releases the bundle's decryption key
  **only** to an enclave whose attestation measurement matches the expected, signed value. Runner
  orchestrates the lifecycle but the logic runs sealed.

**Default: `auto` (DECIDED).** The runtime detects the runner's capability and picks the
strongest available — a remote-attested confidential container if launchable, else a hardware TEE
(SEV‑SNP/TDX/SGX) if present, else a local **memory-sealed WASM sandbox** as the portable
fallback. Both named substrates stay explicitly selectable; `auto` is the default so a migration
runs anywhere at the best opacity the host can offer (the floor is sealed-WASM, never plaintext).

## 3. Key handling & attestation (why the runner can't extract the secret)

1. Per-run **ephemeral X25519** keypair from the provider.
2. The confidential runtime boots and produces **attestation evidence** (enclave measurement +
   nonce), forwarded with the plan request.
3. The BE verifies the measurement against the **expected signed value** for the current migration
   runtime image. Only on a match does it seal the bundle's content-encryption key to a key that
   is **releasable only inside that attested enclave** (HPKE to `ephemeralPubKey`, with the CEK
   wrapped by an attestation-bound KMS release policy).
4. Decryption + execution happen **only inside the attested enclave**. Plaintext logic, the CEK,
   the ephemeral private key, and the scoped cloud creds are **zeroized at run end**.
5. No secret persists on disk or in the runner host's memory; a captured bundle is useless without
   a matching attested enclave (and the ephemeral key is gone).

## 4. The migration itself (run inside the sealed runtime)

Driven by the opaque bundle, inside the enclave, with verify-before-cutover + rollback:
- **Compute/state:** CRIU checkpoint of stateful processes → transfer → restore on target.
- **Filesystems/volumes:** rsync delta sync (seed + incremental) until convergence.
- **Managed DB:** snapshot/dump → restore on target → CDC/log-ship to converge → cutover. Reuses
  the **MDB data-safety guard** (no force-replace; snapshot-based; never in-place encryption flip).
- **Blob/object storage:** parallel sync + checksum verify; versioning preserved.
- **Secrets:** re-seal into the target provider's secret manager (decrypt only inside the enclave).
- **Queues/streams:** drain + replay (or mirror) with at-least-once + dedupe.
- **DNS/LB cutover:** flip after target health-verify; **rollback** = revert cutover + keep source
  until success is confirmed. Idempotent + resumable (checkpointed progress, opaque to the runner).

The runner sees: encrypted traffic, coarse phase/status, and a final success/rollback verdict —
never the method.

## 5. What the provider/runner observe vs. never observe

| Observable | Never observable |
|---|---|
| Migration is happening; coarse phase (e.g. "syncing", "cutover"), % progress | The step sequence / ordering |
| Success / rolled-back verdict | CRIU/rsync/DB/queue strategy + parameters |
| Encrypted byte counts | Source/target cloud credentials (sealed) |
| Attestation success/failure | The bundle plaintext / the planner logic |

## 6. Build plan (after this spec is agreed)

Parallelizable pieces (own-clone subagents, like wave-2):
1. **Provider opaque client** (`internal/migration/`): ephemeral keys (X25519/HPKE), the
   `migration{}` schema, plan-request handshake, bundle hand-off, status polling, rollback wiring
   into TF state. ZERO migration logic.
2. **Confidential-runtime launchers**: `local-tee` (sealed WASM/TEE) and `confidential-container`
   (Nitro/Confidential Space/Azure CC) — attestation evidence generation + sealed-bundle execution
   harness. Pluggable behind one interface.
3. **Backend boundary (BE repo, separate task):** `POST /api/migration/plan` + the sealed-bundle
   format + attestation verification + scoped-cred sealing. The planner *logic* is a separate,
   access-controlled backend module (the moat) — specified only by its I/O contract here.
4. **End-to-end opacity tests:** assert the provider/runner never hold plaintext logic or creds;
   assert key release fails on a bad/forged attestation; assert zeroization.

## 7. Decisions (resolved 2026-06-15)
- **Substrate = `auto`** (DECIDED): detect strongest available — confidential-container → hardware
  TEE → sealed-WASM fallback; both named substrates remain selectable. (§2.3)
- **Data path = runner-side enclave** (DECIDED): data transits the runner's sealed enclave
  (encrypted), source→target. Max opacity + customer-side data sovereignty; runner pays egress. NO
  PyxCloud-hosted broker. (per the directive "far eseguire dal runner")
- **Still to settle during build (non-blocking):** which confidential-container backend to wire
  first (Nitro vs GCP Confidential Space vs Azure CC), and the **attestation root** (PyxCloud-
  operated verifier vs cloud-native attestation service). Build the abstraction so these are
  swappable; wire one real backend + stub the others with explicit TODOs.
