# StackIt round-trip harness for wave-2 (pd-TF-PROVIDERS-WAVE2: stackit).
#
# Concrete StackIt resources are GENERATED from ./place.json by
# `pyxnet-render -provider stackit -component <c>` into generated.tf.
#
# Test flow (SPEC §6):
#   ./roundtrip.sh                              # generate + validate/plan
#   export STACKIT_SERVICE_ACCOUNT_TOKEN=...    # gated, real creds
#   terraform init && terraform plan
#   terraform apply -auto-approve && terraform destroy -auto-approve
#
# No StackIt creds in CI -> validate/plan only; a missing-cred run is SKIPPED
# explicitly, never silently (mirrors the wave-1 harness).

terraform {
  required_providers {
    stackit = {
      source  = "stackitcloud/stackit"
      version = ">= 0.30"
    }
  }
}

# Project the generated resources are created in. Supplied via
# TF_VAR_stackit_project_id (never hard-coded into the generated config).
variable "stackit_project_id" {
  type        = string
  description = "STACKIT project ID the resources are created in."
  default     = "00000000-0000-0000-0000-000000000000"
}

# Region defaults to eu01 (the catalog-resolved csp_region for Frankfurt). Auth
# comes from STACKIT_SERVICE_ACCOUNT_TOKEN / a key file in the environment.
provider "stackit" {
  default_region = "eu01"
}
