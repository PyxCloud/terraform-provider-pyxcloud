package catalog

import "testing"

func TestTranslateKMSAWS(t *testing.T) {
	plan, err := TranslateKMS(KMSSpec{
		Name:     "vault",
		Provider: "aws",
		Keys: []KMSKeySpec{
			{Alias: "vault-unseal", Description: "Vault auto-unseal", EnableRotation: true},
		},
	})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	hcl, err := RenderKMSHCL(plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"resource \"aws_kms_key\"",
		"enable_key_rotation = true",
		"resource \"aws_kms_alias\"",
		"name          = \"alias/vault-unseal\"",
		"target_key_id = aws_kms_key.",
	} {
		if !contains(hcl, want) {
			t.Errorf("KMS HCL missing %q\n---\n%s", want, hcl)
		}
	}
}

func TestTranslateKMSValidation(t *testing.T) {
	if _, err := TranslateKMS(KMSSpec{Provider: "aws", Keys: []KMSKeySpec{{Alias: "a"}}}); err == nil {
		t.Error("expected error: name required")
	}
	if _, err := TranslateKMS(KMSSpec{Name: "x", Provider: "aws"}); err == nil {
		t.Error("expected error: need at least one key")
	}
	if _, err := TranslateKMS(KMSSpec{Name: "x", Provider: "aws", Keys: []KMSKeySpec{{Description: "no alias"}}}); err == nil {
		t.Error("expected error: key needs alias")
	}
	if _, err := TranslateKMS(KMSSpec{Name: "x", Provider: "digitalocean", Keys: []KMSKeySpec{{Alias: "a"}}}); err == nil {
		t.Error("expected error: unsupported provider")
	}
}
