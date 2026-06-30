# ADR 0001 — Email (SES) stays cross-cloud in the AWS→DigitalOcean migration

- Status: **Accepted**
- Context task: `pd-MIG-B3-SES-CROSSCLOUD-DECISION` (P2)
- Supersedes: the open question in `docs/AWS-TO-DO-GAP.md` §B3

## Context

The AWS→DO migration reproduces every platform service as a DO-native resource or an
operator-pattern replacement (see `docs/AWS-TO-DO-GAP.md`). **Email (SES)** is the one
service with no DigitalOcean equivalent: SES is a global API, not a region- or
cloud-coupled resource, and passo.build already calls it directly.

## Decision

Keep email on **AWS SES** as a **deliberate cross-cloud dependency** after the DO cutover.
SES is not migrated and not mitigated to a degraded single-VM SMTP relay. A SaaS sender
(Postmark / Resend / SendGrid) behind the `email-service` type remains an available swap if
a fully AWS-free posture is later required, but is **out of scope** for the cutover.

## Consequences

- "100% off AWS" is **not** a goal of the cutover; the residual AWS surface is exactly one
  global, stateless API (SES) with no data-gravity and no region coupling.
- The migration planner must treat `email-service` / `email` (`ses.go`) as an **accepted
  cross-cloud exception**, not a defect — it is advisory, never a cutover blocker.
- This is recorded so the gap is a ratified decision, not an oversight.
