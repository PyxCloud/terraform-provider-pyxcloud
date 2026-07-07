package catalog

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func testVaultShamirSpec() VaultDropletSpec {
	return VaultDropletSpec{
		Name:   "pyx-vault",
		Region: "fra1",
		Size:   "s-2vcpu-4gb",
		Image:  "ubuntu-24-04-x64",
		VPCRef: "digitalocean_vpc.passo-do-baseline-net.id",
		Seal:   VaultSealShamir,
	}
}

// TestVaultDropletRendersThreeRaftNodes is the core Phase-0 contract: 3 fixed
// droplet nodes, a data volume each, raft storage with distinct node_ids, and
// cloud-auto-join retry_join (no hardcoded peer IPs).
func TestVaultDropletRendersThreeRaftNodes(t *testing.T) {
	docs, err := RenderVaultDropletCluster(testVaultShamirSpec())
	if err != nil {
		t.Fatalf("RenderVaultDropletCluster: %v", err)
	}
	joined := strings.Join(docs, "\n")

	for i := 1; i <= 3; i++ {
		if !strings.Contains(joined, "resource \"digitalocean_droplet\" \"pyx-vault-"+itoa(i)+"\"") {
			t.Errorf("missing vault droplet node %d", i)
		}
		if !strings.Contains(joined, "resource \"digitalocean_volume\" \"pyx-vault-"+itoa(i)+"-data\"") {
			t.Errorf("missing vault data volume for node %d", i)
		}
		if !strings.Contains(joined, `node_id = "vault-node-`+itoa(i)+`"`) {
			t.Errorf("missing raft node_id for node %d", i)
		}
	}
	if n := strings.Count(joined, `resource "digitalocean_droplet"`); n != 3 {
		t.Errorf("expected exactly 3 droplet nodes (raft quorum), got %d", n)
	}
	// Raft integrated storage + tag-based auto-join (chicken/egg solved, no baked IP).
	if !strings.Contains(joined, `storage "raft"`) {
		t.Errorf("expected raft integrated storage")
	}
	if !strings.Contains(joined, `auto_join        = "provider=digitalocean region=fra1 tag_name=pyx-vault"`) {
		t.Errorf("expected cloud-auto-join retry_join by DO tag")
	}
	if !strings.Contains(joined, "retry_join") {
		t.Errorf("expected retry_join stanza")
	}
	// Vault version must match the AWS source for a clean snapshot restore.
	if !strings.Contains(joined, "1.15.6") {
		t.Errorf("expected Vault 1.15.6 (matches AWS source for snapshot restore)")
	}
	// :8200 listener present.
	if !strings.Contains(joined, `address       = "0.0.0.0:8200"`) {
		t.Errorf("expected :8200 listener")
	}
	// disable_mlock MUST be explicit: Vault >= 1.20 refuses to start without it,
	// and the apt pin falls back to the latest binary when the pinned version is
	// gone from the repo (this crash-looped the whole staging estate on
	// 2026-07-07). true is correct on raft droplets (no CAP_IPC_LOCK in the unit).
	if !strings.Contains(joined, "disable_mlock = true") {
		t.Errorf("expected explicit disable_mlock = true (>= 1.20 refuses to boot without it)")
	}
}

// TestVaultDropletPrivateOnlyFirewall asserts the firewall keeps 8200/8201 on the
// VPC only — no public 0.0.0.0/0 exposure on the Vault API.
func TestVaultDropletPrivateOnlyFirewall(t *testing.T) {
	docs, err := RenderVaultDropletCluster(testVaultShamirSpec())
	if err != nil {
		t.Fatalf("RenderVaultDropletCluster: %v", err)
	}
	joined := strings.Join(docs, "\n")
	if !strings.Contains(joined, `resource "digitalocean_firewall" "pyx-vault-sg"`) {
		t.Errorf("expected vault firewall")
	}
	if !strings.Contains(joined, `port_range       = "8200"`) {
		t.Errorf("expected 8200 inbound rule")
	}
	// The 8200 inbound must be VPC-scoped, never 0.0.0.0/0.
	fwStart := strings.Index(joined, `resource "digitalocean_firewall"`)
	fwBody := joined[fwStart:]
	if idx := strings.Index(fwBody, "}\nresource"); idx >= 0 {
		fwBody = fwBody[:idx]
	}
	if strings.Contains(fwBody, `source_addresses = ["0.0.0.0/0", "::/0"]`) {
		t.Errorf("vault firewall must NOT expose 8200 publicly (private-only)")
	}
	if !strings.Contains(fwBody, `source_addresses = ["10.0.1.0/24"]`) {
		t.Errorf("vault firewall inbound must be VPC-scoped")
	}
}

