# DigitalOcean round-trip harness for pd-TF-S3 (object/blob storage, Spaces).
#
# Concrete resource (digitalocean_spaces_bucket with acl + versioning) is
# GENERATED from ../storage.json by `pyxnet-render` into generated.tf. PRIVATE BY
# DEFAULT: acl = "private" when public=false (acl = "public-read" only when public).
#
# Test flow (SPEC 6):
#   pyxnet-render -fixture ../storage.json -provider digitalocean -component object-storage > generated.tf
#   terraform init && terraform plan
#   terraform apply -auto-approve     # gated, real creds
#   terraform destroy -auto-approve
#
# CREDS: DO Spaces uses S3-compatible keys distinct from the API token —
# SPACES_ACCESS_KEY_ID + SPACES_SECRET_ACCESS_KEY (plus DIGITALOCEAN_TOKEN for the
# provider). For a disposable round-trip use a test fixture with force_destroy=true.

terraform {
  required_providers {
    digitalocean = {
      source  = "digitalocean/digitalocean"
      version = "~> 2.0"
    }
  }
}

# token from DIGITALOCEAN_TOKEN; spaces_access_id / spaces_secret_key from
# SPACES_ACCESS_KEY_ID / SPACES_SECRET_ACCESS_KEY.
provider "digitalocean" {}
