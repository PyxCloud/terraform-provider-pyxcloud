package catalog

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderStagingTLSUsesExistingInMemoryVaultToken(t *testing.T) {
	got, err := RenderStagingTLSBootstrap(StagingTLSBootstrapSpec{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"/usr/local/sbin/staging-tls-issue",
		`test -n "${VAULT_TOKEN:-}"`,
		"/v1/secret/data/infra/staging/staging-tls/cloudflare",
		"printf 'dns_cloudflare_api_token = %s",
		"certbot certonly --non-interactive --agree-tos --dns-cloudflare",
		"shred -u /run/staging-tls/cloudflare.ini",
		"/usr/local/sbin/staging-tls-check",
		"openssl x509 -checkend 604800",
		"OnCalendar=*-*-* 03:17:00",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("TLS bootstrap missing %q", want)
		}
	}
	for _, forbidden := range []string{"auth/approle/login", "VaultBootFetchSnippet", "vault_secret_id", "staging-tls-renew", "systemctl start staging-tls"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("TLS bootstrap must not contain %q", forbidden)
		}
	}
}

func TestRenderStagingTLSRejectsUnsafeInputs(t *testing.T) {
	for _, spec := range []StagingTLSBootstrapSpec{
		{Domains: []string{"staging.passo.build; touch /tmp/pwned"}},
		{ACMEEmail: "ops@example.com --bad"},
		{VaultKVPath: "../secret"},
		{VaultKey: "token;bad"},
	} {
		if _, err := RenderStagingTLSBootstrap(spec); err == nil {
			t.Fatalf("expected unsafe spec to fail: %#v", spec)
		}
	}
}

func TestRenderStagingTLSBashParses(t *testing.T) {
	got, err := RenderStagingTLSBootstrap(StagingTLSBootstrapSpec{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	path := filepath.Join(t.TempDir(), "tls.sh")
	if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("bash", "-n", path).CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
}
