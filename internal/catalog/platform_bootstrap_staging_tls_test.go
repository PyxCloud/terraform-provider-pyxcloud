package catalog

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderStagingTLSBootstrapUsesRuntimeVaultDNS01AndStrictTrust(t *testing.T) {
	t.Parallel()

	got, err := RenderStagingTLSBootstrap(StagingTLSBootstrapSpec{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	wants := []string{
		"python3-certbot-dns-cloudflare",
		"auth/approle/login",
		"/v1/secret/data/infra/staging/staging-tls/cloudflare",
		"dns_cloudflare_api_token = $CLOUDFLARE_DNS_API_TOKEN",
		"certbot certonly --non-interactive --agree-tos --dns-cloudflare",
		"-d staging.passo.build -d staging-console.passo.build",
		"test -s /etc/letsencrypt/live/staging.passo.build/fullchain.pem",
		"test -s /etc/letsencrypt/live/staging.passo.build/privkey.pem",
		"shred -u /run/staging-tls/cloudflare.ini",
		"unset CLOUDFLARE_DNS_API_TOKEN VAULT_TOKEN",
		"systemctl reload nginx",
		"OnCalendar=*-*-* 03:17:00",
	}
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("rendered bootstrap missing %q", want)
		}
	}

	forbidden := []string{
		"openssl req -x509",
		"self-signed",
		"--standalone",
		"--webroot",
		"cloudflare_api_token = ${var.",
		"CLOUDFLARE_DNS_API_TOKEN=${var.",
	}
	for _, bad := range forbidden {
		if strings.Contains(got, bad) {
			t.Errorf("rendered bootstrap must not contain %q", bad)
		}
	}
}

func TestRenderStagingTLSBootstrapContainsOnlySecretReferences(t *testing.T) {
	t.Parallel()

	got, err := RenderStagingTLSBootstrap(StagingTLSBootstrapSpec{
		VaultAddrVar:     "tls_vault_addr",
		VaultRoleIDVar:   "tls_vault_role_id",
		VaultSecretIDVar: "tls_vault_secret_id",
		VaultKVPath:      "infra/staging/certificates/cloudflare",
		VaultKey:         "restricted_dns_token",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	for _, ref := range []string{"${var.tls_vault_addr}", "${var.tls_vault_role_id}", "${var.tls_vault_secret_id}"} {
		if !strings.Contains(got, ref) {
			t.Errorf("rendered bootstrap missing reference %q", ref)
		}
	}
	if !strings.Contains(got, "/v1/secret/data/infra/staging/certificates/cloudflare") || !strings.Contains(got, "restricted_dns_token") {
		t.Fatal("rendered bootstrap does not use the configured runtime Vault leaf/key")
	}
	if strings.Contains(got, "data.vault_kv_secret") {
		t.Fatal("DNS credential must not be read by a Terraform Vault data source")
	}
}

func TestRenderStagingTLSBootstrapRejectsUnsafeInputs(t *testing.T) {
	t.Parallel()

	bad := []StagingTLSBootstrapSpec{
		{Domains: []string{"staging.passo.build; touch /tmp/pwned"}},
		{ACMEEmail: "ops@example.com --deploy-hook bad"},
		{VaultKVPath: "../secret"},
		{VaultKey: "token;bad"},
	}
	for _, spec := range bad {
		if _, err := RenderStagingTLSBootstrap(spec); err == nil {
			t.Fatalf("expected unsafe spec to fail: %#v", spec)
		}
	}
}

func TestRenderStagingTLSBootstrapBashParses(t *testing.T) {
	t.Parallel()

	got, err := RenderStagingTLSBootstrap(StagingTLSBootstrapSpec{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	path := filepath.Join(t.TempDir(), "staging-tls.sh")
	if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if out, err := exec.Command("bash", "-n", path).CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
}
