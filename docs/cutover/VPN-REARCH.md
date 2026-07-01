# VPN (WireGuard + JIT) — DigitalOcean re-arch

`pd-MIG-CUTOVER-F2-02` (vpn) · epic `EPIC-AWS-TO-DO-MIGRATION`

Re-architecture of the corporate WireGuard VPN gateway from AWS to DigitalOcean.
The AWS service is the hardest platform module in the cutover because it is
**AWS-control-plane saturated**: its bootstrap talks to SSM Parameter Store, the
EC2 metadata + EIP APIs, `ec2 describe-instances`, and (via the Keycloak SPI) an
AWS security group. This document records what the DO-side port changes, and the
**one cross-repo follow-up** that is intentionally NOT implemented here.

## Source (AWS) vs target (DO)

| Concern | AWS (`PyxCloud/internal-vpn` `wireguard/user_data.sh`) | DigitalOcean (this repo) |
|---|---|---|
| OS / packages | Amazon Linux 2023, `dnf install wireguard-tools dnsmasq` | Ubuntu, `apt-get install wireguard wireguard-tools dnsmasq` |
| Server key + peers store | SSM Parameter Store (`/wireguard/server-private-key`, `/wireguard/peers`) | **DO block-storage volume** mounted at `/mnt/wireguard-state` (survives droplet replacement) |
| Cutover identity | key is native to SSM | **injected at render from AWS SSM** into TF vars (see below) so the DO server is the SAME WireGuard identity |
| Stable public endpoint | EIP, `aws ec2 associate-address` from user_data | **DO Reserved IP** (`digitalocean_reserved_ip`, catalog `reserved-ip`), bound to the autoscale pool declaratively — not from user_data |
| Internal DNS refresh | `aws ec2 describe-instances` tag lookup → dnsmasq `address=/host/ip` | **DO API** `GET /v2/droplets?tag_name=…` → newest droplet's private IPv4 → dnsmasq |
| Idle-peer prune (revoke) | `aws ec2 revoke-security-group-ingress` (UDP 51820 /32) | **DO Cloud Firewall API** `DELETE /v2/firewalls/{id}/rules` |
| JIT door **open** (login) | Keycloak SPI mutates AWS SG ingress | **cross-repo follow-up — see below** |
| Log shipping | CloudWatch agent | dropped from the port (DO monitoring / obs box handles logs); local logs kept under `/var/log/wireguard/` |

Implementation:
- `internal/catalog/platform_bootstrap_vpn.go` — `RenderVPNBootstrapUserData(VPNBootstrapSpec)`
  emits the DO cloud-init (sibling of `platform_bootstrap_sso.go`).
- `internal/catalog/platform_asgs.go` — `PlatformScaleGroupComponentsWithBootstraps(...)`
  threads the DO script into the `vpn` scale-group's
  `UserDataByProvider["digitalocean"]`, which the scale-group translator prefers
  on a DigitalOcean render (`ScaleGroupSpec.UserDataByProvider`).
- The stable endpoint is the existing `reserved-ip` component
  (`internal/catalog/reservedip.go` → `digitalocean_reserved_ip`).

## Cutover identity — server key + peers injected from AWS SSM

For the DO WireGuard server to be the **same identity** as the warm AWS box (so
existing peer configs keep working), the server private key and the persisted
peer block are **injected at render** from the AWS SSM parameters, lifted to
Terraform variables — never inlined into the topology or state:

| Terraform variable | Wired (during cutover) to | Sensitive |
|---|---|---|
| `wg_server_private_key` | AWS SSM `/wireguard/server-private-key` (read-only) | yes |
| `wg_peers_blob` | AWS SSM `/wireguard/peers` (base64 of the `[Peer]` stanzas) | yes |
| `do_api_token` | DO API token (DNS refresh + firewall prune) | yes |
| `do_wg_jit_firewall_id` | DO Cloud Firewall id the prune revokes from | no |

First boot on DO persists the key + peers to the block-storage volume; subsequent
droplet replacements reuse the volume copy, so **once cutover completes the DO box
no longer depends on AWS SSM.** The operator wires the two `wg_*` variables to a
data source reading the AWS SSM parameters only for the cutover window.

## JIT model — cross-repo follow-up (NOT implemented here)