// TestVaultDropletSealConfigurable asserts the seal stanza is a pure function of
// the seal mode. Shamir (the default) emits no auto-unseal stanza and no AWS
// KMS material anywhere in the render (owner decision 2026-07-07: the AWS-KMS
// bridge has been retired); transit remains a config flip for a future opt-in.
func TestVaultDropletSealConfigurable(t *testing.T) {
	// Shamir — the default; no seal stanza, no AWS/KMS material anywhere.
	sh, err := RenderVaultDropletCluster(testVaultShamirSpec())
	if err != nil {
		t.Fatalf("shamir: %v", err)
	}
	shj := strings.Join(sh, "\n")
	if strings.Contains(shj, `seal "awskms"`) || strings.Contains(shj, `seal "transit"`) {
		t.Errorf("shamir mode must render no auto-unseal seal stanza")
	}
	if strings.Contains(shj, "awskms") || strings.Contains(shj, "AWS_ACCESS_KEY_ID") || strings.Contains(shj, "AWS_SECRET_ACCESS_KEY") {
		t.Errorf("shamir mode must not reference AWS KMS / AWS creds at all, got:\n%s", shj)
	}

	// Empty Seal must default to shamir (same assertions as explicit shamir).
	defSpec := testVaultShamirSpec()
	defSpec.Seal = ""
	def, err := RenderVaultDropletCluster(defSpec)
	if err != nil {
		t.Fatalf("default: %v", err)
	}
	defj := strings.Join(def, "\n")
	if strings.Contains(defj, `seal "awskms"`) || strings.Contains(defj, `seal "transit"`) {
		t.Errorf("default (empty) seal must render no auto-unseal seal stanza (shamir)")
	}

	// Transit — a config flip, still available.
	tspec := testVaultShamirSpec()
	tspec.Seal = VaultSealTransit
	tspec.TransitAddr = "https://unseal-vault.internal:8200"
	tspec.TransitToken = "s.transittoken"
	tr, err := RenderVaultDropletCluster(tspec)
	if err != nil {
		t.Fatalf("transit: %v", err)
	}
	trj := strings.Join(tr, "\n")
	if !strings.Contains(trj, `seal "transit"`) {
		t.Errorf("transit mode must render seal \"transit\"")
	}
	if !strings.Contains(trj, `address    = "https://unseal-vault.internal:8200"`) {
		t.Errorf("transit seal must point at the unseal-Vault")
	}
	if strings.Contains(trj, `seal "awskms"`) {
		t.Errorf("transit mode must not render an awskms seal")
	}
	// The transit token must NOT appear as an HCL attribute (out-of-band env only).
	if strings.Contains(trj, `token      = "s.transittoken"`) {
		t.Errorf("transit token must not be written into vault.hcl")
	}
}

// TestVaultDropletValidation covers the guard rails.
func TestVaultDropletValidation(t *testing.T) {
	// transit without addr/token must fail loudly.
	bad2 := VaultDropletSpec{Name: "v", Region: "fra1", Size: "s", Image: "i", VPCRef: "x", Seal: VaultSealTransit}
	if _, err := RenderVaultDropletCluster(bad2); err == nil {
		t.Errorf("expected error for transit seal without addr/token")
	}
	// missing VPC ref must fail (private-only invariant).
	bad3 := testVaultShamirSpec()
	bad3.VPCRef = ""
	if _, err := RenderVaultDropletCluster(bad3); err == nil {
		t.Errorf("expected error for missing VPCRef")
	}
	// unknown seal mode must fail — including the retired "awskms" token.
	bad4 := testVaultShamirSpec()
	bad4.Seal = VaultSealMode("nope")
	if _, err := RenderVaultDropletCluster(bad4); err == nil {
		t.Errorf("expected error for unknown seal mode")
	}
	bad5 := testVaultShamirSpec()
	bad5.Seal = VaultSealMode("awskms")
	if _, err := RenderVaultDropletCluster(bad5); err == nil {
		t.Errorf("expected error for the retired awskms seal mode")
	}
}

// TestVaultDropletReservedIPs asserts reserved IPs are opt-in.
func TestVaultDropletReservedIPs(t *testing.T) {
	off, _ := RenderVaultDropletCluster(testVaultShamirSpec())
	if strings.Contains(strings.Join(off, "\n"), "digitalocean_reserved_ip") {
		t.Errorf("reserved IPs must be off by default")
	}
	spec := testVaultShamirSpec()
	spec.ReservedIPs = true
	on, err := RenderVaultDropletCluster(spec)
	if err != nil {
		t.Fatalf("reserved: %v", err)
	}
	if n := strings.Count(strings.Join(on, "\n"), `resource "digitalocean_reserved_ip"`); n != 3 {
		t.Errorf("expected 3 reserved IPs (one per node), got %d", n)
	}
}

