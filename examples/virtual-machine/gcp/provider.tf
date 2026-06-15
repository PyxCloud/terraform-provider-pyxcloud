# GCP round-trip harness for pd-TF-EC2-VM.
#
# Concrete resources (google_compute_network + _subnetwork + _firewall +
# google_compute_instance) are GENERATED from ../vm.json by `pyxnet-render`
# into generated.tf.
#
# Test flow (SPEC 6):
#   pyxnet-render -fixture ../vm.json -provider gcp -component network         >  generated.tf
#   pyxnet-render -fixture ../vm.json -provider gcp -component security-group  >> generated.tf
#   pyxnet-render -fixture ../vm.json -provider gcp -component virtual-machine >> generated.tf
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
# catalog-resolved csp_region for the fixture region (Frankfurt -> europe-west3);
# the instance zone (europe-west3-a) is baked into the generated instance.
provider "google" {
  region = "europe-west3"
}
