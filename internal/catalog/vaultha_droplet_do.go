package catalog

import (
	"fmt"
	"strings"
)

// vaultha_droplet_do.go — Phase 0 of the Vault-HA-on-DO migration
// (pd-MIG-VAULT-HA-HARDEN / EPIC-AWS-TO-DO-MIGRATION).
//
// WHY A DROPLET MODE (not the DOKS/Helm vault-ha component)
// --------------------------------------------------------
// The existing `vault-ha` component (vaultha.go / render_vaultha.go) models the
// DO target as the OFFICIAL hashicorp/vault Helm chart on a DOKS cluster. That is
// the operator-pattern end-state, BUT **no DOKS cluster exists in the estate**:
// every live DO service is a plain `digitalocean_droplet_autoscale` pool (see
// do_baseline.go). Standing up DOKS purely for Vault is a new managed surface the
// owner has declined — the DECISION is a **3-droplet Raft cluster** matching the
// all-droplet fleet.
//
// This file is the reproducible catalog source for that shape. It renders three
// fixed Vault droplets forming a Raft integrated-storage quorum, an optional DO
// block volume per node (durable /opt/vault/data), optional DO reserved IPs (so a
// droplet roll/self-heal does not break the hardcoded beta-vault A-record — see the
// durable-DO-edge memo), a DO firewall that keeps :8200 PRIVATE (VPC + WireGuard
// only), and a configurable seal stanza (Shamir by default; Transit remains
// available as a single config flip for a future opt-in).
//
// PEER DISCOVERY (the chicken/egg) — SOLVED WITHOUT HARDCODED IPS
// --------------------------------------------------------------
// A raft `retry_join` needs each node to find the other two. Autoscale pools give
// droplets DYNAMIC private IPs (unknowable at render time), so this mode uses THREE
// FIXED `digitalocean_droplet` resources instead — and joins them by DO
// cloud-auto-join (`retry_join { auto_join = "provider=digitalocean region=<r>
// tag_name=<tag>" }`). Consul-style go-discover queries the DO API by tag at boot,
// so nodes discover each other by TAG, never by a baked-in IP. That keeps the
// cluster reproducible and roll-safe. (Fixed private IPs are also emitted as a
// commented fallback for air-gapped discovery.)
//
// VAULT VERSION is pinned to 1.15.6 to MATCH the AWS source Vault, so a
// `raft snapshot restore` from the AWS cluster into this one is byte-clean (Phase 2).
//
// SEAL = SHAMIR (owner decision 2026-07-07, pd-MIG-VAULT-HA-HARDEN)
// -------------------------------------------------------------
// The AWS-KMS auto-unseal bridge (a review-flagged security item: static AWS
// creds baked as systemd Environment=) has been dropped. The rendered vault.hcl
// carries NO seal stanza — Shamir default — so every node requires a MANUAL
// unseal (3-of-5 key shares held by the owner) after a restart/reboot. The
// staging cluster was migrated live to Shamir on 2026-07-07; this render change
// aligns future clusters (including vault prod) with that same shape. See the
// unseal runbook in the secrets-manager repo. The AWS KMS key backing the old
// bridge is scheduled for deletion.

// Vault droplet-mode constants.
const (
	// vaultDropletVersion matches the running AWS Vault (findings §1.1) so a raft
	// snapshot restore across the migration is version-clean.
	vaultDropletVersion = "1.15.6"
	// vaultDropletCount is the fixed Raft quorum (odd, >= 3). Not tunable in Phase 0
	// — 3 is the migration target; a larger odd count is a later concern.
	vaultDropletCount = 3
	// vaultListenerPort is the Vault API/cluster listener.
	vaultListenerPort = 8200
	// vaultClusterPort is the Raft cluster-communication port.
	vaultClusterPort = 8201
	// vaultDropletTag is the DO tag every Vault node carries; the firewall selects on
	// it and cloud-auto-join discovers peers by it.
	vaultDropletTag = "pyx-vault"
	// vaultDataVolumeSizeGiB is the per-node block volume backing /opt/vault/data.
	vaultDataVolumeSizeGiB = 20
)

