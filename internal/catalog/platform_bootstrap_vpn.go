package catalog

import (
	"fmt"
	"sort"
	"strings"
)

// platform_bootstrap_vpn.go — pd-MIG-CUTOVER-F2-02 (vpn slice of the platform
// module migration; sibling of platform_bootstrap_sso.go).
//
// platform_asgs.go already expresses the WireGuard VPN gateway in the canonical
// vocabulary as a `virtual-machine-scale-group` of 1. But a scale-group of a
// bare Ubuntu box is NOT the VPN gateway: the whole substance of the
// hand-written PyxCloud/internal-vpn `wireguard/user_data.sh` bootstrap is the
// WireGuard install + wg0.conf + the internal-DNS refresh + the JIT prune +
// diagnostics. Until that bootstrap is a first-class part of the abstract
// component, "migrating VPN to DigitalOcean" would silently boot an empty box.
//
// This file ports that bootstrap into the catalog as a DigitalOcean cloud-init,
// keyed under UserDataByProvider["digitalocean"] (the same wiring the SSO port
// uses). It is a faithful RE-ARCH of the AWS script — not a copy — because the
// AWS script is welded to AWS control-plane primitives that do not exist on DO:
//
//	AWS (internal-vpn/wireguard/user_data.sh)  ->  DigitalOcean (this port)
//	------------------------------------------     -----------------------------
//	dnf install (AL2023)                        ->  apt install (Ubuntu)
//	server key + peers in SSM Parameter Store   ->  persisted to a DO block-storage
//	                                                volume (survives droplet replace)
//	EIP associate (aws ec2 associate-address)   ->  DO Reserved IP (catalog reservedip:
//	                                                digitalocean_reserved_ip, assigned
//	                                                to the droplet by the autoscale pool)
//	dnsmasq refresh via aws ec2 describe-        ->  refresh via the DO API
//	  instances tag lookups                          (GET /v2/droplets?tag_name=...)
//	JIT prune revokes AWS SG ingress            ->  revoke a DO Cloud Firewall inbound
//	                                                rule (see the SPI cross-repo note
//	                                                below and docs/cutover/VPN-REARCH.md)
//
// CUTOVER IDENTITY (critical): for the DO WG server to be the SAME WireGuard
// identity as the warm AWS box, the server private key + the persisted peer
// block are INJECTED AT RENDER from the AWS SSM parameters
// (/wireguard/server-private-key + /wireguard/peers, read-only). They are lifted
// to Terraform variables here (the script references ${var.<x>}), never inlined,
// so nothing sensitive enters the abstract topology or Terraform state via this
// component — the operator wires those vars to a data source that reads the AWS
// SSM parameters during the cutover window. First boot on DO persists them to the
// block-storage volume; subsequent droplet replacements reuse the volume copy so
// the DO box no longer depends on AWS SSM once the cutover completes.
//
// JIT DOOR — CROSS-REPO FOLLOW-UP (do NOT implement here): the Keycloak JIT SPI
// (single-sign-on repo, providers/pyx-jit-allowlist) today opens/closes AWS
// security-group UDP 51820 ingress on login/logout. On DigitalOcean it must call
// the DO Cloud Firewall API instead. That is an SPI change living in a different
// repo; this port only re-targets the SERVER side (prune -> DO Firewall). See
// docs/cutover/VPN-REARCH.md for the full re-target flag.

// Pinned WireGuard/VPN constants — kept semantically identical to the
// hand-written internal-vpn module so the DO port is a faithful re-arch.
const (
	// vpnWGAddress is the WireGuard server tunnel address (matches internal-vpn).
	vpnWGAddress = "10.8.0.1/24"
	// vpnWGSubnet is the tunnel subnet NAT'd out to the private network.
	vpnWGSubnet = "10.8.0.0/24"
	// vpnWGListenPort is the WireGuard UDP listen port (51820 default).
	vpnWGListenPort = defaultWireGuardPort
	// vpnKeyVolumeMount is where the persistent DO block-storage volume (which
	// holds the server key + peers, replacing SSM) is mounted. DO attaches
	// volumes under /mnt/<volume-name>; the mount path is a variable so the
	// operator can point it at the actual volume.
	vpnKeyVolumeDefaultMount = "/mnt/wireguard-state"
	// vpnDNSRefreshInterval / vpnPruneInterval mirror the AWS systemd timers.
	vpnDNSRefreshEverySec = 60
	vpnPruneEveryMin      = 5
	// vpnPruneInactiveSec is the idle age after which a peer's firewall door is
	// revoked (matches internal-vpn: 86400s / 24h).
	vpnPruneInactiveSec = 86400
)

