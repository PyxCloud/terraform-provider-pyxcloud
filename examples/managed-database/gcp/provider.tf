# GCP round-trip harness for pd-TF-MDB (managed-database, Cloud SQL).
#
# Concrete resources (google_compute_network + _subnetwork + _firewall +
# google_sql_database_instance) are GENERATED from ../db.json by `pyxnet-render`
# into generated.tf.
#
# Test flow (SPEC 6):
#   pyxnet-render -fixture ../db.json -provider gcp -component network          >  generated.tf
#   pyxnet-render -fixture ../db.json -provider gcp -component security-group   >> generated.tf
#   pyxnet-render -fixture ../db.json -provider gcp -component managed-database >> generated.tf
#   terraform init && terraform plan
#   terraform apply -auto-approve     # gated, real creds
#   terraform destroy -auto-approve
#
# The production fixture keeps deletion_protection=true on the Cloud SQL instance;
# a real destroy would require disabling it first (mirrors the production-safe
# default). For a disposable round-trip use a test fixture with the override.

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
