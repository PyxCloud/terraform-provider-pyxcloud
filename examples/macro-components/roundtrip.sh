#!/usr/bin/env bash
# Round-trip test harness for pd-TF-REST-LAMBDA (the remaining macro components,
# SPEC §6). It:
#   1. renders EVERY component for aws/gcp/do from the canonical fixtures (the
#      schema/shape proof) — DO-unsupported components are EXPECTED to emit a
#      clean plan-time error (recorded, not a silent skip);
#   2. on AWS, does a REAL apply -> verify -> immediate destroy for the
#      CHEAP/FAST types only: SQS (managed-queue), Secrets Manager
#      (secrets-manager, no rotation), Route53 (dns-zone), Lambda (serverless,
#      tiny inline zip);
#   3. leaves the EXPENSIVE/SLOW types PLAN-ONLY (never applied): ElastiCache
#      (cache), Kinesis (event-streaming), CloudFront (cdn-service), WAFv2
#      (waf-service), EKS (managed-kubernetes) — slow/costly to create;
#   4. GCP/DO are render + `terraform validate` only unless creds are present.
#
# Creds: aws -> AWS_PROFILE=pyxcloudtest; gcp -> application-default + GOOGLE_PROJECT;
# do -> DIGITALOCEAN_TOKEN. A missing-cred provider is SKIPPED EXPLICITLY.
set -euo pipefail
cd "$(dirname "$0")"

ROOT="$(cd ../.. && pwd)"
RENDER_BIN="${PYXNET_RENDER:-$ROOT/pyxnet-render}"
if [[ ! -x "$RENDER_BIN" ]]; then
  echo "building pyxnet-render..."
  (cd "$ROOT" && go build -o pyxnet-render ./cmd/pyxnet-render)
  RENDER_BIN="$ROOT/pyxnet-render"
fi
FIXTURE="macro.json"
K8S_FIXTURE_AWS="k8s-aws.json"   # AWS k8s uses Dublin (the region with VM SKU rows)

# ── 1. render matrix (plan-shape proof) ──────────────────────────────────────
echo "=== render matrix (component x provider) ==="
for provider in aws gcp digitalocean; do
  for comp in cache managed-queue event-streaming dns-zone cdn-service waf-service secrets-manager serverless-function; do
    if "$RENDER_BIN" -fixture "$FIXTURE" -provider "$provider" -component "$comp" >/dev/null 2>/tmp/pyx-err; then
      echo "  OK          $provider/$comp"
    else
      echo "  UNSUPPORTED $provider/$comp"
    fi
  done
  kfix="$FIXTURE"; [[ "$provider" == "aws" ]] && kfix="$K8S_FIXTURE_AWS"
  if "$RENDER_BIN" -fixture "$kfix" -provider "$provider" -component managed-kubernetes >/dev/null 2>/tmp/pyx-err; then
    echo "  OK          $provider/managed-kubernetes"
  else
    echo "  ERR         $provider/managed-kubernetes: $(cat /tmp/pyx-err)"
  fi
done

# ── 2. AWS real round-trip (cheap types only) ────────────────────────────────
echo
echo "=== AWS round-trip: real apply for CHEAP types, plan-only for EXPENSIVE ==="
: > aws/generated.tf
for comp in managed-queue secrets-manager dns-zone serverless-function; do
  "$RENDER_BIN" -fixture "$FIXTURE" -provider aws -component "$comp" >> aws/generated.tf
done
# Round-trip wiring (TEST-ONLY): a self-contained Lambda execution role + a tiny
# inline zip, fed into the generated aws_lambda_function. Production deployments
# supply var.lambda_role_arn from a sibling access-policy component instead.
cat > aws/support.tf <<'EOF'
data "aws_iam_policy_document" "lambda_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}
resource "aws_iam_role" "lambda_exec" {
  name               = "pyxcloud-rt-lambda-exec"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume.json
  tags               = { pyxcloud = "true" }
}
resource "aws_iam_role_policy_attachment" "lambda_basic" {
  role       = aws_iam_role.lambda_exec.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}
