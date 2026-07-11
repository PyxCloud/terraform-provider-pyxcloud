package catalog

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderStagingFEConvergesFromWrappedTokenAndDeliveredArtifact(t *testing.T) {
	ud, err := RenderStagingFEDOBootstrapUserData(StagingFEDOBootstrapSpec{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"/usr/local/sbin/staging-fe-converge",
		"IFS= read -r WRAPPED_TOKEN",
		"/v1/sys/wrapping/unwrap",
		"/v1/auth/approle/login",
		"/var/lib/staging-fe/inbox/standalone.tar.gz",
		"sha256sum --check",
		"/v1/secret/data/infra/staging/staging-fe/runtime",
		"ExecStart=/usr/bin/node server.js",
		"proxy_pass http://127.0.0.1:3000",
		"INTERNAL_AUTH_URL=https://staging-auth.pyxcloud.io/realms/passobuild/",
		"PYX_MCP_URL=https://staging-mcp.passo.build/mcp",
	} {
		if !strings.Contains(ud, want) {
			t.Errorf("secure bootstrap missing %q", want)
		}
	}
}

func TestRenderStagingFEUserDataContainsNoBootstrapSecrets(t *testing.T) {
	ud, err := RenderStagingFEDOBootstrapUserData(StagingFEDOBootstrapSpec{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, forbidden := range []string{
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "aws s3 cp", "do_spaces_access_key",
		"do_spaces_secret_key", "staging_fe_vault_secret_id", "VAULT_SECRET_ID='${var.",
		"amplifyapp.com", "next build", "npm install",
	} {
		if strings.Contains(ud, forbidden) {
			t.Errorf("user_data must not contain %q", forbidden)
		}
	}
	plain, sensitive := (StagingFEDOBootstrapSpec{}).StagingFEDOBootstrapVariableNames()
	if strings.Join(sensitive, ",") != "" {
		t.Fatalf("staging FE must declare no sensitive Terraform bootstrap vars, got %v", sensitive)
	}
	wantPlain := map[string]bool{
		"staging_fe_artifact_key": true, "staging_fe_artifact_sha256": true,
		"vault_addr": true, "staging_fe_vault_role_id": true,
	}
	for _, name := range plain {
		delete(wantPlain, name)
	}
	if len(wantPlain) != 0 {
		t.Fatalf("missing non-secret bootstrap vars: %v", wantPlain)
	}
}

func TestRenderStagingFEIsFailClosedUntilConverged(t *testing.T) {
	ud, err := RenderStagingFEDOBootstrapUserData(StagingFEDOBootstrapSpec{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"systemctl disable staging-fe.service nginx",
		"systemctl stop staging-fe.service nginx",
		"systemctl enable staging-fe.service nginx",
		"systemctl restart staging-fe.service",
		"systemctl restart nginx",
		"rm -f /var/lib/staging-fe/inbox/standalone.tar.gz",
	} {
		if !strings.Contains(ud, want) {
			t.Errorf("fail-closed bootstrap missing %q", want)
		}
	}
	if strings.Contains(ud, "systemctl enable --now staging-fe.service nginx") {
		t.Fatal("cloud-init must not start staging before out-of-band convergence")
	}
}

func TestRenderStagingFEBashParses(t *testing.T) {
	ud, err := RenderStagingFEDOBootstrapUserData(StagingFEDOBootstrapSpec{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(ud, "%!") {
		t.Fatal("render contains fmt marker")
	}
	path := filepath.Join(t.TempDir(), "user-data.sh")
	if err := os.WriteFile(path, []byte(ud), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("bash", "-n", path).CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
}

func TestStagingFESecureBootstrapIsWiredIntoPrivateBaseline(t *testing.T) {
	secrets := DOBaselineSecrets{
		SpacesAccessKey: "ak", SpacesSecretKey: "sk", BoardDatabaseURL: "postgres://db",
		EmbedTokenSecret: "embed", SSOVaultOIDCSecret: "oidc", SSORunnerPublicKey: "runner",
	}
	docs, err := AssembleDOBaseline(context.Background(), MustEmbedded(), DOBaselineInput("Frankfurt", "", "", ""), secrets, DOBaselineOptions{FullServiceBootstraps: true})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	all := strings.Join(docs, "\n")
	for _, want := range []string{
		`resource "digitalocean_droplet_autoscale" "staging-fe"`,
		`resource "digitalocean_firewall" "passo-do-baseline-staging-fe-sg"`,
		`source_tags = ["pyx-edge"]`,
		`${var.staging_fe_artifact_sha256}`,
		"/usr/local/sbin/staging-fe-converge",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("baseline missing %q", want)
		}
	}
	for _, forbidden := range []string{"${var.staging_fe_vault_secret_id}"} {
		if strings.Contains(all, forbidden) {
			t.Errorf("baseline leaks retired bootstrap variable %q", forbidden)
		}
	}
}
