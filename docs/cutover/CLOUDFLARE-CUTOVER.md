# Cloudflare cutover change-set (AWS ALB → DigitalOcean origins)

**pd-MIG-CUTOVER-F4-PREP** — EPIC-AWS-TO-DO-MIGRATION

This is the EXACT Cloudflare change-set for the DNS flip that moves each prod
hostname off the AWS shared ALB and onto its DigitalOcean service origin. It is
**not yet applied** — it is the runbook to execute once the scoped
`CLOUDFLARE_API_TOKEN` is available (see "What still needs the token" at the end).

## Model

The DOKS→droplet pivot removed L7 host-routing. The DO regional LB
(`edge-lb`, `188.166.195.226`) is L4 `tls_passthrough` and **cannot host-route**.
So the cutover model is:

> **Cloudflare terminates public TLS and routes each hostname directly to the
> correct DO service origin droplet, which presents its own cert on :443.**

Each origin droplet runs an **nginx :443 TLS terminator** (self-signed origin
cert) that reverse-proxies to the local plain-HTTP service port. This is the
`obs` droplet's proven pattern, lifted into a reusable catalog building block
(`internal/catalog/edge_tls_terminator.go`) and wired into the cutover renderer
via `DOBaselineOptions.EdgeTLSOrigins` (`DO_EDGE_TLS_ORIGINS=1`).

**SSL/TLS mode: Cloudflare `Full`** (not `Full (strict)`). `Full` accepts the
self-signed origin cert. Upgrade to `Full (strict)` later by swapping the
self-signed cert for a Cloudflare Origin CA cert on each droplet (out of scope
for the flip). `Flexible` (HTTP origin) is explicitly rejected: it would leave
Cloudflare→origin traffic unencrypted over the public internet, and the DO
firewall only opens :443 anyway.

## Per-hostname → DO origin map (authoritative)

Derived from the live AWS shared ALB (`beta-pyx-shared-alb`) host-header rules
and the DO droplet fleet. **Service droplet IPs rotate** — always re-resolve the
current origin IP before applying:

```bash
doctl compute droplet list --tag-name pyx-sso     --format PublicIPv4 --no-header
doctl compute droplet list --tag-name pyx-backend --format PublicIPv4 --no-header
doctl compute droplet list --tag-name pyx-mcp     --format PublicIPv4 --no-header
```

| Hostname | DO service (tag) | Origin IP (at authoring) | Upstream port | CF proxied | CF SSL mode | Notes |
|---|---|---|---|---|---|---|
| `beta-auth.pyxcloud.io` | sso (`pyx-sso`) | `161.35.30.234` | 8080 (Keycloak) | yes (orange) | Full | Issuer host — see KC_HOSTNAME fix below |
| `beta-api.pyxcloud.io` | backend (`pyx-backend`) | `209.38.215.216` | 8080 (Quarkus) | yes (orange) | Full | ALB rule `beta-api` → api_tg:8080 |
| `mcp.passo.build` | mcp (`pyx-mcp`) | `206.81.24.16` | 8787 (MCP) | yes (orange) | Full | ALB rule `mcp.passo.build` → mcp_tg:8787 |
| `passo.build` / `www.passo.build` | marketing/frontend | *(Spaces static, F1-01)* | — | yes | Full/off | **Not on the ALB today** — served by AWS Amplify/CloudFront. Migrate per F1-01 (Spaces static + CF); flip separately. |
| `admin.passo.build` | — | — | — | — | — | **Does not exist** (NXDOMAIN). Keycloak admin is `beta-auth.pyxcloud.io/admin*`, VPN-gated at the ALB. No separate hostname to flip. |
| `observability.*` | obs (`pyx-obs`) | `64.226.105.50` | 8080 (→ nginx :443) | **no (grey / VPN-only)** | — | Internal only. Keep DNS-only / VPN. Do NOT expose publicly. |
| `beta-vault.pyxcloud.io` | vault (not a DO droplet) | — | 8200 | yes | Full (strict) | Vault is not part of the DO droplet fleet; leave on AWS until vault is migrated. |

> **Correction found during prep:** the DO `pyx-sso` droplet's baked
> `KC_HOSTNAME=beta-auth.passo.build` is **wrong**. The live realm issuer is
> `https://beta-auth.pyxcloud.io/realms/passobuild`. Before the flip, the sso
> origin's `KC_HOSTNAME` (and the self-signed cert CN) must be
> `beta-auth.pyxcloud.io`, otherwise tokens issue with the wrong issuer and every
> OIDC consumer breaks. The committed `platform_bootstrap_sso.go` already renders
> the correct value; the drifted DO user_data does not. Fix on the sso re-apply.

## Pre-flip origin readiness (must be GREEN before touching DNS)

Each origin must answer HTTPS with the prod Host header **before** the DNS flip.
Run these `--resolve` probes (they bypass DNS and hit the DO origin directly):

