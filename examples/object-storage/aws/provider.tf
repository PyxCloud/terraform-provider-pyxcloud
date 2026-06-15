# AWS round-trip harness for pd-TF-S3 (object/blob storage, S3).
#
# The concrete resources (aws_s3_bucket + aws_s3_bucket_versioning +
# aws_s3_bucket_public_access_block) are GENERATED from the canonical fixture
# (../storage-aws.json) by `pyxnet-render`, written next to this file as
# generated.tf. This file only pins the cloud provider + region.
#
# SECURITY NOTE (SPEC 5.7): the bucket is PRIVATE BY DEFAULT — public=false in the
# fixture makes pyxnet-render emit the full public-access-block (all four flags
# true), so the bucket can never be made world-readable by an errant ACL/policy.
#
# TEARDOWN NOTE: the TEST fixture (../storage-aws.json) sets the TEST-ONLY override
# force_destroy=true so `terraform destroy` removes the (empty/just-created) bucket
# cleanly. PRODUCTION fixtures (../storage.json) keep force_destroy=false.
#
# Test flow (SPEC 6):
#   pyxnet-render -fixture ../storage-aws.json -provider aws -component object-storage > generated.tf
#   AWS_PROFILE=pyxcloudtest terraform init && terraform plan
#   AWS_PROFILE=pyxcloudtest terraform apply -auto-approve    # gated, real creds
#   aws s3api get-bucket-versioning / get-public-access-block  # verify
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
# (Frankfurt -> eu-central-1).
provider "aws" {
  region = "eu-central-1"
}
