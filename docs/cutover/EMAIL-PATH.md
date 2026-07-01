# AWS → DigitalOcean cutover: the transactional-email path (GAP-2)

`pd-MIG-CUTOVER-F1-05` (EPIC-AWS-TO-DO-MIGRATION) — resolves **BESPOKE GAP-2**.

## What consumes email

The platform sends **transactional** email only (no marketing/bulk):

- **invites** — team/tenant invitations (SSO / console).
- **passkey / step-up** — passkey enrolment + step-up verification mails.
- **notifications** — operational + billing notifications.

All of it is sent from the verified sending domain **`passo.build`**. Historically
this was **AWS SES** (`aws_ses_domain_identity` + `aws_ses_domain_dkim`), modelled
as the canonical `email` component (`email-sender`).

## The problem

DigitalOcean has **no managed transactional-email primitive**. Before F1-05 the
`email` component (`internal/catalog/ses.go`) was **AWS-only by design** and
**hard-errored** on any non-AWS provider, so the DO target estate had to *exclude*
email entirely (a documented bespoke gap). Post-cutover the DO compute still needs
a working way to send `passo.build` mail.

## The decision: keep AWS SES cross-cloud (default), 3rd-party relay opt-in

We **keep AWS SES as a cross-cloud dependency** as the default, consumed by the DO
compute over **SMTP**, and make the abstract `email` component render an
**SMTP-relay config** on DO instead of hard-erroring.

Rationale:

- **SES is region-global and reachable from anywhere.** SES SMTP
  (`email-smtp.<region>.amazonaws.com:587`, STARTTLS) is a public endpoint; DO
  compute reaches it with **IAM SMTP credentials** exactly like AWS compute would.
- **The verified sending domain does not move.** `passo.build`'s SES identity +
  DKIM stay exactly as they are — **no re-verification, no deliverability reset**,
  no reputation warm-up. This is the single biggest reason to keep SES.
- **Smallest blast radius.** Email is decoupled from the compute substrate: the
  cutover swaps *where the app runs*, not *how mail is sent*. Only a credential and
  an endpoint change on the app side.
- **3rd-party is a config flip, not a rebuild.** If we later want off AWS entirely,
  the same component renders a **SendGrid / Postmark / Mailgun** relay by setting
  `RelayHost` + `CredentialsRef` — no code change.

### Tradeoffs

| Path | Pros | Cons |
| --- | --- | --- |
| **AWS SES cross-cloud (chosen default)** | zero deliverability disruption; domain + DKIM unchanged; tiny change surface; cheapest | keeps a residual AWS account/IAM dependency post-cutover (SES only) |
| **3rd-party relay (SendGrid/Postmark/Mailgun)** | fully off AWS; opt-in via config | new vendor + billing; **must re-do SPF/DKIM/DMARC** for the new sender; deliverability warm-up |
| ~~Self-host SMTP on a DO droplet~~ (rejected) | no external dep | DO blocks/► throttles outbound :25; awful deliverability; you own IP reputation + blocklists — never do this for prod transactional mail |

The previous behaviour (a single-VM `bytemark/smtp` mitigation droplet on DO) is
**removed** for exactly the rejected-row reasons.

## What the component renders now

`internal/catalog/ses.go`:

- **AWS** → native SES: `aws_ses_domain_identity` + `aws_ses_domain_dkim` + a DKIM
  tokens `output` (unchanged).
- **DigitalOcean (any non-AWS)** → an **SMTP-relay config**: a terraform `locals`
  block + an `output`, describing the relay the compute consumes. **No managed
  cloud resource, no inline secret.** Example (DO render of `email-sender`):

  ```hcl
  locals {
    email-sender_smtp = {
      sending_domain  = "passo.build"
      relay_host      = "email-smtp.eu-west-1.amazonaws.com"
      relay_port      = 587
      starttls        = true
      credentials_ref = "email-sender-smtp-credentials"
    }
  }

  output "email-sender_smtp_relay" {
    description = "SMTP-relay config for the DO compute (host/port/creds-ref, no secrets)"
    value       = local.email-sender_smtp
  }
  ```

`email` is now marked **natively supported on DigitalOcean** in the mitigation
matrix (`internal/catalog/mitigation.go`), so it takes the native SMTP-relay render
instead of the degraded single-VM fallback — mirroring the B1 (RabbitMQ operator),
B2 (Cloudflare WAF) and B4 (Vault-HA) alias precedents.

## What the DO compute needs (config, not secrets)

The app reads these at deploy time; secrets are resolved from the secrets manager
(**Vault-HA** on DO, the `secrets-manager` alias):

| Setting | Default value | Notes |
| --- | --- | --- |
| SMTP host | `email-smtp.eu-west-1.amazonaws.com` | AWS SES SMTP, mirrors the prod region (eu-west-1) |
| SMTP port | `587` | STARTTLS submission (SES also allows 2587/465) |
| STARTTLS | `true` | required |
| Credentials ref | `email-sender-smtp-credentials` | **reference**, not a secret — a Vault path holding the SES SMTP user + password |
| From / sending domain | `passo.build` | the SES-verified domain |

**Generating the SES SMTP credentials:** create an IAM user with `ses:SendRawEmail`
(scoped to the `passo.build` identity), then derive its SMTP username/password from
the IAM access key (SES SMTP password = HMAC-derived from the secret key). Store
BOTH under the Vault path referenced by `credentials_ref`. Never commit them; the
terraform render only ever carries the *reference*.

To switch to a 3rd-party relay, set on the `email` component:
`RelayHost = "smtp.sendgrid.net"` (or postmark/mailgun), `RelayPort`, and
`CredentialsRef = "<vault path for that vendor>"`.

## DNS records (SPF / DKIM / DMARC) — stay in Cloudflare

DNS for `passo.build` is authoritative in **Cloudflare** and does **not** move with
the compute. As long as we keep AWS SES (chosen default), the existing records
**stay exactly as-is** — this is why deliverability is undisturbed:

- **DKIM** — the 3 SES CNAME records (`<token>._domainkey.passo.build →
  <token>.dkim.amazonses.com`) **stay**. These are the SES DKIM tokens surfaced by
  the AWS render's `*_dkim_tokens` output.
- **SPF** — the TXT record must authorise the SES sender:
  `v=spf1 include:amazonses.com ~all` (keep as-is). SPF authorises the *sender*, so
  it is unaffected by the compute moving to DO — the relay is still SES.
- **DMARC** — `_dmarc.passo.build` TXT (`v=DMARC1; p=...; rua=...`) **stays**.
- **MAIL FROM** (if a custom MAIL-FROM subdomain is configured) — its MX + SPF
  records **stay**.

**If (and only if) we later move to a 3rd-party relay**, these records must be
**re-done for the new vendor**: new DKIM CNAMEs, add the vendor to SPF
(`include:sendgrid.net` etc.), keep DMARC. Plan a deliverability warm-up. This is
the main reason the default keeps SES.

> Cloudflare note: SES-provider CNAMEs (DKIM) and TXT (SPF/DMARC) must be
> **DNS-only** ("grey cloud", not proxied) — Cloudflare's proxy only fronts HTTP.

## Verification

- `go build ./...` + `go vet ./...` — clean.
- `ses_test.go`: DO no longer hard-errors and renders the SMTP-relay config
  (default SES SMTP, creds-ref, no inline secret); 3rd-party relay override honoured.
- `prod_estate_test.go`: `email-sender` is present in **both** renders — SES on AWS,
  SMTP-relay on DO — and the full DO estate passes `terraform init && validate`
  (plan-only, GREEN).
