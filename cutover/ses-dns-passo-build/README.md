# SES DNS restore — passo.build (bounded DKIM substitution + verification TXT)

One-shot, review-gated Terraform root that converges the **passo.build**
Cloudflare zone to the exact record-set AWS SES requires for the (existing,
currently FAILED) `passo.build` sending identity. Fixes the passobuild realm's
outbound email (`no-reply@passo.build`) on **both** environments — staging
(tf-fase-g catalog, tag `pyx-sso`) and production (platform/infra, tag
`pyx-sso-prod`) share this single SES identity and zone.

## Measured state (2026-10-09, re-measured this session)

| Fact | Value |
|---|---|
| `passo.build` identity (AWS sesv2, acct 533267351655) | `VerificationStatus=FAILED`, `DkimAttributes.Status=FAILED` |
| DKIM tokens | 3, re-measured today via `aws sesv2 get-email-identity`; unchanged since key generation 2026-06-09 |
| Live DNS — 3 DKIM names | **EXIST as placeholder TXT records, content `v=DKIM1; p=`** (empty key; publishes no usable key). Measured live 2026-10-09 via public DNS (`dig TXT <token>._domainkey.passo.build`) for all three tokens. |
| Live DNS — `_amazonses.passo.build` | **ABSENT** (measured live 2026-10-09). |
| `pyxcloud.io` identity | SUCCESS / DKIM SUCCESS — untouched here. |
| Zone | passo.build = `155b6fa434a24bdaba68a60ba87ea134` (Cloudflare NS confirmed via dig) |
| Record IDs | Cloudflare record IDs are fetched READ-ONLY by `make list-records` / `make snapshot` / `make import` (Zone:DNS:Edit-or-Read scoped token, e.g. the pyx-backend `staging` env secret via the pipeline). They are recorded in `snapshots/` for rollback. |

### Evidence correction (supersedes the v1 candidate README)

The v1 README stated the DKIM CNAMEs and verification TXT "were never written
into the Cloudflare zone". **That claim is wrong.** The three DKIM names DO
exist — as placeholder TXT records `v=DKIM1; p=` (written at some earlier
point without the real token key material) — and verification never succeeded
because the published key is empty. `_amazonses.passo.build` is genuinely
absent. Correct claim: *"the DKIM labels were written as empty-key placeholder
TXT records and were never completed with the SES tokens; the verification TXT
was never created."*

### Why v1's "4 creates, additive only" plan is NOT executable

A CNAME cannot coexist with an existing record of another type at the same
name (Cloudflare rejects it). Planning 3 bare CNAME creates against the live
placeholder TXTs produces an apply that fails (or, after any manual delete,
orphaned records). The correct bounded change is a **targeted substitution of
exactly the 3 placeholder TXT records**, plus the missing verification TXT:

```
plan: 3 to destroy, 4 to create, 0 to change.
  destroy  cloudflare_dns_record.ses_dkim["23gjbcgsuuygryvty5fcx4v3zsdoiwwd"]  (imported TXT placeholder)
  destroy  cloudflare_dns_record.ses_dkim["4gwhui5shwet3g7ncxlaxq6resw2r2xe"]  (imported TXT placeholder)
  destroy  cloudflare_dns_record.ses_dkim["j666y3ommaeljmg6nw6up763vxcv7z3g"]  (imported TXT placeholder)
  create   cloudflare_dns_record.ses_dkim[... ×3]   CNAME -> <token>.dkim.amazonses.com
  create   cloudflare_dns_record.ses_verification   TXT  -> rGBksqVMeg2iF/... (re-measure)
```

Nothing else in the zone is visible to this root: no apex TXT (SPF
`v=spf1 -all` untouched), no MX, no other TXT/records. SES envelope MAILFROM
falls back to amazonses.com (SPF-passes); DMARC alignment comes from Easy-DKIM
`d=passo.build` once these CNAMEs verify.

## State ownership (why "no backend" is safe here, and the guards)

This root is deliberately one-shot with **no backend**. The repeat danger of a
no-backend root (lost state → orphaned records; re-apply → duplicate-create
failures) is closed by four guards:

1. `make snapshot` — immutable before-snapshot (`snapshots/dns-before-*.json`)
   of the exact record IDs/types/names/contents in scope. This is the rollback
   source of truth.
2. `make import` — read-only guard that imports the 3 live placeholder TXT
   records into the same resource addresses (`cloudflare_dns_record.ses_dkim`),
   so the plan is a same-address replacement and a partial apply can be
   resumed idempotently. Any non-TXT/CNAME record at a label is a hard veto.
3. `make plan-check` — apply veto gate: the saved plan must contain exactly
   3 destroys + 4 creates + 0 updates, and touch only this root's resources.
4. `make postverify-destroy` — once SES reports SUCCESS, the local state is
   emptied on purpose (refuses to run before SUCCESS), handing ongoing record
   ownership to the canonical DNS-owning pipeline (platform/pyx-backend
   infrastructure, remote Spaces state). This root must never be re-applied
   after that; it is done.

## Apply protocol (fail-closed, in order)

1. **Preconditions**: root review of this branch approved; no other DNS change
   in flight for the zone; `CLOUDFLARE_API_TOKEN` = Zone:DNS:Edit on
   passo.build (the pyx-backend `staging` env secret via the pipeline, or a
   local equivalent); AWS creds for re-measurement.
2. `make re-measure` (mandatory, every apply): if SES rotated the DKIM keys,
   use the NEW tokens — a snapshot/plan made against stale tokens is void.
3. `make snapshot` — before-snapshot (record IDs/types/names/contents).
4. `make init && make import` — provider init + import guard.
5. `make plan && make plan-check` — plan saved + veto gate (3 destroy +
   4 create; ANY other action is a veto). The plan output is the root-review
   artifact.
6. **Root review of the saved plan** (before apply, by the owner).
7. `make apply` — the single write.
8. `make verify` — `VerificationStatus=SUCCESS`, `DkimAttributes.Status=SUCCESS`
   (allow up to 72h; typically <10 min). Then trigger a real email from a
   staging flow — do NOT use `sesv2 send-email`/test emails, and do NOT email
   any real recipient unless a human explicitly authorizes that exact
   recipient.
9. `make postverify-destroy` — empty local state, records stay live.

## Rollback

`make snapshot` output is the contract: recreate each in-scope record with its
exact original id/type/name/content (placeholder TXTs are
`TXT "v=DKIM1; p="` at the three token labels; `_amazonses` was absent, so
rollback for it is deletion). Do it via the Cloudflare dashboard or a
read-only-verified `curl -X POST` per record; never bulk-delete. Rollback
restores the pre-existing FAILED state; no other record is touched.

## Explicitly out of scope

SPF/MX edits, SES MailFrom-domain setup, suppression-list changes, sending
test/seed emails, IAM/SMTP credential rotation, realm SMTP rewrites (the SMTP
PORT fix is the companion single-sign-on reconcile — no realm import), any
production deploy or activation, `terraform destroy` of records this root did
not create.