// VPNBootstrapSpec is the typed, provider-neutral input for the canonical
// WireGuard VPN gateway bootstrap on DigitalOcean. Every value the hand-written
// internal-vpn script pulled from the AWS control plane (SSM, EIP, tag lookups,
// the JIT SG) is lifted to an explicit field so the component is self-describing.
// The secret fields name the Terraform variable that holds the value (NOT the
// value) so nothing sensitive enters the abstract topology or Terraform state.
type VPNBootstrapSpec struct {
	// Environment is the deploy environment (e.g. "beta"); carried for parity with
	// the AWS module log group naming. Optional (defaults to "beta").
	Environment string

	// ServerPrivateKeyVar names the Terraform variable holding the WireGuard
	// server PRIVATE key. During cutover the operator wires this to a data source
	// that reads the AWS SSM parameter /wireguard/server-private-key (read-only)
	// so the DO server is the SAME identity as the warm AWS box. Defaults to
	// "wg_server_private_key".
	ServerPrivateKeyVar string
	// PeersBlobVar names the Terraform variable holding the base64-encoded peer
	// block (the appended [Peer] stanzas), wired to AWS SSM /wireguard/peers
	// during cutover. Empty resolves to no persisted peers (fresh server).
	// Defaults to "wg_peers_blob".
	PeersBlobVar string

	// DOTokenVar names the Terraform variable holding the DigitalOcean API token
	// the DNS-refresh + prune scripts use to query droplet private IPs (by tag)
	// and mutate the Cloud Firewall. Defaults to "do_api_token". Sensitive.
	DOTokenVar string
	// KeyVolumeMount names the mount path of the persistent DO block-storage
	// volume that holds the server key + peers (replacing SSM). Defaults to
	// /mnt/wireguard-state.
	KeyVolumeMount string

	// InternalDNSMap maps an internal hostname to the DigitalOcean droplet TAG
	// whose current private IP should answer that name (the DO-API equivalent of
	// the AWS name->ASG tag map). Empty falls back to the production-faithful set
	// (observability / auth / vault) so a near-empty spec still wires the gated
	// services. The values are DO droplet tag names, not AWS ASG names.
	InternalDNSMap map[string]string

	// DOFirewallIDVar names the Terraform variable holding the DO Cloud Firewall
	// id the prune script revokes idle-peer inbound rules from (the DO equivalent
	// of the AWS jit-sg). Defaults to "do_wg_jit_firewall_id". Fail-safe: empty ->
	// the prune script no-ops (it never blocks the tunnel), matching the AWS SPI's
	// fail-safe posture.
	DOFirewallIDVar string
}

// withDefaults fills the production-faithful defaults for any unset field.
func (s VPNBootstrapSpec) withDefaults() VPNBootstrapSpec {
	def := func(v, d string) string {
		if strings.TrimSpace(v) == "" {
			return d
		}
		return v
	}
	s.Environment = def(s.Environment, "beta")
	s.ServerPrivateKeyVar = def(s.ServerPrivateKeyVar, "wg_server_private_key")
	s.PeersBlobVar = def(s.PeersBlobVar, "wg_peers_blob")
	s.DOTokenVar = def(s.DOTokenVar, "do_api_token")
	s.KeyVolumeMount = def(s.KeyVolumeMount, vpnKeyVolumeDefaultMount)
	s.DOFirewallIDVar = def(s.DOFirewallIDVar, "do_wg_jit_firewall_id")
	if len(s.InternalDNSMap) == 0 {
		// Production-faithful set, ported from internal-vpn's declare -A MAP but
		// keyed by DO droplet tag (the DO-API equivalent of the AWS ASG-tag).
		s.InternalDNSMap = map[string]string{
			"observability.pyxcloud.io": "pyx-observability",
			"beta-auth.pyxcloud.io":     "pyx-sso",
			"sso-admin":                 "pyx-sso",
			"beta-vault.pyxcloud.io":    "pyx-vault",
		}
	}
	return s
}

