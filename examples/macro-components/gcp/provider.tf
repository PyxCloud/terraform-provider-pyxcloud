# GCP plan/validate harness for pd-TF-REST-LAMBDA. The concrete resources are
# GENERATED from ../macro.json by pyxnet-render into generated.tf. Region pins to
# the catalog-resolved csp_region (Frankfurt -> europe-west3). Real apply requires
# application-default credentials + GOOGLE_PROJECT; otherwise this is plan/validate
# only (the round-trip harness skips the apply explicitly).
terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 5.0"
    }
  }
}

provider "google" {
  project = "pyxcloud-plan-only"
  region  = "europe-west3"
}
