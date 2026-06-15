# Alibaba Cloud (alicloud) round-trip harness for pd-TF-REGION-VPC
# (pd-TF-PROVIDERS-WAVE2: alibaba).
#
# The concrete resources (alicloud_vpc + alicloud_vswitch) are GENERATED from the
# canonical fixture (../place.json) by `pyxnet-render -provider alicloud`, written
# next to this file as generated.tf. This file only pins the cloud provider + region.
#
# NO ALIBABA CREDS: this wave-2 provider ships validate/plan-only. The round-trip
# is `terraform init` + `terraform validate` (offline, no creds) and, with creds,
# `terraform plan`. We never apply (no Alibaba account in CI):
#
#   pyxnet-render -fixture ../place.json -provider alicloud > generated.tf
#   terraform init
#   terraform validate                                       # offline, no creds
#   ALICLOUD_ACCESS_KEY=... ALICLOUD_SECRET_KEY=... terraform plan   # creds only
#
# The region MUST equal the catalog-resolved csp_region for the fixture region
# (Frankfurt -> eu-central-1), kept in sync by the generated vswitches' zone_ids.

terraform {
  required_providers {
    alicloud = {
      source  = "aliyun/alicloud"
      version = "~> 1.240"
    }
  }
}

provider "alicloud" {
  region = "eu-central-1"
}
