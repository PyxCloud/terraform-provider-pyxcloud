# AWS round-trip harness for pd-TF-EC2-VM.
#
# The concrete resources (aws_vpc + aws_subnet + aws_security_group(_rule) +
# aws_instance) are GENERATED from the canonical fixture (../vm-aws.json) by
# `pyxnet-render`, written next to this file as generated.tf. This file only
# pins the cloud provider + region.
#
# Test flow (SPEC 6):
#   pyxnet-render -fixture ../vm-aws.json -provider aws -component network         >  generated.tf
#   pyxnet-render -fixture ../vm-aws.json -provider aws -component security-group  >> generated.tf
#   pyxnet-render -fixture ../vm-aws.json -provider aws -component virtual-machine >> generated.tf
#   AWS_PROFILE=pyxcloudtest terraform init && terraform plan
#   AWS_PROFILE=pyxcloudtest terraform apply -auto-approve    # gated, real creds
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
# (Dublin -> eu-west-1). The instance type and AMI come from the catalog.
provider "aws" {
  region = "eu-west-1"
}
