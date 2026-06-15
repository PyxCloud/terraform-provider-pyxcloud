# OVHcloud round-trip harness (pd-TF-PROVIDERS-WAVE2: ovh).
#
# The concrete resources are GENERATED from the canonical fixture (place.json) by
# `pyxnet-render -provider ovh -component <c>`, concatenated into generated.tf.
# This file only pins the ovh/ovh provider and the out-of-band Public Cloud project
# id variable (ovh_service_name) every OVH Public Cloud resource is scoped to.
#
# HONEST SCOPE: only network / managed-database / managed-kubernetes /
# object-storage are emitted on OVH. The other canonical components fail at plan
# time with a clean "unsupported on ovh" error (verified, never invented).
#
# Test flow (SPEC §6) — NO CREDS in CI, so validate/plan only:
#   pyxnet-render -fixture place.json -provider ovh -component network            >  generated.tf
#   pyxnet-render -fixture place.json -provider ovh -component managed-database   >> generated.tf
#   pyxnet-render -fixture place.json -provider ovh -component managed-kubernetes >> generated.tf
#   pyxnet-render -fixture place.json -provider ovh -component object-storage     >> generated.tf
#   terraform init && terraform validate && terraform plan
#   # real apply/destroy is GATED on OVH_* creds + TF_VAR_ovh_service_name; never in CI.

terraform {
  required_providers {
    ovh = {
      source  = "ovh/ovh"
      version = "~> 1.0"
    }
  }
}

# OVH Public Cloud project id (the service_name every Public Cloud resource needs).
# Provided out of band via TF_VAR_ovh_service_name — never committed, never in the
# canonical topology (the same out-of-band pattern wave-1 uses for IAM ARNs / DB
# passwords).
variable "ovh_service_name" {
  type        = string
  description = "OVH Public Cloud project id (service_name)."
  default     = "00000000000000000000000000000000"
}

# OVH provider auth comes from the standard OVH_ENDPOINT / OVH_APPLICATION_KEY /
# OVH_APPLICATION_SECRET / OVH_CONSUMER_KEY environment variables at apply time.
provider "ovh" {
  endpoint = "ovh-eu"
}
