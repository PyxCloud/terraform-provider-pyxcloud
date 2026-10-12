# ──────────────────────────────────────────────────────────────────────────
# SES outbound-email DNS restore — passo.build (shared by staging + prod)
#
# REWORK v2 (2026-10-09). Corrections vs the v1 candidate, all measured:
#
#  1. The v1 README claimed the DKIM/verification records "were never written
#     into the Cloudflare zone". WRONG. Live measurement 2026-10-09 (public
#     DNS, dig, all three labels) shows the three
#     `<token>._domainkey.passo.build` names DO exist — as PLACEHOLDER TXT
#     records with content `v=DKIM1; p=` (empty key, publishes no usable key,
#     which is exactly why SES verification is FAILED/HOST_NOT_FOUND for
#     DKIM). `_amazonses.passo.build` does NOT exist. The tokens currently
#     published by SES (re-measured 2026-10-09 via sesv2 get-email-identity,
#     account 533267351655, key generation 2026-06-09) match the v1 tfvars
#     example, so the placeholder TXT labels are the same three labels we
#     must serve as CNAMEs.
#  2. Therefore the v1 "additive only, 4 creates" plan is NOT executable: a
#     CNAME cannot coexist with the existing TXT at the same name
#     (Cloudflare enforces record-name/type uniqueness for CNAMEs), and the
#     3 CNAME creates would fail at apply. The correct bounded change is a
#     TARGETED SUBSTITUTION of exactly those 3 placeholder TXT records
#     (destroy + create at the SAME resource address, via import), plus the
#     1 missing `_amazonses` verification TXT create:
#         plan: 3 to destroy, 4 to create, 0 to change, 0 to replace-other.
#     Nothing else in the zone is touched: no SPF/MX/apex TXT, no other TXT,
#     no arbitrary deletion.
#  3. State ownership: this root has NO backend (one-shot, review-gated).
#     That is safe for a single apply but dangerous on REPEAT (a lost state
#     would orphan the live records; a second apply would try to create
#     duplicates). Guarded here by (a) mandatory `terraform import` of the 3
#     live placeholder TXT records into the same addresses the CNAMEs now
#     occupy, (b) a committed before-snapshot (make snapshot), (c) a
#     plan-action-count gate (make plan-check: exactly 3 destroy + 4 create),
#     and (d) a mandatory post-verification + `make postverify-destroy`
#     that empties the local state on purpose once SES reports SUCCESS,
#     handing ongoing ownership to the canonical DNS-owning pipeline
#     (platform/pyx-backend infrastructure, remote Spaces state).
#
# Why (measured 2026-10-09, AWS sesv2, account 533267351655):
#   - identity `passo.build`: VerificationStatus = FAILED, DKIM Status =
#     FAILED (re-measured today; tokens unchanged since 2026-06-09).
#   - The passobuild realm sends From no-reply@passo.build on BOTH the
#     staging (tf-fase-g catalog, tag pyx-sso) and production (platform/infra,
#     tag pyx-sso-prod) Keycloaks; one shared SES identity/zone, so ONE
#     record-set fixes both environments.
#   - Independent second blocker (not fixed by DNS): droplet egress to SES
#     on 587/465/25 is BLOCKED (DO outbound filtering), 2587/2465 OPEN
#     (sso-logs run 37945829949, f401e411, 2026-10-09 14:41 UTC). Companion
#     patches: tf-fase-g internal/catalog/platform_bootstrap_sso_do.go
#     (staging, future renders) and platform/infra user_data/sso.sh.tftpl
#     (prod, future rolls); the EXISTING units are reconciled in place via
#     the single-sign-on sso-smtp-port-reconcile workflow (no droplet
#     recreation, no auth-flow reset).
#
# Scope guardrails (fail-closed):
#   - EXACTLY the 3 DKIM names + `_amazonses.passo.build`. The substitution
#     is keyed on the exact SES token labels; any other record is invisible
#     to this root. Apex TXT `v=spf1 -all` untouched; SES envelope MAILFROM
#     falls back to amazonses.com which SPF-passes, DMARC alignment comes
#     from Easy-DKIM d=passo.build.
#   - No MailFrom-domain mutation, no suppression changes, NO test email
#     from Terraform.
#   - Zone id is an INPUT (fail-closed empty). Tokens are re-measured
#     against `aws sesv2 get-email-identity` immediately before apply.
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
  # Deliberately NO backend: one-shot, review-gated, state-emptied after
  # post-verification (see header note 3). Repeat-danger guarded by
  # make snapshot / plan-check / postverify-destroy.
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

# TARGETED SUBSTITUTION. One address per token label. If a placeholder TXT
# exists at the label (it does, live-measured 2026-10-09), it is `terraform
# import`-ed into THIS address before plan (see README step 4) and the plan
# then shows a single replacement: destroy the placeholder TXT, create the
# CNAME. If a label has no record at all, the same address is a plain create.
# Either way the plan for this root is bounded to 3 destroy + 4 create and
# make plan-check enforces it.
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
    precondition {
      # Fail-closed against typos/rotated tokens: a label must be a bare
      # lowercase DNS label (never a name, never a value).
      condition     = can(regex("^[a-z0-9]{20,64}$", each.value))
      error_message = "DKIM token label must be a bare lowercase alnum label; re-measure from aws sesv2 get-email-identity."
    }
  }
}

output "records_planned" {
  description = "Exact record-set this root will converge the zone to (readable in plan; no secrets — DKIM tokens and the verification token are public DNS values by definition)."
  value = concat(
    [{ name = "_amazonses.passo.build", type = "TXT", action = "create (absent live, measured 2026-10-09)" }],
    [for t in local.tokens : { name = "${t}._domainkey.passo.build", type = "CNAME", content = "${t}.dkim.amazonses.com", action = "substitute existing placeholder TXT v=DKIM1; p= (measured live)" }]
  )
}