// VPNBootstrapVariableNames returns, in deterministic order, the Terraform
// variable names this bootstrap references, split plain/sensitive. The
// assembler/CLI uses it to emit the matching `variable "<x>" {}` declarations
// (the secret ones marked sensitive) so the rendered .tf is self-contained.
func (s VPNBootstrapSpec) VPNBootstrapVariableNames() (plain []string, sensitive []string) {
	s = s.withDefaults()
	plain = []string{s.DOFirewallIDVar}
	sensitive = []string{s.ServerPrivateKeyVar, s.PeersBlobVar, s.DOTokenVar}
	return plain, sensitive
}

// sortedDNSHosts returns the InternalDNSMap hosts in deterministic order so the
// rendered script is byte-stable (map iteration is random in Go).
func (s VPNBootstrapSpec) sortedDNSHosts() []string {
	hosts := make([]string, 0, len(s.InternalDNSMap))
	for h := range s.InternalDNSMap {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	return hosts
}

// RenderVPNBootstrapUserData renders the canonical WireGuard VPN gateway
// cloud-init for DigitalOcean as a bash script with `${var.<x>}` placeholders.
// It is a faithful RE-ARCH of internal-vpn/wireguard/user_data.sh: apt install
// wireguard + dnsmasq; persist the server key + peers to a DO block-storage
// volume (seeded at first boot from the AWS-SSM-injected vars); wg0.conf with
// NAT into the private network; dnsmasq internal-name refresh via the DO API
// (droplet tag -> private IP); and the idle-peer prune re-targeted to the DO
// Cloud Firewall. The returned string is meant to be placed into
// AssembleScaleGroup.UserDataByProvider["digitalocean"] for the "vpn" service.
//
// NOTE: the DO Reserved IP (stable public endpoint, replacing the AWS EIP) is
// NOT associated from inside the script — it is a first-class catalog component
// (reserved-ip -> digitalocean_reserved_ip) bound to the autoscale pool by the
// renderer, so the replacement droplet reclaims the same endpoint declaratively.
func RenderVPNBootstrapUserData(spec VPNBootstrapSpec) (string, error) {
	s := spec.withDefaults()

	v := func(name string) string { return "${var." + name + "}" }
	mount := s.KeyVolumeMount

	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }

	w("#!/bin/bash")
	w("set -euxo pipefail")
	w("export HOME=/root")
	w("# Canonical WireGuard VPN gateway bootstrap — DigitalOcean re-arch of")
	w("# PyxCloud/internal-vpn/wireguard/user_data.sh (pd-MIG-CUTOVER-F2-02).")
	w("# apt (Ubuntu); server key + peers persisted to a DO block-storage volume;")
	w("# DNS refresh via the DO API; idle-peer prune via the DO Cloud Firewall.")
	w("# All secrets are Terraform variables, never inlined.")
	w("")
	w("export DEBIAN_FRONTEND=noninteractive")
	w("apt-get update")
	w("apt-get install -y wireguard wireguard-tools dnsmasq iptables jq curl")
	w("")
	w("WG_DIR=/etc/wireguard")
	w("mkdir -p \"$WG_DIR\"")
	w("PRIMARY_IF=$(ip route show default | awk '/default/ {print $5; exit}')")
	w("")
	w("# --- Persistent state on the DO block-storage volume (replaces AWS SSM). ---")
	w("# The volume survives droplet replacement, so the WireGuard identity + peers")
	w("# persist across autoscale-pool self-heal exactly like SSM did on AWS.")
	w("STATE_DIR=\"%s\"", mount)
	w("mkdir -p \"$STATE_DIR\"")
	w("KEY_FILE=\"$STATE_DIR/server-private-key\"")
	w("PEERS_FILE=\"$STATE_DIR/peers.b64\"")
	w("")
	w("# --- Server key: reuse the volume copy if present (survives replacement),")
	w("# else seed it from the AWS-SSM-injected var so the DO box is the SAME")
	w("# WireGuard identity as the warm AWS server during cutover. ---")
	w("if [ -s \"$KEY_FILE\" ]; then")
	w("  SERVER_PRIV=$(cat \"$KEY_FILE\")")
	w("else")
	w("  SERVER_PRIV=\"%s\"", v(s.ServerPrivateKeyVar))
	w("  if [ -z \"$SERVER_PRIV\" ]; then")
	w("    SERVER_PRIV=$(wg genkey)")
	w("  fi")
	w("  printf '%%s' \"$SERVER_PRIV\" > \"$KEY_FILE\"")
	w("  chmod 600 \"$KEY_FILE\"")
	w("fi")
	w("SERVER_PUB=$(printf '%%s' \"$SERVER_PRIV\" | wg pubkey)")
	w("printf '%%s' \"$SERVER_PUB\" > \"$STATE_DIR/server-public-key\"")
	w("")
	w("# --- Peers: reuse the volume copy if present, else seed from the AWS-SSM-")
	w("# injected var (base64 of the appended [Peer] stanzas). ---")
	w("if [ -s \"$PEERS_FILE\" ]; then")
	w("  PEERS_BLOB=$(cat \"$PEERS_FILE\")")
	w("else")
	w("  PEERS_BLOB=\"%s\"", v(s.PeersBlobVar))
	w("  if [ -n \"$PEERS_BLOB\" ]; then")
	w("    printf '%%s' \"$PEERS_BLOB\" > \"$PEERS_FILE\"")
	w("    chmod 600 \"$PEERS_FILE\"")
	w("  fi")
	w("fi")
	w("")
	w("# --- Base wg0.conf (NAT the tunnel out to the private network). ---")
	w("cat >\"$WG_DIR/wg0.conf\" <<CONF")
	w("[Interface]")
	w("Address = %s", vpnWGAddress)
	w("ListenPort = %d", vpnWGListenPort)
	w("PrivateKey = $SERVER_PRIV")
	w("PostUp = sysctl -w net.ipv4.ip_forward=1; iptables -t nat -A POSTROUTING -s %s -o $PRIMARY_IF -j MASQUERADE; iptables -A FORWARD -i wg0 -j ACCEPT; iptables -A FORWARD -o wg0 -j ACCEPT", vpnWGSubnet)
	w("PostDown = iptables -t nat -D POSTROUTING -s %s -o $PRIMARY_IF -j MASQUERADE; iptables -D FORWARD -i wg0 -j ACCEPT; iptables -D FORWARD -o wg0 -j ACCEPT", vpnWGSubnet)
	w("CONF")
	w("if [ -n \"$PEERS_BLOB\" ]; then")
	w("  echo \"$PEERS_BLOB\" | base64 -d >> \"$WG_DIR/wg0.conf\" 2>/dev/null || true")
	w("fi")
	w("chmod 600 \"$WG_DIR/wg0.conf\"")
	w("")
	w("systemctl enable --now wg-quick@wg0")
	w("")
	w("# --- dnsmasq: peers set DNS = %s; everything else forwards upstream. ---", strings.TrimSuffix(vpnWGAddress, "/24"))
	w("cat >/etc/dnsmasq.d/internal.conf <<DNS")
	w("listen-address=%s,127.0.0.1", strings.TrimSuffix(vpnWGAddress, "/24"))
	w("bind-interfaces")
	w("server=1.1.1.1")
	w("DNS")
	w("systemctl enable --now dnsmasq")
	w("")
	w("mkdir -p /var/log/wireguard")
	w("touch /var/log/wireguard/internal-dns-refresh.log /var/log/wireguard/prune.log")
	w("")
	w("# --- DO API token for the refresh + prune scripts (from a TF variable). ---")
	w("mkdir -p /etc/wireguard")
	w("printf '%%s' \"%s\" > /etc/wireguard/do_token", v(s.DOTokenVar))
	w("chmod 600 /etc/wireguard/do_token")
	w("printf '%%s' \"%s\" > /etc/wireguard/do_firewall_id", v(s.DOFirewallIDVar))
	w("chmod 600 /etc/wireguard/do_firewall_id")
	w("")
	w("# --- Refresh script: map internal names -> current DO droplet private IPs")
	w("# via the DigitalOcean API (GET /v2/droplets?tag_name=...), replacing the")
	w("# AWS EC2 instance-describe tag lookups. ---")
	w("cat >/usr/local/bin/wg-internal-dns-refresh.sh <<'REFRESH'")
	w("#!/usr/bin/env bash")
	w("set -euo pipefail")
	w("LOG=/var/log/wireguard/internal-dns-refresh.log")
	w("DO_TOKEN=$(cat /etc/wireguard/do_token 2>/dev/null || echo \"\")")
	w("OUT=/etc/dnsmasq.d/internal-hosts.conf")
	w("OUT_TMP=\"${OUT}.$$\"")
	w("trap 'rm -f \"${OUT_TMP}\"' EXIT")
	w(": > \"$OUT_TMP\"")
	w("changed=0")
	w("do_droplet_ip() {")
	w("  # $1 = droplet tag name -> newest matching droplet's private IPv4.")
	w("  curl -fsS -H \"Authorization: Bearer ${DO_TOKEN}\" \\")
	w("    \"https://api.digitalocean.com/v2/droplets?tag_name=$1&per_page=200\" 2>/dev/null \\")
	w("    | jq -r '[.droplets[]] | sort_by(.created_at) | last")
	w("             | .networks.v4[] | select(.type==\"private\") | .ip_address' 2>/dev/null \\")
	w("    | head -n1")
	w("}")
	// Deterministic host/tag pairs (byte-stable output).
	for _, host := range s.sortedDNSHosts() {
		w("host=%q; tag=%q", host, s.InternalDNSMap[host])
		w("ip=$(do_droplet_ip \"$tag\")")
		w("if [ -n \"$ip\" ] && [ \"$ip\" != \"null\" ]; then")
		w("  echo \"address=/${host}/${ip}\" >> \"$OUT_TMP\"")
		w("  printf '%%s host=%%s tag=%%s ip=%%s\\n' \"$(date -Is)\" \"$host\" \"$tag\" \"$ip\" >> \"$LOG\"")
		w("else")
		w("  printf '%%s host=%%s tag=%%s ip=missing\\n' \"$(date -Is)\" \"$host\" \"$tag\" >> \"$LOG\"")
		w("fi")
	}
	w("if ! cmp -s \"$OUT_TMP\" \"$OUT\" 2>/dev/null; then")
	w("  mv \"$OUT_TMP\" \"$OUT\"; changed=1; systemctl restart dnsmasq")
	w("else")
	w("  rm -f \"$OUT_TMP\"")
	w("fi")
	w("printf '%%s changed=%%s\\n' \"$(date -Is)\" \"$changed\" >> \"$LOG\"")
	w("REFRESH")
	w("chmod 755 /usr/local/bin/wg-internal-dns-refresh.sh")
	w("")
	w("# --- Prune script: revoke idle-peer inbound on the DO Cloud Firewall")
	w("# (replaces the AWS SG-ingress revoke). Fail-safe: no firewall")
	w("# id -> no-op, the tunnel is never blocked. NOTE: the *opening* of a peer's")
	w("# firewall door on login stays in the Keycloak JIT SPI (single-sign-on repo,")
	w("# providers/pyx-jit-allowlist), which must be re-targeted AWS-SG -> DO-Firewall")
	w("# as a cross-repo follow-up (docs/cutover/VPN-REARCH.md). ---")
	w("cat >/usr/local/bin/wg-prune-inactive.sh <<'PRUNE'")
	w("#!/usr/bin/env bash")
	w("set -euo pipefail")
	w("LOG=/var/log/wireguard/prune.log")
	w("DO_TOKEN=$(cat /etc/wireguard/do_token 2>/dev/null || echo \"\")")
	w("FW_ID=$(cat /etc/wireguard/do_firewall_id 2>/dev/null || echo \"\")")
	w("if [ -z \"$FW_ID\" ]; then")
	w("  printf '%%s no DO firewall id set; prune no-op (fail-safe)\\n' \"$(date -Is)\" >> \"$LOG\"; exit 0")
	w("fi")
	w("now=$(date +%%s)")
	w("declare -A peer_endpoints")
	w("while read -r pubkey endpoint; do")
	w("  if [ -n \"$endpoint\" ] && [ \"$endpoint\" != \"(none)\" ]; then")
	w("    peer_endpoints[\"$pubkey\"]=\"${endpoint%%:*}\"")
	w("  fi")
	w("done < <(wg show wg0 endpoints)")
	w("while read -r pubkey handshake; do")
	w("  is_inactive=0")
	w("  if [ \"$handshake\" -eq 0 ]; then is_inactive=1; else")
	w("    age=$((now - handshake)); if [ \"$age\" -ge %d ]; then is_inactive=1; fi", vpnPruneInactiveSec)
	w("  fi")
	w("  if [ \"$is_inactive\" -eq 1 ]; then")
	w("    ip=\"${peer_endpoints[\"$pubkey\"]:-}\"")
	w("    if [ -n \"$ip\" ]; then")
	w("      printf '%%s pruning inactive peer ip=%%s\\n' \"$(date -Is)\" \"$ip\" >> \"$LOG\"")
	w("      # Remove the peer's /32 inbound rule (UDP %d) from the DO Cloud Firewall.", vpnWGListenPort)
	w("      curl -fsS -X DELETE -H \"Authorization: Bearer ${DO_TOKEN}\" \\")
	w("        -H \"Content-Type: application/json\" \\")
	w("        \"https://api.digitalocean.com/v2/firewalls/${FW_ID}/rules\" \\")
	w("        -d \"{\\\"inbound_rules\\\":[{\\\"protocol\\\":\\\"udp\\\",\\\"ports\\\":\\\"%d\\\",\\\"sources\\\":{\\\"addresses\\\":[\\\"${ip}/32\\\"]}}]}\" \\", vpnWGListenPort)
	w("        >> \"$LOG\" 2>&1 || printf '%%s revoke failed/absent ip=%%s\\n' \"$(date -Is)\" \"$ip\" >> \"$LOG\"")
	w("    fi")
	w("  fi")
	w("done < <(wg show wg0 latest-handshakes)")
	w("PRUNE")
	w("chmod 755 /usr/local/bin/wg-prune-inactive.sh")
	w("")
	w("# --- systemd timers (parity with the AWS module). ---")
	w("cat >/etc/systemd/system/wg-internal-dns-refresh.service <<'UNIT'")
	w("[Unit]")
	w("Description=Refresh private DNS records served by WireGuard dnsmasq (DO API)")
	w("After=network-online.target dnsmasq.service")
	w("Wants=network-online.target")
	w("[Service]")
	w("Type=oneshot")
	w("ExecStart=/usr/local/bin/wg-internal-dns-refresh.sh")
	w("UNIT")
	w("cat >/etc/systemd/system/wg-internal-dns-refresh.timer <<'UNIT'")
	w("[Unit]")
	w("Description=Run WireGuard private DNS refresh every %ds", vpnDNSRefreshEverySec)
	w("[Timer]")
	w("OnBootSec=20s")
	w("OnUnitActiveSec=%ds", vpnDNSRefreshEverySec)
	w("AccuracySec=5s")
	w("Unit=wg-internal-dns-refresh.service")
	w("[Install]")
	w("WantedBy=timers.target")
	w("UNIT")
	w("cat >/etc/systemd/system/wg-prune-inactive.service <<'UNIT'")
	w("[Unit]")
	w("Description=Prune inactive WireGuard peer DO-firewall inbound rules")
	w("After=network-online.target wg-quick@wg0.service")
	w("Wants=network-online.target")
	w("[Service]")
	w("Type=oneshot")
	w("ExecStart=/usr/local/bin/wg-prune-inactive.sh")
	w("UNIT")
	w("cat >/etc/systemd/system/wg-prune-inactive.timer <<'UNIT'")
	w("[Unit]")
	w("Description=Run WireGuard inactive peer prune every %dm", vpnPruneEveryMin)
	w("[Timer]")
	w("OnBootSec=1m")
	w("OnUnitActiveSec=%dm", vpnPruneEveryMin)
	w("AccuracySec=10s")
	w("Unit=wg-prune-inactive.service")
	w("[Install]")
	w("WantedBy=timers.target")
	w("UNIT")
	w("")
	w("systemctl daemon-reload")
	w("systemctl enable --now wg-internal-dns-refresh.timer")
	w("systemctl enable --now wg-prune-inactive.timer")
	w("/usr/local/bin/wg-internal-dns-refresh.sh || true")
	w("/usr/local/bin/wg-prune-inactive.sh || true")

	return b.String(), nil
}
