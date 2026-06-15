# AWS round-trip harness for pd-TF-MDB (managed-database, RDS).
#
# The concrete resources (aws_vpc + aws_subnet + aws_security_group(_rule) +
# aws_db_subnet_group + aws_db_instance) are GENERATED from the canonical fixture
# (../db-aws.json) by `pyxnet-render`, written next to this file as generated.tf.
# This file only pins the cloud provider + region and declares the throwaway DB
# password variable the rendered aws_db_instance references (var.db_password).
#
# COST + DATA-SAFETY NOTE: RDS costs money and takes several minutes to create /
# destroy. The TEST fixture (../db-aws.json) sets the smallest class
# (Frankfurt 2vCPU/1GiB -> db.t3.micro), the 20 GiB minimum storage,
# encrypted=false, and the TEST-ONLY OVERRIDE deletion_protection=false +
# skip_final_snapshot=true so the harness can `terraform destroy` cleanly.
# PRODUCTION fixtures (../db.json) keep the production-safe defaults:
# deletion_protection=true + a final snapshot on destroy. The override is visible
# and test-only — never use it for a real database.
#
# Test flow (SPEC 6):
#   pyxnet-render -fixture ../db-aws.json -provider aws -component network          >  generated.tf
#   pyxnet-render -fixture ../db-aws.json -provider aws -component security-group   >> generated.tf
#   pyxnet-render -fixture ../db-aws.json -provider aws -component managed-database >> generated.tf
#   AWS_PROFILE=pyxcloudtest TF_VAR_db_password=... terraform init && terraform plan
#   AWS_PROFILE=pyxcloudtest TF_VAR_db_password=... terraform apply -auto-approve   # gated, real creds
#   AWS_PROFILE=pyxcloudtest terraform destroy -auto-approve                        # immediately

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

# Throwaway master password for the RDS round-trip. Supplied out-of-band via
# TF_VAR_db_password (never committed). In production this comes from Secrets
# Manager / Vault, rotated, not a Terraform variable.
variable "db_password" {
  type      = string
  sensitive = true
  default   = "ChangeMe-RoundTrip-123!"
}
