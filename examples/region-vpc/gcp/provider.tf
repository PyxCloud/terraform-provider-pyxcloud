# GCP round-trip harness for pd-TF-REGION-VPC.
#
# Concrete resources (google_compute_network + google_compute_subnetwork) are
# GENERATED from ../place.json by `pyxnet-render -provider gcp` into generated.tf.
#
# Test flow (SPEC §6):
#   pyxnet-render -fixture ../place.json -provider gcp > generated.tf
#   terraform init && terraform plan
#   terraform apply -auto-approve     # gated, real creds
#   terraform destroy -auto-approve

terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 6.0"
    }
  }
}

# project comes from GOOGLE_PROJECT / gcloud config. Region matches the
# catalog-resolved csp_region for the fixture region (Frankfurt -> europe-west3).
provider "google" {
  region = "europe-west3"
}
