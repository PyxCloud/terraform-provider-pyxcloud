# GCP round-trip harness for pd-TF-S3 (object/blob storage, Cloud Storage).
#
# Concrete resource (google_storage_bucket with uniform_bucket_level_access +
# versioning) is GENERATED from ../storage.json by `pyxnet-render` into
# generated.tf. PRIVATE BY DEFAULT: uniform bucket-level access + enforced
# public_access_prevention when public=false.
#
# Test flow (SPEC 6):
#   pyxnet-render -fixture ../storage.json -provider gcp -component object-storage > generated.tf
#   terraform init && terraform plan
#   terraform apply -auto-approve     # gated, real creds (GOOGLE_PROJECT + ADC)
#   terraform destroy -auto-approve
#
# For a disposable round-trip use a test fixture with force_destroy=true so a
# just-created bucket tears down cleanly.

terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 6.0"
    }
  }
}

# project comes from GOOGLE_PROJECT / gcloud config. The bucket location is set
# from the catalog-resolved csp_region for the fixture region
# (Frankfurt -> europe-west3, rendered as EUROPE-WEST3).
provider "google" {
  region = "europe-west3"
}
