# AWS round-trip harness for pd-TF-ASG (virtual-machine-scale-group).
#
# The concrete resources (aws_vpc + aws_subnet + aws_security_group(_rule) +
# aws_launch_template + aws_autoscaling_group) are GENERATED from the canonical
# fixture (../asg-aws.json) by `pyxnet-render`, written next to this file as
# generated.tf. This file only pins the cloud provider + region.
#
# The fixture uses min=1/max=1 with the smallest SKU (Dublin 2vCPU/1GiB ->
# t3.micro) to minimise cost during the real apply/destroy round-trip.
#
# Test flow (SPEC 6):
#   pyxnet-render -fixture ../asg-aws.json -provider aws -component network         >  generated.tf
#   pyxnet-render -fixture ../asg-aws.json -provider aws -component security-group  >> generated.tf
#   pyxnet-render -fixture ../asg-aws.json -provider aws -component scale-group     >> generated.tf
#   AWS_PROFILE=pyxcloudtest terraform init && terraform plan
#   AWS_PROFILE=pyxcloudtest terraform apply -auto-approve     # gated, real creds
#   AWS_PROFILE=pyxcloudtest terraform destroy -auto-approve   # immediately

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
