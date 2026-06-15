terraform {
  required_providers {
    oci = {
      source  = "oracle/oci"
      version = "~> 6.0"
    }
  }
}

# Region matches the catalog-resolved csp_region for the fixture region
# (Frankfurt -> eu-frankfurt-1). Auth comes from the OCI config file / instance
# principal; not committed. No OCI creds in CI -> validate/plan only (explicit).
provider "oci" {
  region = "eu-frankfurt-1"
}

variable "compartment_id" {
  type        = string
  description = "OCID of the target compartment"
  default     = "ocid1.compartment.oc1..aaaaaaaaexamplecompartmentplaceholder"
}
