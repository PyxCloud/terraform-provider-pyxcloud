# GCP round-trip harness for pd-TF-LB (load-balancer).
#
# Concrete resources (google_compute_network + _subnetwork + _firewall +
# instance_template + region_instance_group_manager + region_autoscaler +
# health_check + google_compute_region_backend_service +
# google_compute_forwarding_rule) are GENERATED from ../lb.json by
# `pyxnet-render` into generated.tf.
#
# Test flow (SPEC 6):
#   pyxnet-render -fixture ../lb.json -provider gcp -component network         >  generated.tf
#   pyxnet-render -fixture ../lb.json -provider gcp -component security-group  >> generated.tf
#   pyxnet-render -fixture ../lb.json -provider gcp -component scale-group     >> generated.tf
#   pyxnet-render -fixture ../lb.json -provider gcp -component load-balancer   >> generated.tf
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
# the regional backend service + forwarding rule front the regional MIG.
provider "google" {
  region = "europe-west3"
}
