# Runbook: staging-fe shim (replaces the obs-box FE shim) — pd-STAGING-FE-SHIM

> **Status: DO-NOT-APPLY.** Every command below is prepared for review. Nothing
> has been run. This PR is IaC + scripts + this runbook only — no droplet was
> created, no secret was written, no DNS/edge config was touched.

## Why

`staging.passo.build` (VPN-only; the `pyx-edge` SNI routers pass its `:443`
straight through to whatever is listening on `10.0.1.7:443`) keeps going down
(`ERR_CONNECTION_CLOSED`). That IP is the **`obs` droplet** — the observability
aggregator's box (`platform_bootstrap_obs_do.go`, tag `pyx-obs`, CPU 4 / RAM 8)
— which someone has hand-SSH'd a Next.js-adjacent shim onto, outside any
pipeline. There is no committed source for what runs there today (see
"Investigation notes" below); if it ever tries to `next build`/`next start` on
that box, it competes for RAM with the obs aggregator and can OOM both.

## What this PR adds

A **dedicated, stateless** `staging-fe` droplet-autoscale-of-1
(`internal/catalog/do_baseline.go` + `platform_bootstrap_staging_fe_do.go`):
nginx ONLY, reverse-proxying to the **existing** Amplify staging branch
(`https://staging.de9vejckwo4b9.amplifyapp.com`, app `de9vejckwo4b9`,
`eu-west-1`) and injecting that branch's Basic-Auth credential (it has
`enableBasicAuth: true`) as an upstream `Authorization` header, fetched from DO
Vault at **boot time** (never inlined, never a render-time Terraform variable
carrying the credential value). It is firewalled to accept `:443` ONLY from
the `pyx-edge` tag — never `0.0.0.0/0` — unlike the shared baseline firewall
every other service uses.

## Step 0 — one-time Vault + AppRole setup (NOT in this PR's terraform; run by
a human with Vault admin access)

```
# 1. Read the CURRENT Amplify staging-branch Basic-Auth credential (base64
#    user:pass) — do NOT print it to a log/terminal that gets archived.
aws amplify get-branch --app-id de9vejckwo4b9 --branch-name staging --region eu-west-1 \
  --query 'branch.basicAuthCredentials' --output text
# (If this returns empty/null, the branch doesn't have Basic-Auth creds set —
#  check `enableBasicAuth` and set credentials via `aws amplify update-branch`
#  first; that step is outside this PR's scope.)

# 2. Write it to DO Vault at the canonical secret/infra/<env>/<service> leaf
#    (see do-vault-canonical-secrets memo) — key name matches
#    StagingFEDOBootstrapSpec.VaultKey default "basic_auth_b64":
vault kv put secret/infra/staging/staging-fe/amplify-basic-auth \
  basic_auth_b64='<value from step 1, verbatim>'

# 3. Create a read-only AppRole scoped to ONLY that leaf (mirrors the
#    `observability-boot` AppRole pattern already in place for obs):
vault policy write staging-fe-boot - <<'HCL'
path "secret/data/infra/staging/staging-fe/amplify-basic-auth" {
  capabilities = ["read"]
}
HCL
vault auth enable approle            # if not already enabled
vault write auth/approle/role/staging-fe-boot \
  token_policies="staging-fe-boot" token_ttl=15m token_max_ttl=30m secret_id_ttl=0
vault read auth/approle/role/staging-fe-boot/role-id            # -> staging_fe_vault_role_id
vault write -f auth/approle/role/staging-fe-boot/secret-id       # -> staging_fe_vault_secret_id
```

`vault_addr`, `staging_fe_vault_role_id`, `staging_fe_vault_secret_id` are the
three Terraform variables the rendered bootstrap references
(`${var.staging_fe_vault_role_id}` etc. — see
`DOBaselineVariableNames()`/`StagingFEDOBootstrapVariableNames()`). Supply them
at apply time the same way every other boot-fetching service's AppRole
variables are supplied (never committed).

## Step 1 — provision the staging-fe droplet (human-gated apply)

The IaC lives in `internal/catalog/do_baseline.go`
(`DOBaselineServices()` now has a 7th entry: `{Name: "staging-fe", Tag:
"pyx-staging-fe", CPU: 1, RAM: 2}`) and its own dedicated firewall (step "2b"
in `AssembleDOBaseline`). Render with `DOBaselineOptions.FullServiceBootstraps
= true` (the same flag that already turns on the real per-service bootstraps
for obs/sast/backend/vpn — see the `DO_FULL_SERVICE_BOOTSTRAPS=1` harness flag)
so the droplet gets the real nginx-to-Amplify bootstrap, not a bare box.

```
cd terraform-provider-pyxcloud/cutover
# render (see cutover/README.md for the exact render invocation + how secrets
# are threaded); confirm the plan shows ONLY additions:
#   + digitalocean_droplet_autoscale.staging-fe
#   + digitalocean_firewall.passo-do-baseline-staging-fe-sg
#   + variable.staging_fe_vault_role_id / .staging_fe_vault_secret_id
terraform plan   # review — 0 changes to any EXISTING resource (obs untouched)
terraform apply  # human-gated; NOT run as part of this PR
```

Do not run this against the live staging state until a human has reviewed the
plan and confirmed it is additive-only.

## Step 2 — verify the shim BEFORE touching the public hostname

The new droplet is VPN-only and not yet in the edge's SNI map, so reach it the
same way `core/scripts/pyx-debug.sh` reaches the obs box today (tunnel via the
`pyx-edge` bastion, which is already allowed to SSH any staging-VPC droplet):

