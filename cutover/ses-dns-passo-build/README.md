# SES DNS restore — passo.build (DKIM CNAMEs + verification TXT)

One-shot, review-gated Terraform root that converges the **passo.build** Cloudflare
zone to the exact record-set AWS SES requires for the (already existing, currently
FAILED) `passo.build` sending identity. Fixes the passobuild realm's outbound
email (`no-reply@passo.build`) on **both** environments at once — staging
(tf-fase-g catalog, tag `pyx-sso`) and production (platform/infra, tag
`pyx-sso-prod`) share this single SES identity and zone.

## Measured state (2026-10-09, AWS sesv2 / account 533267351655)

| Fact | Value |
|---|---|
| `passo.build` identity | `VerificationStatus=FAILED`, `DkimAttributes.Status=FAILED`, `ErrorType=HOST_NOT_FOUND` (last checked 2026-06-12) |
| DKIM tokens | 3, see `terraform.tfvars.example` (re-measure before apply) |
| Live DNS | no CNAME at any `<token>._domainkey.passo.build`, no `_amazonses.passo.build` TXT |
| `pyxcloud.io` identity | SUCCESS / DKIM SUCCESS (prod `no-reply@pyxcloud.io` sender unaffected, untouched) |
| Zone | passo.build = `155b6fa434a24bdaba68a60ba87ea134` (Cloudflare NS confirmed via dig) |
| Account quota | `Max24HourSend=50000`, `SentLast24Hours=0` (not used as evidence of breakage) |

Second measured blocker (independent of DNS): from the staging droplet, outbound
TCP to `email-smtp.eu-west-1.amazonaws.com` on **587/465/25 is BLOCKED**, while
SES alternate ports **2587/2465 are OPEN** (probed twice via the read-only
`sso-logs` workflow, 2026-10-09 14:24 + 14:41 UTC). `KC_SMTP_PORT=587` therefore
cannot connect even with perfect DNS — the catalog unit must use **2587**
(STARTTLS unchanged). Companion patches: `internal/catalog/platform_bootstrap_sso_do.go`
(this repo, staging) and `platform/infra user_data/sso.sh.tftpl` (production).

## Apply protocol (fail-closed)

1. **Preconditions**: root review of this branch approved; no other DNS change
   in flight for the zone.
2. **Re-measure (mandatory, every apply)**:
   ```bash
   aws sesv2 get-email-identity --region eu-west-1 --email-identity passo.build \
     --query '{Dkim:DkimAttributes.Tokens,Status:DkimAttributes.Status}'
   aws ses get-identity-verification-attributes --region eu-west-1 \
     --identities passo.build --query 'VerificationAttributes'
   ```
   Paste into `terraform.tfvars`. If tokens changed, use the new ones.
3. **Token scope**: `CLOUDFLARE_API_TOKEN` with **Zone:DNS:Edit on passo.build**
   (the pyx-backend `staging` env secret is the known scoped token).
4. **Plan (no write)**:
   ```bash
   terraform init -backend=false
   terraform plan   # must show exactly: 1 TXT + 3 CNAME creates. ANY destruction is a veto.
   ```
5. **Apply**: `terraform apply` (root contains only these 4 records).
6. **Verify**: `aws sesv2 get-email-identity` → `VerificationStatus=SUCCESS`,
   `DkimAttributes.Status=SUCCESS` (allow up to 72h; typically <10 min). Re-run
   `sso-logs.yml` (staging) and trigger a real email from a staging flow — do
   NOT use `sesv2 send-email`/test emails (guardrail: no test emails).

## Rollback

`terraform destroy` from this root (removes exactly the 4 records it created),
or delete the records in the Cloudflare dashboard. Rollback restores the
pre-existing FAILED state; no other resource is touched.

## Explicitly out of scope

SPF/MX edits, SES MailFrom-domain setup, suppression-list changes, sending
test/seed emails, IAM/SMTP credential rotation, Keycloak realm SMTP rewrites,
any production deploy or activation.