// TestVaultDropletDeterministic asserts byte-stable output.
func TestVaultDropletDeterministic(t *testing.T) {
	a, _ := RenderVaultDropletCluster(testVaultShamirSpec())
	b, _ := RenderVaultDropletCluster(testVaultShamirSpec())
	if strings.Join(a, "\n") != strings.Join(b, "\n") {
		t.Errorf("render is not deterministic")
	}
}

// --- baseline wiring: flag-gated, off by default ---

// --- Mode-A (pyxcloud_environment) wiring: AssembleInput.VaultHADroplet ---

// TestAssembleHCLVaultHADropletOffByDefault asserts an environment with no
// vault_ha config renders no Vault resources at all (0 change to existing
// Mode-A users).
func TestAssembleHCLVaultHADropletOffByDefault(t *testing.T) {
	docs, err := AssembleHCL(context.Background(), MustEmbedded(), AssembleInput{
		Name: "prod", Provider: "digitalocean", Region: "Frankfurt",
	})
	if err != nil {
		t.Fatalf("AssembleHCL: %v", err)
	}
	all := strings.Join(docs, "\n")
	if strings.Contains(all, "pyx-vault") || strings.Contains(all, `storage "raft"`) {
		t.Errorf("vault_ha unset must NOT add any Vault resources, got:\n%s", all)
	}
}

