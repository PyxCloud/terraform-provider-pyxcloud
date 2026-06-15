# Ubicloud (wave-2) provider pin for the round-trip harness
# [pd-TF-PROVIDERS-WAVE2: ubicloud].
#
# The concrete resources (ubicloud_private_subnet + ubicloud_firewall(+_rule) +
# ubicloud_vm + ubicloud_postgres) are GENERATED from the canonical fixture
# (./place.json) by `pyxnet-render -provider ubicloud`, written next to this file.
# This file only pins the cloud provider + declares the inputs Ubicloud needs that
# are not part of PyxCloud's abstract model (project id, VM SSH key).
#
# HONEST COVERAGE: Ubicloud's official Terraform provider exposes only
# vm/postgres/private_subnet/firewall(+rule)/project — so ONLY the four supported
# components above are generated. Every other PyxCloud component (scale-group, lb,
# object-storage, cache, queue, stream, dns, cdn, waf, k8s, secrets, serverless)
# raises a clean plan-time `unsupported` error from pyxnet-render and is NOT
# emitted (see ./roundtrip.sh, which asserts those errors).
#
# Test flow (SPEC §6 — adapted; no Ubicloud test creds are available, so this PR
# does validate/plan ONLY, never a real apply):
#   ./roundtrip.sh                          # generates *.tf + asserts unsupported set
#   terraform init
#   terraform validate                      # no creds needed
#   UBICLOUD_API_TOKEN=... terraform plan   # gated, real creds (NOT run here)
#
# Ubicloud derives its region per-resource (location) from the catalog-resolved
# csp_region (Frankfurt -> eu-central-h1), so no provider-level region is pinned.

terraform {
  required_providers {
    ubicloud = {
      source  = "ubicloud/ubicloud"
      version = "~> 0.1"
    }
  }
}

provider "ubicloud" {
  # api_token is supplied out-of-band (UBICLOUD_API_TOKEN) for a real plan/apply;
  # validate needs no credentials.
}

# Inputs Ubicloud requires that are NOT part of PyxCloud's abstract model. The
# generated resources reference these (var.ubicloud_project_id / _ssh_public_key).
variable "ubicloud_project_id" {
  type        = string
  description = "Ubicloud project id every resource is scoped to (e.g. pj01...)."
  default     = "pj0000000000000000000000000" # placeholder so `validate` passes with no creds
}

variable "ubicloud_ssh_public_key" {
  type        = string
  description = "SSH public key injected into every ubicloud_vm."
  default     = "ssh-ed25519 AAAAPLACEHOLDERKEYFORVALIDATEONLY pyxcloud@validate"
}