```bash
SSO=161.35.30.234 ; BACKEND=209.38.215.216 ; MCP=206.81.24.16   # re-resolve first!

# SSO — must return 200 with issuer=https://beta-auth.pyxcloud.io/realms/passobuild
curl -sk --resolve beta-auth.pyxcloud.io:443:$SSO \
  https://beta-auth.pyxcloud.io/realms/passobuild/.well-known/openid-configuration | jq .issuer

# Backend — must return 200 (Quarkus health)
curl -sk -o /dev/null -w '%{http_code}\n' --resolve beta-api.pyxcloud.io:443:$BACKEND \
  https://beta-api.pyxcloud.io/q/health

# MCP — must return 200
curl -sk -o /dev/null -w '%{http_code}\n' --resolve mcp.passo.build:443:$MCP \
  https://mcp.passo.build/health
```

If any probe returns `000`/timeout, the :443 terminator is not up on that origin
— re-apply the pool with `DO_EDGE_TLS_ORIGINS=1` (see the DO-side runbook) or fix
the firewall before proceeding.

## The DNS flip (Cloudflare change-set)

Today `beta-auth`, `beta-api`, `mcp.passo.build` are proxied A/CNAME records
pointing at the AWS ALB (`beta-pyx-shared-alb-...elb.amazonaws.com`). The flip
repoints each **proxied** record to its DO origin IP and sets zone SSL to `Full`.

### Option A — terraform (catalog cloudflare provider)

The catalog already has a Cloudflare component
(`internal/catalog/cloudflare.go`, `cloudflare_dns_record`). Render + apply:

```hcl
# cutover-edge.tf — records only; zone_id + token from the environment.
variable "cloudflare_zone_pyxcloud" { type = string }  # pyxcloud.io zone id
variable "cloudflare_zone_passo"    { type = string }  # passo.build zone id

resource "cloudflare_dns_record" "beta_auth" {
  zone_id = var.cloudflare_zone_pyxcloud
  name    = "beta-auth"
  type    = "A"
  content = "161.35.30.234"   # re-resolve pyx-sso before apply
  proxied = true
  ttl     = 1
}
resource "cloudflare_dns_record" "beta_api" {
  zone_id = var.cloudflare_zone_pyxcloud
  name    = "beta-api"
  type    = "A"
  content = "209.38.215.216"  # re-resolve pyx-backend before apply
  proxied = true
  ttl     = 1
}
resource "cloudflare_dns_record" "mcp_passo" {
  zone_id = var.cloudflare_zone_passo
  name    = "mcp"
  type    = "A"
  content = "206.81.24.16"    # re-resolve pyx-mcp before apply
  proxied = true
  ttl     = 1
}
```

```bash
export CLOUDFLARE_API_TOKEN=...   # scoped: Zone:DNS:Edit + Zone:SSL and Certificates:Edit
terraform apply -target=cloudflare_dns_record.beta_auth \
                -target=cloudflare_dns_record.beta_api \
                -target=cloudflare_dns_record.mcp_passo
```

Set each zone's SSL mode to `Full` (see Option B — no first-class TF resource for
the zone SSL toggle in all provider versions; the API call is authoritative).

### Option B — `gh api` / curl (Cloudflare API, precise & reversible)

```bash
TOKEN=$CLOUDFLARE_API_TOKEN
Z_PYX=<pyxcloud.io zone id> ; Z_PASSO=<passo.build zone id>
cf() { curl -s -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" "$@"; }

# 1. Find each record id (repeat per name/zone):
cf "https://api.cloudflare.com/client/v4/zones/$Z_PYX/dns_records?name=beta-auth.pyxcloud.io" | jq '.result[].id'

# 2. Repoint each record to its DO origin (PATCH keeps proxied=true):
cf -X PATCH "https://api.cloudflare.com/client/v4/zones/$Z_PYX/dns_records/<REC_ID_AUTH>" \
   --data '{"type":"A","name":"beta-auth","content":"161.35.30.234","proxied":true,"ttl":1}'
cf -X PATCH "https://api.cloudflare.com/client/v4/zones/$Z_PYX/dns_records/<REC_ID_API>" \
   --data '{"type":"A","name":"beta-api","content":"209.38.215.216","proxied":true,"ttl":1}'
cf -X PATCH "https://api.cloudflare.com/client/v4/zones/$Z_PASSO/dns_records/<REC_ID_MCP>" \
   --data '{"type":"A","name":"mcp","content":"206.81.24.16","proxied":true,"ttl":1}'

# 3. Set zone SSL mode to Full (per zone that has flipped hostnames):
cf -X PATCH "https://api.cloudflare.com/client/v4/zones/$Z_PYX/settings/ssl"   --data '{"value":"full"}'
cf -X PATCH "https://api.cloudflare.com/client/v4/zones/$Z_PASSO/settings/ssl" --data '{"value":"full"}'
```

