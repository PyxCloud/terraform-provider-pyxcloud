package catalog

import (
	"strings"
	"testing"
)

// TestVaultKVDataSourceHCLShape asserts the render/apply-time Vault data-source
// helper emits a KV-v2 read scoped to the `secret` mount at the given path
// (no data/ infix — the provider adds it) and a deterministic, valid HCL
// label derived from the path.
func TestVaultKVDataSourceHCLShape(t *testing.T) {
	t.Parallel()
	doc, label := VaultKVDataSourceHCL("infra/staging/do/spaces-keys")

	if label != "infra_staging_do_spaces_keys" {
		t.Errorf("unexpected label: got %q", label)
	}
	mustContain := []string{
		`data "vault_kv_secret_v2" "infra_staging_do_spaces_keys"`,
		`mount = "secret"`,
		`name  = "infra/staging/do/spaces-keys"`,
	}
	for _, want := range mustContain {
		if !strings.Contains(doc, want) {
			t.Errorf("vault kv data source doc missing %q:\n%s", want, doc)
		}
	}
	// No data/ infix baked into `name` (the data source itself adds it).
	if strings.Contains(doc, `name  = "data/`) {
		t.Errorf("vault kv data source must not bake the data/ infix into name:\n%s", doc)
	}
}

// TestVaultKVRefShape asserts the reference expression interpolates the
// data source's .data[<key>] attribute, ready to embed directly into a bash
// heredoc the way sso/sast now do for their render-time-injected secrets.
func TestVaultKVRefShape(t *testing.T) {
	t.Parallel()
	ref := VaultKVRef("infra_staging_do_spaces_keys", "access_key")
	want := `${data.vault_kv_secret_v2.infra_staging_do_spaces_keys.data["access_key"]}`
	if ref != want {
		t.Errorf("VaultKVRef: got %q, want %q", ref, want)
	}
}

// TestVaultProviderBlockShape asserts the shared vault provider/required_providers
// block pins hashicorp/vault ~> 4.0 and does not hardcode any address/token.
func TestVaultProviderBlockShape(t *testing.T) {
	t.Parallel()
	block := VaultProviderBlock()
	mustContain := []string{
		`source  = "hashicorp/vault"`,
		`version = "~> 4.0"`,
		`provider "vault" {}`,
	}
	for _, want := range mustContain {
		if !strings.Contains(block, want) {
			t.Errorf("vault provider block missing %q:\n%s", want, block)
		}
	}
}
