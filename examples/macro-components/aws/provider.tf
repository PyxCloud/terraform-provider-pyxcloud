# AWS round-trip harness for pd-TF-REST-LAMBDA (the remaining macro components).
#
# The concrete resources are GENERATED from the canonical fixture (../macro.json)
# by `pyxnet-render`, written next to this file as generated.tf. This file pins
# the cloud provider + region and supplies the out-of-band inputs the generated
# macro resources reference by variable (Lambda execution role, secret rotation
# lambda, EKS roles). Those variables are wired by support.tf for the round-trip.
#
# REAL ROUND-TRIP (SPEC §6) — only the CHEAP/FAST types are actually applied:
#   - aws_sqs_queue          (managed-queue)
#   - aws_secretsmanager_secret (secrets-manager, no rotation)
#   - aws_route53_zone       (dns-zone)
#   - aws_lambda_function    (serverless, tiny inline zip)
# EXPENSIVE/SLOW types are PLAN-ONLY (never real-applied):
#   - aws_elasticache_replication_group (cache)   — slow to create (~10 min)
#   - aws_kinesis_stream     (event-streaming)    — fine to plan; applied? no, kept plan-only for cost parity
#   - aws_cloudfront_distribution (cdn)           — ~15-40 min to deploy
#   - aws_wafv2_web_acl      (waf)                 — plan-only
#   - aws_eks_cluster        (managed-kubernetes)  — ~15 min, costly
#
# Region MUST equal the catalog-resolved csp_region for the fixture region
# (Frankfurt -> eu-central-1).

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
    archive = {
      source  = "hashicorp/archive"
      version = "~> 2.0"
    }
  }
}

provider "aws" {
  region = "eu-central-1"
}
