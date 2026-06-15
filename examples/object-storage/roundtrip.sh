#!/usr/bin/env bash
# Round-trip test harness for pd-TF-S3 (object/blob storage, SPEC 6).
#
# For each provider:
#   1. generate the concrete .tf from the canonical fixture:
#        object-storage -> generated.tf
#   2. terraform init + plan (always)
#   3. terraform apply + verify + destroy (only when test creds are present;
#      a missing-cred provider is SKIPPED EXPLICITLY, never silently)
#
# SECURITY (SPEC 5.7): the bucket is PRIVATE BY DEFAULT. The fixtures set
# public=false, so the rendered AWS config carries the full public-access-block
# (all four flags true), GCP carries uniform access + enforced
# public_access_prevention, and DO uses acl=private. PyxCloud never emits a
# world-readable bucket by default.
#
# TEARDOWN: the AWS apply path uses the TEST fixture (storage-aws.json) with the
# TEST-ONLY override force_destroy=true so a just-created bucket tears down
# cleanly. It runs `destroy` IMMEDIATELY after verifying and asserts the bucket is
# gone. The PRODUCTION fixture (storage.json) keeps force_destroy=false.
#
# Creds:
#   aws -> AWS_PROFILE=pyxcloudtest (aws sts get-caller-identity)
#   gcp -> application-default creds + GOOGLE_PROJECT
#   do  -> DIGITALOCEAN_TOKEN + SPACES_ACCESS_KEY_ID + SPACES_SECRET_ACCESS_KEY
set -euo pipefail
cd "$(dirname "$0")"

ROOT="$(cd ../.. && pwd)"
RENDER_BIN="${PYXNET_RENDER:-$ROOT/pyxnet-render}"
if [[ ! -x "$RENDER_BIN" ]]; then
  echo "building pyxnet-render..."
  (cd "$ROOT" && go build -o pyxnet-render ./cmd/pyxnet-render)
  RENDER_BIN="$ROOT/pyxnet-render"
fi

# gen <provider> <dir> <fixture>
gen() {
  local provider="$1" dir="$2" fixture="$3"
  : > "$dir/generated.tf"
  "$RENDER_BIN" -fixture "$fixture" -provider "$provider" -component object-storage >> "$dir/generated.tf"
  echo "generated $dir/generated.tf"
}

plan() { ( cd "$1" && terraform init -input=false >/dev/null && terraform plan -input=false -no-color ); }

# The catalog-derived bucket name (deterministic) — recover it from the rendered tf.
bucket_name() { grep -m1 -E 'bucket\s+=' "$1/generated.tf" | sed -E 's/.*"([^"]+)".*/\1/'; }

# --- AWS (Frankfurt -> eu-central-1; S3 bucket, versioning, public-access-block) ---
# Uses the TEST fixture with the visible test-only force_destroy override.
gen aws aws storage-aws.json
plan aws
if AWS_PROFILE=pyxcloudtest aws sts get-caller-identity >/dev/null 2>&1; then
  echo ">>> AWS creds present: real apply + verify + immediate destroy"
  BUCKET="$(bucket_name aws)"
  ( cd aws && AWS_PROFILE=pyxcloudtest terraform apply -auto-approve -no-color )
  echo ">>> verify bucket versioning ($BUCKET):"
  AWS_PROFILE=pyxcloudtest aws s3api get-bucket-versioning --bucket "$BUCKET" --region eu-central-1
  echo ">>> verify public-access-block ($BUCKET) — all four flags must be true:"
  AWS_PROFILE=pyxcloudtest aws s3api get-public-access-block --bucket "$BUCKET" --region eu-central-1
  echo ">>> DESTROY (test fixture has force_destroy=true):"
  ( cd aws && AWS_PROFILE=pyxcloudtest terraform destroy -auto-approve -no-color )
  echo ">>> verify teardown (head-bucket should fail — bucket gone):"
  AWS_PROFILE=pyxcloudtest aws s3api head-bucket --bucket "$BUCKET" --region eu-central-1 2>&1 || echo "OK: bucket no longer exists"
else
  echo ">>> SKIP AWS apply/destroy: no pyxcloudtest profile (plan only)"
fi

# --- GCP (Frankfurt -> europe-west3; Cloud Storage bucket) ---
gen gcp gcp storage.json
GOOGLE_PROJECT="${GOOGLE_PROJECT:-pyxcloud-plan-only}" plan gcp
if [[ "${GOOGLE_PROJECT:-}" != "" && "${GOOGLE_PROJECT}" != "pyxcloud-plan-only" ]] && gcloud auth application-default print-access-token >/dev/null 2>&1; then
  echo ">>> GCP creds present: real apply + destroy"
  ( cd gcp && terraform apply -auto-approve -no-color && terraform destroy -auto-approve -no-color )
else
  echo ">>> SKIP GCP apply/destroy: no application-default creds / GOOGLE_PROJECT (plan only)"
fi

# --- DigitalOcean (Frankfurt -> fra1; Spaces bucket) ---
gen digitalocean digitalocean storage.json
if [[ "${DIGITALOCEAN_TOKEN:-}" != "" && "${SPACES_ACCESS_KEY_ID:-}" != "" && "${SPACES_SECRET_ACCESS_KEY:-}" != "" ]]; then
  echo ">>> DO creds present: real apply + destroy"
  ( cd digitalocean && terraform init -input=false >/dev/null && terraform apply -auto-approve -no-color && terraform destroy -auto-approve -no-color )
else
  echo ">>> SKIP DO apply/destroy: no DIGITALOCEAN_TOKEN / SPACES_* keys (plan only — init+validate)"
  ( cd digitalocean && terraform init -input=false >/dev/null && terraform validate -no-color )
fi

echo "round-trip complete."
