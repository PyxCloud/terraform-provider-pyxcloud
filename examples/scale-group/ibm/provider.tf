# IBM Cloud round-trip harness for pd-TF-ASG (wave-2). IBM uses ibm_is_instance_group(+_manager).
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

variable "ibm_ssh_key_id" {
  type    = string
  default = "r010-00000000-0000-0000-0000-000000000000"
}
