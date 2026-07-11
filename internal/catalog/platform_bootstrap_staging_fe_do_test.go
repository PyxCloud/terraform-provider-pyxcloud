package catalog

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderStagingFENextStandaloneRunsServerRoutesInsideVPC(t *testing.T) {
	ud, err := RenderStagingFEDOBootstrapUserData(StagingFEDOBootstrapSpec{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"s3://pyx-artifacts-fra1/$STAGING_FE_ARTIFACT_KEY",
		"/opt/staging-fe/current/server.js",
		"ExecStart=/usr/bin/node server.js",
		"WorkingDirectory=/opt/staging-fe/current",
		"proxy_pass http://127.0.0.1:3000",
		"INTERNAL_AUTH_URL=https://staging-auth.pyxcloud.io/realms/passobuild/",
		"NEXT_PUBLIC_API_URL=https://staging-api.pyxcloud.io",
		"PYX_MCP_URL=https://staging-mcp.passo.build/mcp",
	} {
		if !strings.Contains(ud, want) {
			t.Errorf("standalone bootstrap missing %q", want)
		}
	}
	for _, forbidden := range []string{"amplifyapp.com", "AMPLIFY_BASIC_AUTH", "next build", "npm install"} {
		if strings.Contains(ud, forbidden) {
			t.Errorf("standalone bootstrap retains forbidden runtime dependency %q", forbidden)
		}
	}
	if strings.Contains(ud, "openssl req -x509") {
		t.Fatal("private staging FE must not fall back to a self-signed certificate")
	}
	if !strings.Contains(ud, "/etc/letsencrypt/live/staging.passo.build/fullchain.pem") ||
		!strings.Contains(ud, "--dns-cloudflare") {
		t.Fatal("private staging FE must provision and use its DNS-01 certificate")
	}
}

func TestRenderStagingFENextStandaloneUsesPinnedArtifactAndVaultEnv(t *testing.T) {
	ud, err := RenderStagingFEDOBootstrapUserData(StagingFEDOBootstrapSpec{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"staging_fe_artifact_key",
		"vibe-frontend/[0-9a-f]{40}/standalone.tar.gz",
		"auth/approle/login",
		"/v1/secret/data/infra/staging/staging-fe/runtime",
		"STAGING_FE_ENV",
		"/etc/staging-fe.env",
		"chmod 0600 /etc/staging-fe.env",
	} {
		if !strings.Contains(ud, want) {
			t.Errorf("secure artifact/env bootstrap missing %q", want)
		}
	}
}

func TestRenderStagingFENextStandaloneBashParses(t *testing.T) {
	ud, err := RenderStagingFEDOBootstrapUserData(StagingFEDOBootstrapSpec{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(ud, "%!") {
		t.Fatalf("render contains fmt error marker")
	}
	path := filepath.Join(t.TempDir(), "user-data.sh")
	if err := os.WriteFile(path, []byte(ud), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("bash", "-n", path).CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
}

func TestStagingFENextStandaloneIsPrivateBaselineService(t *testing.T) {
	found := false
	for _, service := range DOBaselineServices() {
		if service.Name == "staging-fe" && service.Tag == "pyx-staging-fe" {
			found = true
		}
	}
	if !found {
		t.Fatal("staging-fe service missing from DO baseline")
	}

	secrets := DOBaselineSecrets{
		SpacesAccessKey:    "ak",
		SpacesSecretKey:    "sk",
		BoardDatabaseURL:   "postgres://db",
		EmbedTokenSecret:   "embed",
		SSOVaultOIDCSecret: "oidc-secret",
		SSORunnerPublicKey: "runner-public-key",
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
		"ExecStart=/usr/bin/node server.js",
		`${var.staging_fe_artifact_key}`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("baseline missing %q", want)
		}
	}
	for _, doc := range docs {
		if strings.Contains(doc, `resource "digitalocean_firewall" "passo-do-baseline-sg"`) && strings.Contains(doc, "pyx-staging-fe") {
			t.Error("staging-fe must not inherit the public shared firewall")
		}
	}
}