> **SSL-mode caveat:** the zone SSL setting is zone-wide. `beta-vault.pyxcloud.io`
> currently expects `Full (strict)`. Prefer **per-hostname configuration rules**
> (Rulesets `http_config_settings` action `ssl=full` scoped by
> `http.host in {"beta-auth.pyxcloud.io" "beta-api.pyxcloud.io"}`) rather than
> flipping the whole zone, if vault must stay strict. That ruleset needs a token
> with **Zone WAF / Account Rulesets: Edit** (the same scope gap noted in
> `single-sign-on/main.tf` — the CI token could not POST to `/zones/{id}/rulesets`).

### Post-flip verification (real DNS, no --resolve)

```bash
dig +short beta-auth.pyxcloud.io   # should still be Cloudflare IPs (104.21.* / 172.67.*)
curl -s https://beta-auth.pyxcloud.io/realms/passobuild/.well-known/openid-configuration | jq .issuer
curl -s -o /dev/null -w '%{http_code}\n' https://beta-api.pyxcloud.io/q/health
curl -s -o /dev/null -w '%{http_code}\n' https://mcp.passo.build/health
```

Because the records stay **proxied**, the origin IP is never publicly visible and
the client-facing IPs (Cloudflare edge) do not change — so the flip is invisible
to clients except that the origin behind Cloudflare is now DO.

## Rollback (revert to the AWS ALB)

Repoint each record back to the AWS shared ALB DNS name (CNAME/flattened),
keeping `proxied=true`. The ALB target and its host rules are untouched by this
cutover, so rollback is immediate (Cloudflare TTL=1 / proxied ⇒ seconds):

```bash
ALB="beta-pyx-shared-alb-361858529.eu-west-1.elb.amazonaws.com"
cf -X PATCH ".../zones/$Z_PYX/dns_records/<REC_ID_AUTH>" \
   --data "{\"type\":\"CNAME\",\"name\":\"beta-auth\",\"content\":\"$ALB\",\"proxied\":true,\"ttl\":1}"
cf -X PATCH ".../zones/$Z_PYX/dns_records/<REC_ID_API>" \
   --data "{\"type\":\"CNAME\",\"name\":\"beta-api\",\"content\":\"$ALB\",\"proxied\":true,\"ttl\":1}"
cf -X PATCH ".../zones/$Z_PASSO/dns_records/<REC_ID_MCP>" \
   --data "{\"type\":\"CNAME\",\"name\":\"mcp\",\"content\":\"$ALB\",\"proxied\":true,\"ttl\":1}"
# Restore zone SSL to its prior value (was 'full' with strict expected for vault):
cf -X PATCH ".../zones/$Z_PYX/settings/ssl" --data '{"value":"full"}'
```

**Capture the current records first** (pre-flip snapshot) so rollback is exact:

```bash
for z in $Z_PYX $Z_PASSO; do
  cf "https://api.cloudflare.com/client/v4/zones/$z/dns_records?per_page=100" \
    | jq '.result[] | {name,type,content,proxied,ttl,id}'
done > cloudflare-preflip-snapshot.json
```

## DO-side runbook (already prepared — see the F4-prep PR)

1. **Re-apply origins with the :443 terminator** (targeted, 0 destroy of PG/VPC):
   ```bash
   export DO_EDGE_TLS_ORIGINS=1
   export DO_SPACES_ACCESS_KEY=... DO_SPACES_SECRET_KEY=... \
          DO_BOARD_DATABASE_URL=... DO_MCP_EMBED_TOKEN=...   # from Secrets Manager
   go run ./cutover/render.go
   cd cutover/generated && terraform init
   terraform apply -target=digitalocean_droplet_autoscale.sso \
                   -target=digitalocean_droplet_autoscale.backend \
                   -target=digitalocean_droplet_autoscale.mcp \
                   -var 'do_ssh_keys=["57496891"]'
   ```
   > A user_data change rolls each droplet_autoscale (terminate → self-heal to
   > floor 1). Acceptable in the cutover window — blue serves no prod traffic
   > until the DNS flip. Do NOT `-target` the PG clusters or the VPC.
2. **Fix `KC_HOSTNAME`** to `beta-auth.pyxcloud.io` on the sso origin (drift fix;
   the committed `platform_bootstrap_sso.go` already renders the correct value).
3. **Firewall posture:** the per-service DO firewalls open :443 from `0.0.0.0/0`.
   Once flipped, tighten each origin's :443 to **Cloudflare's published IP
   ranges** (`https://www.cloudflare.com/ips-v4` / `-v6`) so the origin only
   accepts Cloudflare edge traffic. Documented as a follow-up; keep `0.0.0.0/0`
   until the flip is verified to avoid locking yourself out during rollback.

## What still needs the CLOUDFLARE token

Everything under **"The DNS flip"** and the SSL-mode changes require a
`CLOUDFLARE_API_TOKEN` scoped for **Zone:DNS:Edit** + **Zone SSL and
Certificates:Edit** (and **Account Rulesets:Edit** if per-hostname SSL rules are
used instead of the zone toggle). No DNS record is touched until that token is
available. The DO-side prep (origin :443 terminators, KC_HOSTNAME fix) does NOT
need the Cloudflare token and can land independently.
