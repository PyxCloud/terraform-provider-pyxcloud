#!/usr/bin/env bash
# Plan-first round-trip for pd-DEP-PYXLAMBDA-CONTROLPLANE (the pyx-lambda DevOps
# control-plane component, dogfood). It:
#   1. renders the canonical control-plane fixture into concrete AWS HCL via the
#      catalog (the abstract-first descent), wiring the REAL compiled Step Functions
#      ASL (aws/ci.json) emitted by pyx-pipeline-ir into the provisioned state machine;
#   2. runs `terraform init -backend=false` + `terraform validate` (no creds, no apply)
#      as the plan-shape proof;
#   3. asserts every non-AWS provider is a CLEAN plan-time error (the pyx-lambda
#      backend is AWS-specific — never an invented resource).
#
# This NEVER applies. A real apply -> verify -> immediate destroy against the
# pyxcloudtest profile is the operator's gated step (SPEC §6), not run here.
set -euo pipefail
cd "$(dirname "$0")"

ROOT="$(cd ../.. && pwd)"
RENDER_BIN="${PYXNET_RENDER:-$ROOT/pyxnet-render}"
if [[ ! -x "$RENDER_BIN" ]]; then
  echo "building pyxnet-render..."
  (cd "$ROOT" && go build -o pyxnet-render ./cmd/pyxnet-render)
  RENDER_BIN="$ROOT/pyxnet-render"
fi
FIXTURE="control-plane.json"
COMPONENT="pipeline-control-plane"

# ── 1. unsupported-provider matrix (clean plan-time error, never invented) ───
echo "=== unsupported-provider matrix ==="
for provider in gcp digitalocean azure; do
  if "$RENDER_BIN" -fixture "$FIXTURE" -provider "$provider" -component "$COMPONENT" >/dev/null 2>/tmp/pcp-err; then
    echo "  UNEXPECTED  $provider rendered (the pyx-lambda backend is AWS-specific)"; exit 1
  else
    echo "  UNSUPPORTED $provider (clean): $(head -1 /tmp/pcp-err)"
  fi
done

# ── 2. AWS render + terraform validate (plan-shape proof, no apply) ──────────
echo "=== aws render + terraform validate ==="
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
"$RENDER_BIN" -fixture "$FIXTURE" -provider aws -component "$COMPONENT" > "$WORK/control_plane.tf"
cat > "$WORK/provider.tf" <<'EOF'
provider "aws" {
  region                      = "eu-central-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true
  skip_metadata_api_check     = true
}
EOF
( cd "$WORK" && terraform init -backend=false -input=false >/dev/null && terraform validate )
echo "  OK aws/$COMPONENT validated"
echo "rendered HCL: $WORK/control_plane.tf (copied below)"
sed -n '1,40p' "$WORK/control_plane.tf"