// TestAssembleHCLVaultHADropletProducesThreeNodeCluster is the core Mode-A
// plumbing contract: a pyxcloud_environment with vault_ha set renders the SAME
// 3-droplet + firewall + shamir (no seal stanza) HCL as the Mode-B baseline
// assembler, reusing the environment's own VPC (name-net) and DO tag.
func TestAssembleHCLVaultHADropletProducesThreeNodeCluster(t *testing.T) {
	docs, err := AssembleHCL(context.Background(), MustEmbedded(), AssembleInput{
		Name: "prod", Provider: "digitalocean", Region: "Frankfurt",
		VaultHADroplet: &AssembleVaultHADroplet{
			Seal: VaultSealShamir,
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL: %v", err)
	}
	all := strings.Join(docs, "\n")

	// The environment's own VPC is created (vault-only env still needs a network)
	// and reused as the vault droplets' vpc_uuid — no separate network.
	if !strings.Contains(all, `resource "digitalocean_vpc" "prod-net"`) {
		t.Errorf("expected the environment's own VPC (prod-net), got:\n%s", all)
	}
	if !strings.Contains(all, "vpc_uuid   = digitalocean_vpc.prod-net.id") {
		t.Errorf("expected vault droplets wired to the environment VPC (prod-net), got:\n%s", all)
	}

	// 3 fixed droplet nodes + a data volume each (the tested Raft quorum shape).
	if n := strings.Count(all, `resource "digitalocean_droplet" "pyx-vault-`); n != 3 {
		t.Errorf("expected exactly 3 vault droplets, got %d\n%s", n, all)
	}
	if n := strings.Count(all, `resource "digitalocean_volume" "pyx-vault-`); n != 3 {
		t.Errorf("expected exactly 3 vault data volumes, got %d\n%s", n, all)
	}
	// The renderer's own private-only firewall, tagged pyx-vault (distinct from
	// the environment SG — this renderer emits its own tags/firewall).
	if !strings.Contains(all, `resource "digitalocean_firewall" "pyx-vault-sg"`) {
		t.Errorf("expected the vault-ha private-only firewall, got:\n%s", all)
	}
	if !strings.Contains(all, `tags       = ["pyx-vault"]`) {
		t.Errorf("expected the pyx-vault tag stamped on every droplet, got:\n%s", all)
	}
	// Raft + cloud-auto-join (no baked peer IPs), and NO auto-unseal seal stanza
	// (shamir: manual unseal, no AWS KMS anywhere).
	if !strings.Contains(all, `storage "raft"`) || !strings.Contains(all, "retry_join") {
		t.Errorf("expected raft storage + retry_join, got:\n%s", all)
	}
	if strings.Contains(all, `seal "awskms"`) || strings.Contains(all, `seal "transit"`) {
		t.Errorf("shamir vault_ha must render no seal stanza, got:\n%s", all)
	}
	if strings.Contains(all, "AWS_ACCESS_KEY_ID") || strings.Contains(all, "AWS_SECRET_ACCESS_KEY") {
		t.Errorf("shamir vault_ha must not inject any AWS creds, got:\n%s", all)
	}
	// do_ssh_keys must be declared (the droplet resource references var.do_ssh_keys)
	// even though no virtual-machine-scale-group component is present.
	if !strings.Contains(all, `variable "do_ssh_keys"`) {
		t.Errorf("expected var.do_ssh_keys to be declared for the vault droplets, got:\n%s", all)
	}
	// digitalocean provider must be pinned (terraform plan requires it once any
	// digitalocean_* resource is present).
	if !strings.Contains(all, `source = "digitalocean/digitalocean"`) {
		t.Errorf("expected the digitalocean provider pinned, got:\n%s", all)
	}
}

// TestAssembleHCLVaultHADropletRejectsNonDO asserts vault_ha is a hard plan-time
// error off DigitalOcean (never a silent no-op/drop).
func TestAssembleHCLVaultHADropletRejectsNonDO(t *testing.T) {
	_, err := AssembleHCL(context.Background(), MustEmbedded(), AssembleInput{
		Name: "prod", Provider: "aws", Region: "Dublin",
		VaultHADroplet: &AssembleVaultHADroplet{Seal: VaultSealShamir},
	})
	if err == nil {
		t.Errorf("expected an error for vault_ha on a non-DO provider")
	}
}

// TestAssembleHCLVaultHADropletRejectsBadNodeCount asserts a node_count other
// than the renderer's supported 1 or 3 nodes is a hard plan-time error.
func TestAssembleHCLVaultHADropletRejectsBadNodeCount(t *testing.T) {
	_, err := AssembleHCL(context.Background(), MustEmbedded(), AssembleInput{
		Name: "prod", Provider: "digitalocean", Region: "Frankfurt",
		VaultHADroplet: &AssembleVaultHADroplet{
			Seal: VaultSealShamir, NodeCount: 5,
		},
	})
	if err == nil {
		t.Errorf("expected an error for node_count != 1 && node_count != 3")
	}
	// node_count == 1 and 3 must be accepted.
	for _, n := range []int{1, 3} {
		_, err = AssembleHCL(context.Background(), MustEmbedded(), AssembleInput{
			Name: fmt.Sprintf("prod-%d", n), Provider: "digitalocean", Region: "Frankfurt",
			VaultHADroplet: &AssembleVaultHADroplet{
				Seal: VaultSealShamir, NodeCount: n,
			},
		})
		if err != nil {
			t.Errorf("node_count=%d must be accepted, got: %v", n, err)
		}
	}
}

// TestDOBaselineVaultHAFlagGated asserts the baseline is UNCHANGED when VaultHA is
// off, and grows the 3-node Vault cluster (shamir, no seal stanza) when on.
func TestDOBaselineVaultHAFlagGated(t *testing.T) {
	in := DOBaselineInput("Frankfurt", "x86_64", "ubuntu", "1.30")
	secrets := testDOBaselineSecrets()

	off, err := AssembleDOBaseline(context.Background(), MustEmbedded(), in, secrets, DOBaselineOptions{PrivateDBHost: true})
	if err != nil {
		t.Fatalf("baseline off: %v", err)
	}
	offJoined := strings.Join(off, "\n")
	if strings.Contains(offJoined, "pyx-vault") || strings.Contains(offJoined, `storage "raft"`) {
		t.Errorf("VaultHA off must NOT add any Vault resources (0 change to base estate)")
	}

	on, err := AssembleDOBaseline(context.Background(), MustEmbedded(), in, secrets, DOBaselineOptions{
		PrivateDBHost: true,
		VaultHA:       true,
		VaultHASpec: VaultDropletSpec{
			Seal: VaultSealShamir,
		},
	})
	if err != nil {
		t.Fatalf("baseline on: %v", err)
	}
	onJoined := strings.Join(on, "\n")
	// 3 vault droplets appended, sized via the catalog SKU resolver.
	if n := strings.Count(onJoined, `resource "digitalocean_droplet" "pyx-vault-`); n != 3 {
		t.Errorf("expected 3 vault droplets in baseline, got %d", n)
	}
	if !strings.Contains(onJoined, `storage "raft"`) || !strings.Contains(onJoined, "retry_join") {
		t.Errorf("baseline vault nodes must use raft + retry_join")
	}
	if strings.Contains(onJoined, `seal "awskms"`) || strings.Contains(onJoined, `seal "transit"`) {
		t.Errorf("baseline vault (shamir) must render no seal stanza")
	}
	// On must strictly extend off (baseline resources still present).
	if len(on) <= len(off) {
		t.Errorf("VaultHA on must append resources to the baseline")
	}
}
