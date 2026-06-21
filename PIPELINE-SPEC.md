# PyxCloud DevOps Pipeline IR — Specification

> Task `pd-DEVOPS-PIPELINE-IR-SPEC` (epic `EPIC-DEVOPS-ABSTRACTION`). Gating document, same
> discipline as [`SPEC.md`](SPEC.md): define the abstraction extensively first, then implement
> one backend at a time with round-trip tests — `pd-DEVOPS-COMPILE-GHA` (GitHub Actions backend)
> → `pd-DEVOPS-LAMBDA-RUNNER` (super-custom AWS control plane) → `pd-DEVOPS-BACKEND-PARITY`
> (same IR, both backends) → `pd-DEVOPS-MIGRATE-ALL`.

## 0. Why this exists

The TF provider ([`SPEC.md`](SPEC.md)) parifies **infrastructure** across clouds: declare in a
canonical vocabulary, descend to a concrete provider. This document does the same for the **DevOps
pipeline**: declare a pipeline once in a canonical IR, descend it to a concrete **execution
backend** (GitHub Actions today, a super-custom AWS Lambda control plane next), so a pipeline is
**provider-parified** the way infrastructure already is.

Terraform itself is the wrong layer to *express* pipeline execution — it reconciles desired infra
state, it does not orchestrate ordered, event-driven jobs with gates. So the pipeline gets its own
canonical IR (an extensible DevOps language). Terraform — via the pyxcloud provider — still owns the
job it is good at: **provisioning the backend's infrastructure** (Lambda, Step Functions, Fargate,
CodeBuild, IAM/OIDC, EventBridge, S3 artifact store). Two layers, one for each concern.

## 1. Principles

1. **Abstract-first (same inversion as the TF provider).** The author declares a pipeline in
   PyxCloud's canonical vocabulary — `job`, `step`, `capability`, `gate` — never in a backend's
   native syntax. A **backend adapter** descends the canonical model to native (a GHA workflow
   YAML; a Step Functions state machine + executor invocations). We never start from
   `actions/checkout@v4`; we start from the `checkout` **capability** and resolve it down.
2. **Capabilities, not vendor actions.** The unit of reuse is a small, closed set of canonical
   **capabilities** (`checkout`, `setup-tool`, `cache`, `artifact-upload`/`-download`,
   `cloud-auth`, `run`). Each backend implements every capability natively. Free-form vendor
   actions (`uses:`) are deliberately NOT first-class: they break parity. An escape hatch exists
   (`run` with raw shell) but it is the author's parity risk, flagged at compile time.
3. **Fully-custom executor.** The execution kernel is ours end-to-end — no third-party pipeline
   engine. The IR is the contract; the GHA backend is a transitional renderer; the AWS Lambda
   control plane is the real runtime we own.
4. **Parity is provable, by round-trip.** The acceptance gate for each backend: take the project's
   *current* pipelines (`deploy-mcp`, `deploy-sso`), express them in the IR, render them back, and
   show behavioural equivalence. Same IR on two backends must produce the same result/artifacts.
   (No backend may silently drop a construct — unsupported constructs FAIL-CLOSED at compile time.)
5. **Secrets are references, never values.** The IR carries named `SecretRef`s only; each backend
   resolves them from its secret store (GH encrypted secrets / SSM+Secrets Manager). Secret
   material is never inlined, never logged.
6. **Gates bind to the board.** Approval/step-up gates resolve against the board's owner-approval +
   passkey/biometric step-up surfaces, so a deploy gate is the same contract everywhere.

## 2. The canonical model (IR)

A `Pipeline` is a DAG of `Job`s. Each `Job` runs an ordered list of `Step`s on an abstract
`Runner`. A `Step` is either a `run` (shell) or a typed `capability` invocation.

