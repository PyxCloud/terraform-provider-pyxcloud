#!/usr/bin/env bash
# Terraform 1.13.4, pinned to HashiCorp's published SHA256SUMS.
# Select the actual host, since self-hosted runner labels can be stale.
set -euo pipefail
TF_VERSION=1.13.4
case "$(uname -s)-$(uname -m)" in
  Linux-x86_64) TF_PLATFORM=linux_amd64; TF_SHA=98aa516201e948306698efd9954ab4cc0d1227c2578ba56245898b5f679e590b ;;
  Linux-aarch64|Linux-arm64) TF_PLATFORM=linux_arm64; TF_SHA=a17bde150a4d6c9e7ece063ab634c07723b8242e078f3ae9017486277d6690c4 ;;
  Darwin-x86_64) TF_PLATFORM=darwin_amd64; TF_SHA=0a4441f6aaf37bb18cbaf5c7538f9be638f0d93eb325d285b4b452d014a7737a ;;
  Darwin-arm64) TF_PLATFORM=darwin_arm64; TF_SHA=6b9e2ff8102c469added7be28cd5cfc2c5a8f7fc3be2df8fe9ee5458d84a1305 ;;
  *) echo 'Unsupported Terraform host OS/architecture' >&2; exit 1 ;;
esac
: "${GITHUB_PATH:?GITHUB_PATH must name the runner path file}"
TF_DIR="$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/terraform-1.13.4.XXXXXX")"
TF_ZIP="terraform_${TF_VERSION}_${TF_PLATFORM}.zip"
curl -fsSL --connect-timeout 15 --max-time 120 \
  "https://releases.hashicorp.com/terraform/${TF_VERSION}/${TF_ZIP}" -o "${TF_DIR}/${TF_ZIP}"
if command -v sha256sum >/dev/null 2>&1; then
  printf '%s  %s\n' "$TF_SHA" "${TF_DIR}/${TF_ZIP}" | sha256sum -c -
else
  printf '%s  %s\n' "$TF_SHA" "${TF_DIR}/${TF_ZIP}" | shasum -a 256 -c -
fi
unzip -q "${TF_DIR}/${TF_ZIP}" -d "$TF_DIR"
"${TF_DIR}/terraform" version -json > "${TF_DIR}/version.json"
grep -Eq '"terraform_version"[[:space:]]*:[[:space:]]*"1\.13\.4"' "${TF_DIR}/version.json"
printf '%s\n' "$TF_DIR" >> "$GITHUB_PATH"
echo "Verified executable Terraform ${TF_VERSION} for ${TF_PLATFORM}"
