# ──────────────────────────────────────────────────────────────────────────
# SES outbound-email DNS restore — passo.build (shared by staging + prod)
#
# Why (measured 2026-10-09, AWS sesv2, account 533267351655):
#   - identity `passo.build`: VerificationStatus = FAILED, DKIM Status = FAILED,
#     ErrorType = HOST_NOT_FOUND (last checked 2026-06-12) — the three Easy-DKIM
#     CNAMEs and the `_amazonses` verification TXT were never written into the
#     Cloudflare zone (verified live: no CNAME/TXT at any `<token>._domainkey`,
#     no `_amazonses` TXT).
#   - identity `pyxcloud.io`: SUCCESS / DKIM SUCCESS — untouched here.
#   - The passobuild realm sends From no-reply@passo.build on BOTH the staging
#     (tf-fase-g catalog, tag pyx-sso) and production (platform/infra, tag
#     pyx-sso-prod) Keycloaks; one shared SES identity/zone, so ONE record-set
#     fixes both environments.
#
# Scope guardrails (fail-closed, per D4 / measured-prereqs policy):
#   - Additive ONLY: 1 TXT + 3 CNAMEs. No SPF/MX rewrite (apex TXT `v=spf1 -all`
#     is untouched; SES envelope MAILFROM falls back to amazonses.com which SPF-
#     passes, and DMARC alignment comes from Easy-DKIM d=passo.build once these
#     CNAMEs verify). No MailFrom-domain mutation, no suppression changes, no
#     test email from Terraform.
#   - Zone id is an INPUT (fail-closed empty). Tokens are re-measured against
#     `aws sesv2 get-email-identity` immediately before apply (see README).
#   - This root has NO backend and NO other resources; records only.
#
# Apply pattern per docs/cutover/CLOUDFLARE-CUTOVER.md Option A:
#   CLOUDFLARE_API_TOKEN=<Zone:DNS:Edit on passo.build> terraform apply
# ──────────────────────────────────────────────────────────────────────────

terraform {
  required_providers {
    # Pinned to the same version line the live DNS-owning pipeline uses
    # (platform/pyx-backend infrastructure/do-backend/providers.tf).
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 5.0"
    }
  }
  # Deliberately NO backend: a one-shot, review-gated record-set change.
}

provider "cloudflare" {
  # v5 reads CLOUDFLARE_API_TOKEN from the environment. No var/default here so
  # a run without a token fails loudly instead of no-op'ing.
}

locals {
  tokens = [for t in var.ses_dkim_tokens : trimspace(t)]
}

resource "cloudflare_dns_record" "ses_verification" {
  zone_id = var.cloudflare_zone_passo
  name    = "_amazonses.passo.build"
  type    = "TXT"
  content = var.ses_verification_token
  proxied = false
  ttl     = var.ses_dns_ttl

  lifecycle {
    precondition {
      condition     = trimspace(var.cloudflare_zone_passo) != ""
      error_message = "cloudflare_zone_passo must be set (fail-closed: no zone, no write)."
    }
    precondition {
      condition     = trimspace(var.ses_verification_token) != ""
      error_message = "ses_verification_token must be re-measured from aws ses get-identity-verification-attributes before apply."
    }
  }
}

resource "cloudflare_dns_record" "ses_dkim" {
  for_each = toset(local.tokens)

  zone_id = var.cloudflare_zone_passo
  name    = "${each.value}._domainkey.passo.build"
  content = "${each.value}.dkim.amazonses.com"
  type    = "CNAME"
  proxied = false
  ttl     = var.ses_dns_ttl

  lifecycle {
    precondition {
      condition     = trimspace(var.cloudflare_zone_passo) != ""
      error_message = "cloudflare_zone_passo must be set (fail-closed: no zone, no write)."
    }
    precondition {
      condition     = length(local.tokens) == 3
      error_message = "SES Easy DKIM always publishes exactly 3 CNAMEs; re-measure ses_dkim_tokens from aws sesv2 get-email-identity."
    }
  }
}

output "records_planned" {
  description = "Exact record-set this root will converge the zone to (readable in plan; no secrets)."
  value = concat(
    [{ name = "_amazonses.passo.build", type = "TXT" }],
    [for t in local.tokens : { name = "${t}._domainkey.passo.build", type = "CNAME", content = "${t}.dkim.amazonses.com" }]
  )
}
