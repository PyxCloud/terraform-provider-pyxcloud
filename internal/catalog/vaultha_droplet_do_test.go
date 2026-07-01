package catalog

import (
	"context"
	"strings"
	"testing"
)

func testVaultKMSSpec() VaultDropletSpec {
	return VaultDropletSpec{
		Name:           "pyx-vault",
		Region:         "fra1",
		Size:           "s-2vcpu-4gb",
		Image:          "ubuntu-24-04-x64",
		VPCRef:         "digitalocean_vpc.passo-do-baseline-net.id",
		Seal:           VaultSealAWSKMS,
		KMSKeyID:       "arn:aws:kms:eu-west-1:111:key/abc",
		KMSRegion:      "eu-west-1",
		AWSAccessKeyID: "AKIATEST",
		AWSSecretKey:   "SECRETTEST",
	}
}

// TestVaultDropletRendersThreeRaftNodes is the core Phase-0 contract: 3 fixed
// droplet nodes, a data volume each, raft storage with distinct node_ids, and
// cloud-auto-join retry_join (no hardcoded peer IPs).
func TestVaultDropletRendersThreeRaftNodes(t *testing.T) {
	docs, err := RenderVaultDropletCluster(testVaultKMSSpec())
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
}

// TestVaultDropletPrivateOnlyFirewall asserts the firewall keeps 8200/8201 on the
// VPC only — no public 0.0.0.0/0 exposure on the Vault API.
func TestVaultDropletPrivateOnlyFirewall(t *testing.T) {
	docs, err := RenderVaultDropletCluster(testVaultKMSSpec())
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
// the seal mode (the Phase-6 KMS->Transit flip = one enum change).
func TestVaultDropletSealConfigurable(t *testing.T) {
	// AWS KMS bridge.
	kms, err := RenderVaultDropletCluster(testVaultKMSSpec())
	if err != nil {
		t.Fatalf("kms: %v", err)
	}
	kj := strings.Join(kms, "\n")
	if !strings.Contains(kj, `seal "awskms"`) {
		t.Errorf("awskms mode must render seal \"awskms\"")
	}
	if !strings.Contains(kj, `kms_key_id = "arn:aws:kms:eu-west-1:111:key/abc"`) {
		t.Errorf("awskms seal must carry the existing KMS key id")
	}
	if strings.Contains(kj, `seal "transit"`) {
		t.Errorf("awskms mode must not render a transit seal")
	}
	// AWS creds injected via the systemd drop-in (no AWS role on a DO box).
	if !strings.Contains(kj, "AWS_ACCESS_KEY_ID=AKIATEST") {
		t.Errorf("awskms mode must inject AWS creds for the seal")
	}

	// Transit end-state — a pure config flip.
	tspec := testVaultKMSSpec()
	tspec.Seal = VaultSealTransit
	tspec.KMSKeyID, tspec.KMSRegion = "", ""
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

	// Shamir — no seal stanza.
	sspec := testVaultKMSSpec()
	sspec.Seal = VaultSealShamir
	sspec.KMSKeyID, sspec.KMSRegion = "", ""
	sh, err := RenderVaultDropletCluster(sspec)
	if err != nil {
		t.Fatalf("shamir: %v", err)
	}
	shj := strings.Join(sh, "\n")
	if strings.Contains(shj, `seal "awskms"`) || strings.Contains(shj, `seal "transit"`) {
		t.Errorf("shamir mode must render no auto-unseal seal stanza")
	}
}

// TestVaultDropletValidation covers the guard rails.
func TestVaultDropletValidation(t *testing.T) {
	// awskms without a key id must fail loudly.
	bad := testVaultKMSSpec()
	bad.KMSKeyID = ""
	if _, err := RenderVaultDropletCluster(bad); err == nil {
		t.Errorf("expected error for awskms seal without KMS key id")
	}
	// transit without addr/token must fail loudly.
	bad2 := VaultDropletSpec{Name: "v", Region: "fra1", Size: "s", Image: "i", VPCRef: "x", Seal: VaultSealTransit}
	if _, err := RenderVaultDropletCluster(bad2); err == nil {
		t.Errorf("expected error for transit seal without addr/token")
	}
	// missing VPC ref must fail (private-only invariant).
	bad3 := testVaultKMSSpec()
	bad3.VPCRef = ""
	if _, err := RenderVaultDropletCluster(bad3); err == nil {
		t.Errorf("expected error for missing VPCRef")
	}
	// unknown seal mode must fail.
	bad4 := testVaultKMSSpec()
	bad4.Seal = VaultSealMode("nope")
	if _, err := RenderVaultDropletCluster(bad4); err == nil {
		t.Errorf("expected error for unknown seal mode")
	}
}

// TestVaultDropletReservedIPs asserts reserved IPs are opt-in.
func TestVaultDropletReservedIPs(t *testing.T) {
	off, _ := RenderVaultDropletCluster(testVaultKMSSpec())
	if strings.Contains(strings.Join(off, "\n"), "digitalocean_reserved_ip") {
		t.Errorf("reserved IPs must be off by default")
	}
	spec := testVaultKMSSpec()
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
	a, _ := RenderVaultDropletCluster(testVaultKMSSpec())
	b, _ := RenderVaultDropletCluster(testVaultKMSSpec())
	if strings.Join(a, "\n") != strings.Join(b, "\n") {
		t.Errorf("render is not deterministic")
	}
}

// --- baseline wiring: flag-gated, off by default ---

// TestDOBaselineVaultHAFlagGated asserts the baseline is UNCHANGED when VaultHA is
// off, and grows the 3-node Vault cluster when on.
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
			Seal:      VaultSealAWSKMS,
			KMSKeyID:  "arn:aws:kms:eu-west-1:111:key/abc",
			KMSRegion: "eu-west-1",
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
	if !strings.Contains(onJoined, `seal "awskms"`) {
		t.Errorf("baseline vault must render the configured seal stanza")
	}
	// On must strictly extend off (baseline resources still present).
	if len(on) <= len(off) {
		t.Errorf("VaultHA on must append resources to the baseline")
	}
}