// VaultSealMode selects the auto-unseal seal stanza the nodes render.
//
//   - VaultSealShamir  — the DEFAULT and current end-state (owner decision
//     2026-07-07): no auto-unseal. Emits NO seal stanza; every restart/reboot
//     requires a MANUAL unseal (3-of-5 key shares held by the owner). The AWS-KMS
//     bridge that used to fill this role has been retired (static AWS creds baked
//     into systemd were a review-flagged security item, and the AWS estate is
//     being decommissioned).
//   - VaultSealTransit — an auto-unseal option against a separate small
//     "unseal Vault"'s transit engine, for a future opt-in (never the default).
type VaultSealMode string

const (
	VaultSealTransit VaultSealMode = "transit"
	VaultSealShamir  VaultSealMode = "shamir"
)

// VaultDropletSpec describes the 3-node Raft droplet cluster. Everything secret
// (Transit addr+token+key, when seal=transit) is a RENDER-TIME injection passed
// through here and inlined into user_data — it is NEVER committed and NEVER stored
// in terraform state as a resource attribute (same discipline as DOBaselineSecrets
// and workloadidentity.go's out-of-band env). The default seal (Shamir) has no
// secret material to inject at all — unseal is a manual, out-of-band operator step.
type VaultDropletSpec struct {
	// Name is the resource/hostname prefix, e.g. "pyx-vault". Empty -> "pyx-vault".
	Name string
	// Region is the concrete DO slug (fra1) — the caller resolves the abstract
	// region_name via the catalog before calling (as AssembleDOBaseline does).
	Region string
	// Size is the concrete droplet SKU slug (e.g. "s-2vcpu-4gb"); the caller resolves
	// it via cat.ResolveSKU so it is never hand-picked.
	Size string
	// Image is the concrete DO image slug (ubuntu 24.04) resolved via cat.ResolveImage.
	Image string
	// VPCRef is the terraform reference expression for the VPC uuid the nodes join,
	// e.g. "digitalocean_vpc.passo-do-baseline-net.id". Required so :8200 stays on the
	// private VPC network.
	VPCRef string

	// Seal selects the seal stanza. Empty -> VaultSealShamir (no auto-unseal; manual
	// 3-of-5 unseal post-reboot — see the runbook in the secrets-manager repo).
	Seal VaultSealMode
	// Transit seal parameters (only used when Seal == VaultSealTransit). TransitAddr
	// is the unseal-Vault address, TransitToken a token with `transit/` access,
	// TransitKeyName the key. All injected out-of-band at render time.
	TransitAddr    string
	TransitToken   string
	TransitKeyName string

	// ReservedIPs, when true, emits a digitalocean_reserved_ip per node bound to the
	// droplet, giving Vault STABLE public addresses so the beta-vault A-record / a DO
	// LB origin survives a droplet roll (durable-DO-edge memo). Off by default: Phase
	// 0 renders the cluster; the reserved-IP/LB fronting is the Phase-1 wiring.
	ReservedIPs bool

	// NodeCount is the number of nodes in the cluster (e.g. 1 or 3).
	NodeCount int
}

// VaultDropletDefaults normalizes a spec and enforces the invariants (odd quorum,
// seal mode, required refs). Returns a copy; never mutates the input.
func (s VaultDropletSpec) normalized() (VaultDropletSpec, error) {
	out := s
	out.Name = strings.TrimSpace(out.Name)
	if out.Name == "" {
		out.Name = vaultDropletTag
	}
	if strings.TrimSpace(out.Region) == "" {
		return out, fmt.Errorf("vault-ha droplet: region (concrete DO slug) is required")
	}
	if strings.TrimSpace(out.Size) == "" {
		return out, fmt.Errorf("vault-ha droplet: size (resolved droplet SKU) is required")
	}
	if strings.TrimSpace(out.Image) == "" {
		return out, fmt.Errorf("vault-ha droplet: image (resolved DO image) is required")
	}
	if strings.TrimSpace(out.VPCRef) == "" {
		return out, fmt.Errorf("vault-ha droplet: VPCRef is required (nodes must be private-VPC only)")
	}
	if out.NodeCount == 0 {
		out.NodeCount = 3
	}
	if out.NodeCount != 1 && out.NodeCount != 3 {
		return out, fmt.Errorf("vault-ha droplet: node_count=%d is not supported (only 1 or 3 nodes)", out.NodeCount)
	}
	if out.Seal == "" {
		out.Seal = VaultSealShamir // default: no auto-unseal, manual 3-of-5 post-reboot
	}
	switch out.Seal {
	case VaultSealTransit:
		if strings.TrimSpace(out.TransitAddr) == "" || strings.TrimSpace(out.TransitToken) == "" {
			return out, fmt.Errorf(
				"vault-ha droplet: seal=transit (end-state) requires TransitAddr + TransitToken of the unseal-Vault")
		}
		if strings.TrimSpace(out.TransitKeyName) == "" {
			out.TransitKeyName = defaultTransitKey
		}
	case VaultSealShamir:
		// No seal stanza; manual unseal. Nothing to validate.
	default:
		return out, fmt.Errorf("vault-ha droplet: unknown seal mode %q (transit | shamir)", out.Seal)
	}
	return out, nil
}