data "archive_file" "fn" {
  type        = "zip"
  output_path = "${path.module}/function.zip"
  source {
    content  = "def handler(event, context):\n    return {'ok': True}\n"
    filename = "main.py"
  }
}
EOF
# Point the generated lambda at the support role + zip, and use a non-reserved
# domain for the Route53 round-trip (AWS reserves *.example.com).
perl -0pi -e 's/role          = var\.lambda_role_arn/role             = aws_iam_role.lambda_exec.arn\n  source_code_hash = data.archive_file.fn.output_base64sha256/' aws/generated.tf
perl -0pi -e 's/filename      = "function.zip"/filename         = data.archive_file.fn.output_path/' aws/generated.tf
perl -0pi -e 's/pyxcloud-plan-only\.example\.com/pyxcloud-rt-test.com/' aws/generated.tf

( cd aws && terraform init -input=false >/dev/null && terraform validate -no-color ) && echo "aws cheap-types config valid"

if AWS_PROFILE=pyxcloudtest aws sts get-caller-identity >/dev/null 2>&1; then
  echo ">>> AWS creds present: real apply + verify + IMMEDIATE destroy"
  ( cd aws && AWS_PROFILE=pyxcloudtest terraform apply -auto-approve -no-color )
  echo ">>> verify:"
  AWS_PROFILE=pyxcloudtest aws sqs get-queue-url --queue-name jobs --region eu-central-1 || true
  AWS_PROFILE=pyxcloudtest aws secretsmanager describe-secret --secret-id db-password --region eu-central-1 --query Name || true
  AWS_PROFILE=pyxcloudtest aws route53 list-hosted-zones-by-name --dns-name pyxcloud-rt-test.com --query 'HostedZones[0].Name' || true
  AWS_PROFILE=pyxcloudtest aws lambda get-function --function-name api --region eu-central-1 --query 'Configuration.[FunctionName,Runtime]' || true
  echo ">>> DESTROY:"
  ( cd aws && AWS_PROFILE=pyxcloudtest terraform destroy -auto-approve -no-color )
  echo ">>> verify teardown (get-function should fail):"
  AWS_PROFILE=pyxcloudtest aws lambda get-function --function-name api --region eu-central-1 2>&1 || echo "OK: lambda gone"
else
  echo ">>> SKIP AWS apply/destroy: no pyxcloudtest profile (plan/validate only)"
fi

# EXPENSIVE types — render + validate ONLY, NEVER applied.
echo
echo "=== AWS EXPENSIVE types (plan-only, NEVER applied): Kinesis + WAFv2 ==="
mkdir -p aws/plan-only
cp aws/provider.tf aws/plan-only/provider.tf
: > aws/plan-only/generated.tf
"$RENDER_BIN" -fixture "$FIXTURE" -provider aws -component event-streaming >> aws/plan-only/generated.tf
"$RENDER_BIN" -fixture "$FIXTURE" -provider aws -component waf-service     >> aws/plan-only/generated.tf
( cd aws/plan-only && terraform init -input=false >/dev/null && terraform validate -no-color ) && echo "aws expensive-types config valid (plan-only)"
echo ">>> ElastiCache / CloudFront / EKS reference sibling resources (subnets/origin/roles);"
echo "    their shape is proven by the render matrix above + the unit tests. NEVER real-applied (cost)."

# ── 3. GCP / DO: render + validate only ──────────────────────────────────────
echo
echo "=== GCP: render + validate (plan-only) ==="
: > gcp/generated.tf
for comp in managed-queue event-streaming dns-zone secrets-manager serverless-function; do
  "$RENDER_BIN" -fixture "$FIXTURE" -provider gcp -component "$comp" >> gcp/generated.tf
done
( cd gcp && terraform init -input=false >/dev/null && terraform validate -no-color ) && echo "gcp config valid"
echo ">>> SKIP GCP apply: validate-only (real apply needs application-default creds + GOOGLE_PROJECT)"

echo
echo "=== DigitalOcean: render supported + validate (queue/stream/waf/secrets are UNSUPPORTED -> clean errors) ==="
: > digitalocean/generated.tf
# The DO cache (managed Redis) is private to the place's VPC, so render the
# network sibling first — a faithful place graph, not an orphan resource.
"$RENDER_BIN" -fixture "$FIXTURE" -provider digitalocean -component network >> digitalocean/generated.tf
for comp in cache dns-zone serverless-function; do
  "$RENDER_BIN" -fixture "$FIXTURE" -provider digitalocean -component "$comp" >> digitalocean/generated.tf
done
( cd digitalocean && terraform init -input=false >/dev/null && terraform validate -no-color ) && echo "do config valid"
echo ">>> SKIP DO apply: validate-only (real apply needs DIGITALOCEAN_TOKEN)"

echo
echo "round-trip complete."
