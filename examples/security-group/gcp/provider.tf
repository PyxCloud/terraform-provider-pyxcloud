# GCP round-trip harness for pd-TF-SG.
#
# Concrete resources are GENERATED from ../sg.json by `pyxnet-render`:
#   - google_compute_network (network component, referenced by the firewall)
#   - google_compute_firewall x2 (ingress + egress; GCP firewalls are
#     direction-scoped, so one resource per direction)
#
# Test flow (SPEC §6):
#   pyxnet-render -fixture ../sg.json -provider gcp -component network        >  generated.tf
#   pyxnet-render -fixture ../sg.json -provider gcp -component security-group >> generated.tf
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