// RenderVaultDropletCluster emits the terraform documents for the 3-node Raft Vault
// droplet cluster: N block volumes, N droplets (each with a raft/auto-join
// user_data), an optional reserved IP per node, and a private-only firewall on
// :8200/:8201. Deterministic (fixed slice order) so the plan is byte-stable.
func RenderVaultDropletCluster(spec VaultDropletSpec) ([]string, error) {
	s, err := spec.normalized()
	if err != nil {
		return nil, err
	}
	var docs []string

	// 1. Private-only firewall: :8200 (API) + :8201 (raft cluster) reachable ONLY
	//    from inside the VPC (backend/sso/mcp) — plus SSH from the WireGuard base SG
	//    for humans. NO public 0.0.0.0/0 on :8200 (findings: private-only exposure).
	//    Egress open so the box can auto-join via the DO API (and reach the Transit
	//    unseal-Vault, when seal=transit).
	docs = append(docs, fmt.Sprintf(`resource "digitalocean_firewall" %q {
  name = %q
  tags = [%q]

  # Vault API + Raft cluster: VPC-internal only (no public 0.0.0.0/0 on 8200).
  inbound_rule {
    protocol         = "tcp"
    port_range       = "%d"
    source_addresses = ["10.0.1.0/24"]
  }
  inbound_rule {
    protocol         = "tcp"
    port_range       = "%d"
    source_addresses = ["10.0.1.0/24"]
  }

  outbound_rule {
    protocol              = "tcp"
    port_range            = "1-65535"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }
  outbound_rule {
    protocol              = "udp"
    port_range            = "1-65535"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }
  outbound_rule {
    protocol              = "icmp"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }
}`, s.Name+"-sg", s.Name+"-sg", vaultDropletTag, vaultListenerPort, vaultClusterPort))

	// 2..N per-node: block volume + droplet (+ optional reserved IP).
	for i := 1; i <= s.NodeCount; i++ {
		node := fmt.Sprintf("%s-%d", s.Name, i)
		nodeID := fmt.Sprintf("vault-node-%d", i)

		// Durable data volume mounted at /opt/vault/data (raft integrated storage).
		docs = append(docs, fmt.Sprintf(`resource "digitalocean_volume" %q {
  name                    = %q
  region                  = %q
  size                    = %d
  initial_filesystem_type = "ext4"
  description             = "Vault raft integrated storage (/opt/vault/data)"

  lifecycle {
    prevent_destroy = true
  }
}`, node+"-data", node+"-data", s.Region, vaultDataVolumeSizeGiB))

		userData := renderVaultNodeUserData(s, nodeID)
		docs = append(docs, fmt.Sprintf(`resource "digitalocean_droplet" %q {
  name       = %q
  region     = %q
  size       = %q
  image      = %q
  vpc_uuid   = %s
  ssh_keys   = var.do_ssh_keys
  tags       = [%q]
  volume_ids = [digitalocean_volume.%s.id]

  user_data = <<-USERDATA
%s
  USERDATA
}`, node, node, s.Region, s.Size, s.Image, s.VPCRef, vaultDropletTag, node+"-data", indentUserData(userData)))

		if s.ReservedIPs {
			// Stable address so beta-vault DNS / a DO LB origin survives a roll.
			docs = append(docs, fmt.Sprintf(`resource "digitalocean_reserved_ip" %q {
  droplet_id = digitalocean_droplet.%s.id
  region     = %q
}`, node+"-ip", node, s.Region))
		}
	}

	return docs, nil
}

