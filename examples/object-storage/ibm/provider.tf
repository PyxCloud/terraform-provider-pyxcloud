# IBM Cloud round-trip harness for pd-TF-S3 (wave-2). IBM uses ibm_resource_instance(COS)+ibm_cos_bucket, private by default.
terraform {
  required_providers {
    ibm = {
      source  = "IBM-Cloud/ibm"
      version = "~> 1.70"
    }
  }
}

# Region MUST equal the catalog-resolved csp_region for the fixture region
# (Frankfurt -> eu-de). Kept in sync by the generated resources' zones.
provider "ibm" {
  region = "eu-de"
}

variable "ibm_resource_group_id" {
  type    = string
  default = "00000000000000000000000000000000"
}