```
Pipeline
  name            string
  triggers        []Trigger        # push{branches}, pull_request{branches}, manual{inputs}, schedule{cron}
  concurrency      Concurrency?      # {group, cancelInProgress}
  env             map[string]string # pipeline-wide, non-secret
  secrets         []SecretRef       # named; resolved per-backend
  jobs            []Job

Job
  id              string            # unique within the pipeline
  needs           []string          # job ids; defines the DAG (acyclic, must exist)
  runner          Runner            # {sizeClass: small|standard|large, image, arch}
  matrix          map[string][]string?  # fan-out; backend expands
  if              string?           # minimal, backend-portable condition
  env             map[string]string
  timeoutMinutes  int?
  gate            Gate?             # approval/step-up before the job runs
  steps           []Step
  produces        []Artifact?       # named outputs
  consumes        []string?         # artifact names from upstream jobs

Step
  name            string
  capability      Capability?       # typed, portable (see §3) — XOR with run
  run             string?           # raw shell escape hatch (parity risk, flagged)
  with            map[string]string # capability params
  env             map[string]string
  workingDir      string?
  continueOnError bool

Gate     { type: approval, approvers []string, requireStepUp bool }
Runner   { sizeClass, image, arch }
Trigger  { kind, branches[], cron, inputs[] }
SecretRef{ name, fromKey }            # fromKey = lookup key in the backend secret store
Artifact { name, paths []string, retentionDays? }
```

## 3. Capabilities (the closed set)

| Capability | Params (`with`) | GHA backend | Lambda backend |
|---|---|---|---|
| `checkout` | `ref`, `depth` | `actions/checkout` | git clone in executor |
| `setup-tool` | `tool`, `version` | `actions/setup-*` | toolchain layer in image |
| `cache` | `key`, `paths` | `actions/cache` | S3-backed cache by key |
| `artifact-upload` | `name`, `paths` | `actions/upload-artifact` | S3 put under run id |
| `artifact-download` | `name` | `actions/download-artifact` | S3 get |
| `cloud-auth` | `provider`(aws/gcp/do), `via`(oidc) | `aws-actions/configure-aws-credentials` (OIDC) | task role / STS |

A `Step` is **either** a typed `capability` (above) **or** a raw `run` script — never both, never
neither (`Validate` enforces the XOR). `run` is the escape hatch, not a capability: it is a parity
risk (a backend may differ in shell/OS), so the compiler flags it; it renders to a `run:` step on
GHA and to an `exec` in a Fargate/CodeBuild step on the Lambda backend.

Adding a capability is the extension point: implement it once per backend adapter. The set is
intentionally small so parity stays tractable.

## 4. Backends

A **backend adapter** is `Compile(Pipeline) -> artifact` + `Run(...)`:

- **`github-actions`** (`pd-DEVOPS-COMPILE-GHA`): pure compile to one workflow YAML. Transitional —
  proves the IR can express every real pipeline and lets us migrate incrementally while the runtime
  is built.
- **`aws-lambda`** (`pd-DEVOPS-LAMBDA-RUNNER`): the super-custom runtime. GitHub webhook /
  EventBridge → Step Functions orchestrator (the DAG) → executors: **Lambda** for short steps,
  **Fargate/CodeBuild** for long or Docker steps (Lambda's 15-min + no-Docker-daemon limits).
  Artifacts in S3, logs to CloudWatch, OIDC/STS for cloud-auth. **All of this infra is provisioned
  by the pyxcloud TF provider** (dogfood) — the pipeline runtime is itself a `compare` placement.

## 5. Validation (compile-time, FAIL-CLOSED)

`Pipeline.Validate()` rejects, before any backend sees it: duplicate job ids; `needs` pointing at a
missing job; a cycle in the job DAG; a `Step` that sets neither `capability` nor `run` (or both); an
unknown capability; a `SecretRef`/artifact `consumes` with no producer; a `gate.requireStepUp` with
no approvers. A backend additionally rejects any construct it cannot represent (no silent drop).

## 6. Acceptance (parity gate)

1. Express `deploy-mcp` and `deploy-sso` in the IR (`examples/pipeline/`).
2. `github-actions` backend regenerates equivalent workflows; behaviour matches today's.
3. `aws-lambda` backend runs the same IR end-to-end; result/artifacts/logs match the GHA run.
4. Only then migrate all repos; GitHub Actions becomes a trigger or is dismissed.

## 7. Out of scope (this task)

The compiler/runtime are later phases. This task delivers: this spec, the canonical Go types
(`internal/pipeline`), `Validate()`, and a real pipeline expressed in the IR as a coverage proof.