// renderVaultNodeUserData produces the cloud-init bootstrap for one Vault node:
// install Vault 1.15.6, mount the data volume, write vault.hcl (raft storage +
// auto-join by DO tag + the configured seal stanza + :8200 listener), and start.
func renderVaultNodeUserData(s VaultDropletSpec, nodeID string) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }

	w("#!/bin/bash")
	w("# pyx Vault-HA node (%s) — DO droplet bootstrap. Raft integrated storage,", nodeID)
	w("# cloud-auto-join by DO tag %q, seal mode %q. Vault %s (matches AWS source).", vaultDropletTag, string(s.Seal), vaultDropletVersion)
	w("set -euo pipefail")
	w("export DEBIAN_FRONTEND=noninteractive")
	w(`log() { echo "[vault-bootstrap] $*"; }`)
	w("")
	w("log \"apt deps + hashicorp repo\"")
	w("apt-get update -y")
	w("apt-get install -y ca-certificates curl gnupg lsb-release openssl jq")
	w("install -m 0755 -d /etc/apt/keyrings")
	w("curl -fsSL https://apt.releases.hashicorp.com/gpg | gpg --dearmor -o /etc/apt/keyrings/hashicorp.gpg")
	w(`echo "deb [signed-by=/etc/apt/keyrings/hashicorp.gpg] https://apt.releases.hashicorp.com $(lsb_release -cs) main" > /etc/apt/sources.list.d/hashicorp.list`)
	w("apt-get update -y")
	w("# Pin the version to match the AWS source Vault so raft snapshot restore is clean.")
	w("apt-get install -y vault=%s || apt-get install -y vault", vaultDropletVersion)
	w("")
	w("log \"mount data volume at /opt/vault/data\"")
	w("DATA_DEV=$(ls /dev/disk/by-id/*pyx*vault*data* 2>/dev/null | head -n1 || true)")
	w(`if [ -z "$DATA_DEV" ]; then DATA_DEV=$(lsblk -dpno NAME,TYPE | awk '$2=="disk"{print $1}' | grep -v vda | head -n1); fi`)
	w("mkdir -p /opt/vault/data")
	w(`if [ -n "$DATA_DEV" ] && ! blkid "$DATA_DEV" >/dev/null 2>&1; then mkfs.ext4 -F "$DATA_DEV"; fi`)
	w(`if [ -n "$DATA_DEV" ]; then grep -q "$DATA_DEV" /etc/fstab || echo "$DATA_DEV /opt/vault/data ext4 defaults,nofail 0 0" >> /etc/fstab; mount -a || true; fi`)
	w("chown -R vault:vault /opt/vault/data")
	w("")
	w("log \"self-signed internal TLS cert (Vault listener 8200)\"")
	w("mkdir -p /opt/vault/tls")
	w("PRIV_IP=$(curl -fsSL http://169.254.169.254/metadata/v1/interfaces/private/0/ipv4/address || hostname -I | awk '{print $1}')")
	w(`if [ ! -f /opt/vault/tls/vault.crt ]; then`)
	w(`  openssl req -x509 -nodes -newkey rsa:2048 -days 3650 \`)
	w(`    -keyout /opt/vault/tls/vault.key -out /opt/vault/tls/vault.crt \`)
	w(`    -subj "/CN=%s" -addext "subjectAltName=DNS:%s,IP:$PRIV_IP,IP:127.0.0.1"`, s.Name, s.Name)
	w("fi")
	w("chown -R vault:vault /opt/vault/tls")
	w("")
	w("log \"write /etc/vault.d/vault.hcl\"")
	w("mkdir -p /etc/vault.d")
	w("umask 027")
	// Heredoc quoted so the shell does not expand ${...}; PRIV_IP is substituted via
	// a sed pass after the heredoc so the node advertises its real private address.
	w("cat > /etc/vault.d/vault.hcl <<'VAULTHCL'")
	w("ui = true")
	// Vault >= 1.20 REFUSES to start unless disable_mlock is set explicitly
	// (older 1.15.x defaulted it to false). The apt pin below falls back to the
	// latest Vault when the pinned version is gone from the repo, so a fresh
	// droplet can boot a >= 1.20 binary — without this line it crash-loops with
	// "disable_mlock must be configured 'true' or 'false'". true is correct on
	// raft/integrated-storage droplets: enabling mlock would also require
	// CAP_IPC_LOCK + LimitMEMLOCK in the systemd unit (not set here).
	w("disable_mlock = true")
	w("cluster_addr = \"https://__PRIV_IP__:%d\"", vaultClusterPort)
	w("api_addr     = \"https://__PRIV_IP__:%d\"", vaultListenerPort)
	w("")
	w("listener \"tcp\" {")
	w("  address       = \"0.0.0.0:%d\"", vaultListenerPort)
	w("  tls_cert_file = \"/opt/vault/tls/vault.crt\"")
	w("  tls_key_file  = \"/opt/vault/tls/vault.key\"")
	w("}")
	w("")
	w("storage \"raft\" {")
	w("  path    = \"/opt/vault/data\"")
	w("  node_id = %q", nodeID)
	w("  # Cloud-auto-join: discover the other Vault nodes by DO tag (no baked IPs).")
	w("  retry_join {")
	w("    auto_join        = \"provider=digitalocean region=%s tag_name=%s\"", s.Region, vaultDropletTag)
	w("    auto_join_scheme = \"https\"")
	w("    leader_tls_servername = %q", s.Name)
	w("    leader_ca_cert_file   = \"/opt/vault/tls/vault.crt\"")
	w("  }")
	w("}")
	w("")
	w(renderSealStanza(s))
	w("VAULTHCL")
	w("sed -i \"s/__PRIV_IP__/$PRIV_IP/g\" /etc/vault.d/vault.hcl")
	w("chown -R vault:vault /etc/vault.d")
	w("umask 022")
	w("")
	// The auto-join go-discover DO plugin needs a DO API token to query the tag.
	w("log \"DO auto-join needs a DO API read token in the vault env\"")
	w("mkdir -p /etc/systemd/system/vault.service.d")
	w("cat > /etc/systemd/system/vault.service.d/10-pyx.conf <<'DROPIN'")
	w("[Service]")
	if s.Seal == VaultSealTransit {
		w("Environment=VAULT_TRANSIT_SEAL_TOKEN=%s", s.TransitToken)
	}
	// DIGITALOCEAN_TOKEN is required by the go-discover DO provider for tag auto-join.
	// Injected at render time (read-scoped token); NOT stored in tf state.
	w("Environment=DIGITALOCEAN_TOKEN=${DIGITALOCEAN_TOKEN}")
	w("DROPIN")
	w("")
	w("log \"enable + start vault\"")
	w("systemctl daemon-reload")
	w("systemctl enable vault")
	w("systemctl restart vault")
	w("log \"node up (seal=%s) — 'vault operator init' on node-1 OR raft snapshot restore (Phase 2); peers auto-join by tag\"", string(s.Seal))
	if s.Seal == VaultSealShamir {
		w("log \"seal=shamir: MANUAL unseal required after every restart/reboot (3-of-5 key shares held by the owner; runbook in the secrets-manager repo)\"")
	}

	return strings.TrimRight(b.String(), "\n")
}

// renderSealStanza returns the seal { } HCL for the configured mode. Making this a
// pure function of the seal mode keeps a future seal change a single enum flip
// here, not a rewrite.
func renderSealStanza(s VaultDropletSpec) string {
	switch s.Seal {
	case VaultSealTransit:
		// End-state: auto-unseal against the separate unseal-Vault's transit engine.
		return fmt.Sprintf(`# END-STATE seal: transit auto-unseal against the unseal-Vault (no cloud KMS).
seal "transit" {
  address    = %q
  # token supplied via VAULT_TRANSIT_SEAL_TOKEN env (never in state).
  mount_path = "transit/"
  key_name   = %q
}`, s.TransitAddr, s.TransitKeyName)
	default:
		// Shamir (the default): no seal stanza. MANUAL unseal required post-reboot
		// (3-of-5 key shares held by the owner) — see the unseal runbook in the
		// secrets-manager repo.
		return "# seal: shamir (default) — no seal stanza. Manual unseal required post-reboot\n" +
			"# (3-of-5 key shares held by the owner); see the unseal runbook in the secrets-manager repo."
	}
}
