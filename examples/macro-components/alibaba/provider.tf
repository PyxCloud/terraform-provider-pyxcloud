# Alibaba Cloud (alicloud) round-trip harness for the wave-2 macro components
# (pd-TF-PROVIDERS-WAVE2: alibaba; mirrors pd-TF-REST-LAMBDA on aws/gcp/do).
#
# The concrete resources are GENERATED from the canonical fixture (../macro.json)
# by `pyxnet-render -provider alicloud -component <c>`, concatenated next to this
# file as generated.tf. This file pins the cloud provider + region and supplies the
# out-of-band inputs the generated macro resources reference by variable (see
# variables.tf): the alikafka placement vswitch + KMS key, the WAF instance/domain,
# the KMS secret value, and the Function Compute role + OSS code object.
#
# NO ALIBABA CREDS: this wave-2 provider ships validate/plan-only. There is no
# Alibaba account in CI, so this fixture is NEVER applied — the round-trip is:
#
#   ./gen.sh                # render every component -> generated.tf
#   terraform init
#   terraform validate      # offline, no creds (what CI runs)
#   ALICLOUD_ACCESS_KEY=... ALICLOUD_SECRET_KEY=... terraform plan   # creds only
#
# Component cost note (for when creds exist, plan-only — never applied here):
#   - alicloud_kvstore_instance (cache), alicloud_alikafka_instance (stream),
#     alicloud_cdn_domain_new (cdn), alicloud_waf_domain (waf), and
#     alicloud_cs_managed_kubernetes (k8s) are slow/costly to create — plan-only.
#   - alicloud_mns_queue (queue), alicloud_kms_secret (secrets),
#     alicloud_alidns_domain (dns), alicloud_fcv3_function (serverless) are cheap.
#
# Region MUST equal the catalog-resolved csp_region for the fixture region
# (Frankfurt -> eu-central-1).

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