The JIT door has two halves:

1. **Server-side prune (idle revoke)** — re-targeted in this PR. The prune script
   revokes a peer's `/32` inbound rule via the **DO Cloud Firewall API** instead
   of the AWS SG. Fail-safe: no firewall id → prune no-ops, the tunnel is never
   blocked.

2. **Door OPEN on login** — **cross-repo follow-up. Flagged, not done here.**
   Today the Keycloak JIT SPI (`single-sign-on` repo, `providers/pyx-jit-allowlist`)
   opens WireGuard UDP `51820` ingress on the AWS security group to the logged-in
   user's source IP `/32` on login, and revokes on logout/idle, backed by a
   DynamoDB `jit-allowlist` table. On DigitalOcean this SPI must instead call the
   **DO Cloud Firewall API** (add an inbound rule for the user's `/32`), backed by
   a DO-side allowlist store. That is a code change in a **different repository**
   (the SPI is a Keycloak Java provider), so it is out of scope for this
   provider-catalog PR.

   **Interface sketch for the follow-up (single-sign-on repo):**
   - env: `JIT_BACKEND=do` selects the DO firewall implementation (vs `aws`).
   - env: `DO_WG_JIT_FIREWALL_ID`, `DO_API_TOKEN` (the same firewall this repo's
     prune script targets — one firewall, opened by the SPI, closed by the prune).
   - open: `POST /v2/firewalls/{id}/rules` with an `inbound_rules` entry
     `{protocol: udp, ports: "51820", sources:{addresses:["<ip>/32"]}}`.
   - close: `DELETE /v2/firewalls/{id}/rules` with the same rule shape (the exact
     call the server-side prune already makes).
   - allowlist store: replace the DynamoDB `jit-allowlist` table with a DO-side
     equivalent (or keep the reaper's TTL semantics against any KV the SPI can
     reach). Track under a follow-up board task in `EPIC-AWS-TO-DO-MIGRATION`.

   The abstract `vpn-access` component (`internal/catalog/vpnaccess.go`) currently
   returns `ErrComponentUnsupported` for non-AWS providers precisely because this
   SPI half is AWS-native. When the SPI re-target lands, `vpn-access` can grow a
   DigitalOcean plan (DO Cloud Firewall + a DO allowlist store) — a separate,
   follow-up change in this repo.

## Reachability

The VPN gates internal services (observability, SSO admin, vault, backend). Those
must stay reachable from the tunnel:

- **DNS**: the DO-API refresh resolves the gated hostnames to the current droplet
  private IP by **droplet tag** (`observability.pyxcloud.io` → `pyx-observability`,
  `beta-auth.pyxcloud.io`/`sso-admin` → `pyx-sso`, `beta-vault.pyxcloud.io` →
  `pyx-vault`). Adjust `VPNBootstrapSpec.InternalDNSMap` to match the DO droplet
  tags the services actually carry.
- **Firewall**: the **per-service DO Cloud Firewall already exists** in the DO
  baseline (`digitalocean_firewall` split one-per-service; see
  `internal/catalog/do_baseline.go`). The gated services must allow inbound from
  the VPN droplet's tag / the tunnel subnet `10.8.0.0/24` — this is a firewall
  inbound rule on the *service* side, not on the VPN box.

## Cutover safety

- **AWS WG stays warm as fallback** for the whole cutover window. The DO box
  shares the AWS identity (via the injected key + peers), so peers can be pointed
  at the DO Reserved IP endpoint and rolled back to the AWS EIP endpoint without
  re-issuing configs.
- Roll forward only after: (1) DO-API DNS refresh resolves all gated hostnames,
  (2) the SPI re-target (above) is live so login opens the DO firewall door, and
  (3) reachability to obs/backend/vault/SSO is verified over the DO tunnel.

## Verification

- `go build ./... && go vet ./...` — exit 0.
- `go test ./internal/catalog/ -run VPN` — DO bootstrap unit tests
  (faithful-re-arch, identity-from-vars, deterministic, `UserDataByProvider` wiring).
- `terraform init && validate` on the DO baseline / full DO estate — GREEN
  (`TestDOBaselineTerraformValidate`, `TestFullEstateTerraformValidateDO`).
- **No apply.**
