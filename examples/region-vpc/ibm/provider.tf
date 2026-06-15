# IBM Cloud round-trip harness for pd-TF-REGION-VPC (wave-2).
#
# The concrete resources (ibm_is_vpc + ibm_is_subnet) are GENERATED from the
# canonical fixture (../place.json) by `pyxnet-render -provider ibm`, written next
# to this file as generated.tf. This file only pins the cloud provider + region
# and declares the out-of-band variables the IBM renderers reference.
#
# Test flow (SPEC §6) — no IBM creds in CI, so init + validate + plan only:
#   pyxnet-render -fixture ../place.json -provider ibm > generated.tf
#   terraform init
#   terraform validate
#   IC_API_KEY=... terraform plan      # gated, real creds (apply/destroy too)

terraform {
  required_providers {
    ibm = {
      source  = "IBM-Cloud/ibm"
      version = "~> 1.70"
    }
  }
}

# The region MUST equal the catalog-resolved csp_region for the fixture region
# (Frankfurt -> eu-de). Kept in sync by the generated resources' zones (eu-de-1..3).
provider "ibm" {
  region = "eu-de"
}

# Out-of-band identifiers the IBM renderers reference (account-specific, never
# baked into the canonical topology). Defaulted to placeholders so `terraform
# validate` passes without creds; a real apply supplies them via TF_VAR_*.
variable "ibm_resource_group_id" {
  type    = string
  default = "00000000000000000000000000000000"
}
