# GCP round-trip harness for pd-TF-ASG (virtual-machine-scale-group).
#
# Concrete resources (google_compute_network + _subnetwork + _firewall +
# google_compute_instance_template + google_compute_region_instance_group_manager
# + google_compute_region_autoscaler + google_compute_health_check) are GENERATED
# from ../asg.json by `pyxnet-render` into generated.tf.
#
# Test flow (SPEC 6):
#   pyxnet-render -fixture ../asg.json -provider gcp -component network         >  generated.tf
#   pyxnet-render -fixture ../asg.json -provider gcp -component security-group  >> generated.tf
#   pyxnet-render -fixture ../asg.json -provider gcp -component scale-group     >> generated.tf
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
# the regional MIG/autoscaler spread the group multi-zone within that region.
provider "google" {
  region = "europe-west3"
}