```
STAGING_FE_IP="$(doctl compute droplet list --tag-name pyx-staging-fe --format PrivateIPv4 --no-header)"
EDGE="$(doctl compute droplet list --tag-name pyx-edge --format PublicIPv4 --no-header | head -1)"
ssh -f -N -L 8444:"$STAGING_FE_IP":443 root@"$EDGE"
curl -sk https://localhost:8444/ -o /dev/null -w '%{http_code}\n'
#   -> expect a 2xx/3xx (the Amplify staging branch's actual page), NOT a 401
#      (401 would mean the injected Authorization header is missing/wrong —
#      check the staging-fe-boot AppRole and the Vault leaf from Step 0) and
#      NOT a connection failure (would mean nginx isn't up — check
#      `journalctl -u nginx` via `pyx-debug sh <staging-fe-ip>` once :22 from
#      pyx-edge is confirmed reachable).
curl -sk https://localhost:8444/some/known/staging/route -o /dev/null -w '%{http_code}\n'
pkill -f "8444:${STAGING_FE_IP}:443"   # close the tunnel
```

Only proceed to Step 3 once this returns the expected Amplify page content
(check body, not just status code — a misconfigured `Host`/SNI can return a
DIFFERENT Amplify app's default page with a 200).

## Step 3 — repoint the edge (gated script)

```
cd terraform-provider-pyxcloud/examples/one-lb-consolidation
./add-staging-fe-route.sh
# resolves the staging-fe private IP + both pyx-edge public IPs via doctl,
# prompts for an explicit 'yes', then idempotently rewrites nginx.conf's
# `stream {}` map + upstream on BOTH edge routers (nginx -t gates the reload —
# a bad config is never activated). Mirrors add-api-prod-route.sh exactly.
```

Then verify over the VPN (staging.passo.build is VPN-only, so this must be run
from a VPN-connected host, e.g. via headscale):

```
curl -sk --resolve staging.passo.build:443:188.166.192.241 \
  https://staging.passo.build/ -o /dev/null -w '%{http_code}\n'
```

If anything looks wrong, the script prints a one-line rollback (repoint back
to `10.0.1.7`, the obs-box shim) at the end of its output — the obs box is
NOT touched or decommissioned until this step is confirmed good.

## Step 4 — decommission the obs-box shim role

Only after staging.passo.build has been observed stable on the new shim for a
reasonable soak period (propose: a few days, or per the owner's judgement):

1. SSH the obs droplet (`pyx-debug sh <obs-ip>` or directly, since it is
   already `:22`-reachable from the `pyx-edge` tag) and inventory what is
   actually running there on the FE's behalf (there is no committed source —
   expect an ad hoc `pm2`/systemd unit, an nginx server block, or a bare
   `next start` process; check `systemctl list-units`, `pm2 ls` if present,
   `ss -tlnp` for anything on :443/:3000, and `crontab -l`/`/etc/cron.d` for
   any respawn hack).
2. Stop and disable whatever that inventory finds (`systemctl disable --now
   <unit>` / `pm2 delete <name>` / remove the nginx server block for the old
   FE route and `nginx -t && systemctl reload nginx`) — leave the
   observability aggregator itself (`observability.service`) untouched.
3. Confirm `https://staging.passo.build` still serves correctly (edge is
   already pointed at the NEW shim since Step 3, so this should be a no-op
   from the outside — this step only frees the obs box's RAM/attack surface).
4. Update `observability/.github/workflows/deploy-observability.yml`'s header
   comment (currently silent on the FE role) and any memory/runbook that still
   references the obs box as the staging FE — nothing to change in this repo,
   listed here for completeness.

## Uncertainty / open questions

- **What exactly runs on the obs box today** could not be determined from the
  repo (no committed source, no deploy workflow references it — see
  "Investigation" in the PR description). Step 4 above budgets time to
  discover and remove it by hand; it may turn out to be a `pm2` process, a
  `next start` under systemd, or something stranger. Confirm before assuming
  a one-line `systemctl disable`.
- **Amplify redirects/cookies**: the nginx config rewrites the ONE redirect
  case nginx's `proxy_redirect` handles declaratively (an absolute
  `Location: https://staging.de9vejckwo4b9.amplifyapp.com/...` header) and
  the `Set-Cookie` domain via `proxy_cookie_domain`. If the Next.js app on
  Amplify issues **relative** redirects (the common case) neither rewrite is
  needed and both directives are no-ops — harmless either way. If it uses
  `NextResponse.redirect(new URL(...))` with an **absolute** URL built from
  its own request host, the redirect may already resolve correctly relative
  to what the browser sent (`Host: staging.passo.build`, forwarded correctly)
  — but this should be confirmed empirically in Step 2 by exercising an
  actual redirecting route (e.g. an auth callback), not assumed from reading
  the nginx config alone.
- **Amplify IP stability**: the `resolver ... valid=60s` + variable
  `proxy_pass` avoids nginx caching a stale Amplify edge IP for the process
  lifetime, but has not been load-tested against Amplify's actual DNS TTL/
  rotation behaviour — worth watching in the soak period.
- **staging-fe sizing** (CPU 1 / RAM 2, matching the `pyx-edge` droplets) is a
  guess for "nginx reverse-proxy only, no build" — should be plenty, but bump
  if the watchdog/health-gate ever shows resource pressure.
