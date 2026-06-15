# AWS round-trip harness for pd-TF-SG.
#
# The concrete resources are GENERATED from the canonical fixture
# (../sg.json) by `pyxnet-render`, written next to this file as generated.tf:
#   - the VPC (network component, required by the SG's vpc_id reference)
#   - the security group + rules (security-group component)
# This file only pins the cloud provider + region.
#
# Test flow (SPEC §6):
#   pyxnet-render -fixture ../sg.json -provider aws -component network        >  generated.tf
#   pyxnet-render -fixture ../sg.json -provider aws -component security-group >> generated.tf
#   AWS_PROFILE=pyxcloudtest terraform init
#   AWS_PROFILE=pyxcloudtest terraform plan
#   AWS_PROFILE=pyxcloudtest terraform apply -auto-approve   # gated, real creds
#   AWS_PROFILE=pyxcloudtest aws ec2 describe-security-groups ...
#   AWS_PROFILE=pyxcloudtest terraform destroy -auto-approve

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

# Region MUST equal the catalog-resolved csp_region for the fixture region
# (Frankfurt -> eu-central-1).
provider "aws" {
  region = "eu-central-1"
}
