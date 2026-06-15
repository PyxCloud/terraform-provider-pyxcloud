# AWS round-trip harness for pd-TF-REGION-VPC.
#
# The concrete resources (aws_vpc + aws_subnet) are GENERATED from the canonical
# fixture (../place.json) by `pyxnet-render -provider aws`, written next to this
# file as generated.tf. This file only pins the cloud provider + region.
#
# Test flow (SPEC §6):
#   pyxnet-render -fixture ../place.json -provider aws > generated.tf
#   AWS_PROFILE=pyxcloudtest terraform init
#   AWS_PROFILE=pyxcloudtest terraform plan
#   AWS_PROFILE=pyxcloudtest terraform apply -auto-approve   # gated, real creds
#   AWS_PROFILE=pyxcloudtest terraform destroy -auto-approve

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

# The region MUST equal the catalog-resolved csp_region for the fixture region
# (Frankfurt -> eu-central-1). Kept in sync by the generated resources' AZs.
provider "aws" {
  region = "eu-central-1"
}
