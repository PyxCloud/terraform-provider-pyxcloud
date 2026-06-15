#!/usr/bin/env bash
# Render the full alicloud macro-component matrix from ../macro.json into
# generated.tf (network + security-group + every macro component), for the
# validate/plan-only round-trip (pd-TF-PROVIDERS-WAVE2: alibaba). No creds needed
# for `terraform validate`.
set -euo pipefail
cd "$(dirname "$0")"
ROOT="$(cd ../../.. && pwd)"
BIN="${PYXNET_RENDER:-$ROOT/pyxnet-render}"
if [[ ! -x "$BIN" ]]; then
  (cd "$ROOT" && go build -o pyxnet-render ./cmd/pyxnet-render)
  BIN="$ROOT/pyxnet-render"
fi
F=../macro.json
: > generated.tf
"$BIN" -fixture "$F" -provider alicloud -component network          >> generated.tf
echo "" >> generated.tf
"$BIN" -fixture "$F" -provider alicloud -component security-group   >> generated.tf
# object-storage is co-rendered because the cdn-service origin references the
# bucket resource (alicloud_oss_bucket.<origin>.extranet_endpoint).
for comp in object-storage cache managed-queue event-streaming dns-zone cdn-service waf-service secrets-manager serverless-function managed-kubernetes; do
  echo "" >> generated.tf
  "$BIN" -fixture "$F" -provider alicloud -component "$comp"        >> generated.tf
done
echo "wrote generated.tf ($(wc -l < generated.tf) lines)"
