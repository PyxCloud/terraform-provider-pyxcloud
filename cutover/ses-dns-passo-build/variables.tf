variable "cloudflare_zone_passo" {
  description = "Cloudflare zone id for passo.build (non-secret; resolvable from any DNS:Read-scoped token). Fail-closed: empty = resources refuse to render."
  type        = string
}

variable "ses_verification_token" {
  description = "AWS SES domain-identity verification token for passo.build (public DNS value, not a secret). Measured 2026-10-09 from `aws ses get-identity-verification-attributes --identities passo.build`."
  type        = string
}

variable "ses_dkim_tokens" {
  description = "The 3 Easy-DKIM CNAME token labels for passo.build. Measured 2026-10-09 via `aws sesv2 get-email-identity --email-identity passo.build --query DkimAttributes.Tokens`. Re-measure before every apply."
  type        = list(string)
}

variable "ses_dns_ttl" {
  description = "TTL for the SES verification/DKIM records (canonical: 60, matching the existing SES DNS record pattern)."
  type        = number
  default     = 60
}
