package catalog

import (
	"fmt"
	"strings"
)

// platform_bootstrap_sso_do.go — pd-MIG-CUTOVER-F2-02 (sso; EPIC-AWS-TO-DO-MIGRATION).
//
// The DigitalOcean counterpart of platform_bootstrap_sso.go. That file ports the
// AWS single-sign-on/main.tf keycloak_user_data as a provider-neutral cloud-init
// that references its secrets by Terraform variable (${var.kc_db_password}, …)
// and fetches the runtime bundle + secrets via the instance role the AWS ASG
// carries. On DigitalOcean neither of those holds:
//
//   - A droplet_autoscale pool has NO instance role — it cannot `aws secretsmanager
//     get-secret-value` on boot the way the AWS ASG does. Every secret therefore
//     has to be INJECTED AT RENDER TIME (the mcp EMBED_TOKEN_SECRET pattern): the
//     CI/render step resolves the value out of Secrets Manager and the renderer
//     bakes it into the emitted user_data. Nothing is fetched on the droplet.
//   - The runtime bundle lives in DO Spaces (S3-compatible), not S3, so the boot
//     fetch is `aws s3 cp --endpoint-url https://fra1.digitaloceanspaces.com` with
//     the Spaces access/secret keys — also injected at render (Secrets Manager
//     beta-DigitalOceanSpacesKeys), never on the box.
//   - KC_DB_URL points at the DO Managed Postgres keycloak-db (jdbc form,
//     sslmode=require), from Secrets Manager beta-DO-keycloak-db-url — injected at
//     render.
//   - The JIT-VPN SPI env is left UNSET: the SPI is fail-safe and no-ops on DO
//     (the WireGuard SG it opens is an AWS resource). SMTP still points at the AWS
//     SES SMTP endpoint (cross-cloud) per the F1-05 / ADR-0001 decision — SES is
//     region-global and reachable from DO with the IAM SMTP creds.
//
// This is wired into the sso scale-group as UserDataByProvider["digitalocean"]
// (SSODOUserData below), so the ONE canonical `sso` topology carries the AWS
// bootstrap as its default and this DO bootstrap as the per-provider override —
// no forked topology, no new component (matches the UserDataByProvider design in
// scalegroup.go and the mcp DO-vs-AWS user_data split described there).
//
// SECURITY: the secrets are injected VALUES (there is no other option on DO — the
// droplet cannot fetch them), so this function must only ever be called with
// values resolved fresh from Secrets Manager at render time, and the rendered
// user_data must be treated as sensitive (it is, exactly like the injected mcp
// EMBED_TOKEN_SECRET). The file vault + keycloak.conf are written 0600/0640 and
// owned by the service user, same as the AWS port.
//
// VAULT MIGRATION (EPIC-BOOTFETCH-AWS-SM-TO-VAULT, wave 2 — the MOST DELICATE of
// the three: this is the auth service). "Injected at render" no longer means "an
// operator ran `aws secretsmanager get-secret-value` and passed a Go string into
// this function" — it means Terraform itself resolves the value from Vault via a
// `data "vault_kv_secret_v2"` block (vault_datasource.go) at APPLY time and the
// rendered user_data interpolates ${data.vault_kv_secret_v2.<label>.data["<key>"]}
// directly in the keycloak.conf heredoc (exactly where ${var.<x>} used to sit for
// mcp/sast). The droplet STILL never talks to Vault itself (no boot-fetch, no
// instance role, unchanged reasoning above) — only the SOURCE of the render-time
// value moved from a human-exported AWS-SM env var to a Vault data source
// Terraform reads at plan/apply. Mapping (secret/infra/<env>/...):
//
//	KCDBURL         -> sso/keycloak-db-url      key "url"
//	KCDBUsername    -> sso/keycloak-db          key "username"
//	KCDBPassword    -> sso/keycloak-db          key "password"
//	AdminPassword   -> sso/keycloak-admin       key "password"
//	SpacesAccessKey -> do/spaces-keys           key "access_key"
//	SpacesSecretKey -> do/spaces-keys           key "secret_key"
//	SMTPUser        -> sso/smtp                 key "username"
//	SMTPPassword    -> sso/smtp                 key "password"
//
// NOT migrated (kept as plain injected-value fields; see the RISK note in the
// PR description):
//   - VaultOIDCSecret: no Vault KV leaf was provisioned for the pyx Vault OIDC
//     client secret in this wave.
//   - RunnerPublicKey: the only related leaf (sso/runner-ssh-key) holds a
//     "private_key" field, not a public key — injecting a private key into
//     authorized_keys would be a serious bug, so this field is left exactly as
//     it was (an operator-supplied public-key string) rather than guessing a
//     wrong mapping.

// DO Spaces boot-fetch constants for the SSO runtime bundle. The bundle object
// (SPI jars + themes + realm.json) is unchanged from the AWS path; only the
// endpoint (DO Spaces, fra1) and the key source (injected) differ. The bundle
// key is pinned to the F2 cutover artefact.
const (
	// ssoDOSpacesEndpoint is the DO Spaces S3-compatible endpoint (fra1 region).
	ssoDOSpacesEndpoint = "https://fra1.digitaloceanspaces.com"
	// ssoDOSpacesBucket is the artefacts Spaces bucket the SSO bundle lives in.
	ssoDOSpacesBucket = "pyx-artifacts-fra1"
	// ssoDOBundleKey is the pinned SSO runtime bundle object (SPI/theme/realm tgz).
	ssoDOBundleKey = "sso/sso-bundle-80f79e3550.tgz"
	// ssoDOBundleURI is the full s3://… URI the droplet `aws s3 cp`s (with the DO
	// Spaces endpoint override).
	ssoDOBundleURI = "s3://" + ssoDOSpacesBucket + "/" + ssoDOBundleKey
)

// SSODOBootstrapSpec is the typed input for the DigitalOcean SSO (Keycloak)
// bootstrap. Most credential fields now name a Vault KV-v2 leaf PATH (resolved
// by Terraform at apply time via a `data "vault_kv_secret_v2"` block — see the
// VAULT MIGRATION note above and vault_datasource.go), not a literal value: the
// rendered heredoc interpolates ${data.vault_kv_secret_v2...} exactly where a
// literal used to sit. The two fields with no provisioned Vault leaf
// (VaultOIDCSecret, RunnerPublicKey) remain literal INJECTED VALUES, resolved by
// the operator/CI exactly as before — because a DO droplet has no instance role
// to fetch anything itself (the mcp EMBED_TOKEN_SECRET reasoning still applies).
type SSODOBootstrapSpec struct {
	// Environment is the deploy environment (e.g. "beta"); drives the public
	// hostname (<env>-auth.<domain>) and the keystore file names. Required.
	Environment string
	// DomainName is the apex used for the public hostname + SMTP From address.
	// Defaults to "pyxcloud.io".
	DomainName string

	// KCDBURLKVPath is the Vault KV-v2 leaf holding the FULL jdbc URL for the DO
	// Managed Postgres keycloak-db (key "url"), e.g.
	//   jdbc:postgresql://kc-do-do-user.db.ondigitalocean.com:25060/defaultdb?sslmode=require
	// Default "infra/staging/sso/keycloak-db-url".
	KCDBURLKVPath string
	// KCDBKVPath is the Vault KV-v2 leaf holding the keycloak-db credentials
	// (keys "username"/"password"). Default "infra/staging/sso/keycloak-db".
	KCDBKVPath string
	// AdminKVPath is the Vault KV-v2 leaf holding the Keycloak bootstrap admin
	// credentials (key "password" used here). Default "infra/staging/sso/keycloak-admin".
	AdminKVPath string
	// VaultOIDCSecret is the pyx Vault OIDC client secret written to the file vault
	// (KC_VAULT=file), injected at render. UNMIGRATED: no Vault KV leaf was
	// provisioned for this secret in this wave, so it stays a literal value.
	// Required.
	VaultOIDCSecret string

	// SpacesKVPath is the Vault KV-v2 leaf holding the DO Spaces keys (keys
	// "access_key"/"secret_key") used to boot-fetch the runtime bundle (the
	// bundle fetch is the substance of the SSO box). Default
	// "infra/staging/do/spaces-keys".
	SpacesKVPath string

	// RunnerPublicKey is the deploy runner's STABLE SSH public key, injected at
	// render. UNMIGRATED: the only related Vault leaf (sso/runner-ssh-key) holds
	// a "private_key" field, not a public key — see the RISK note above; do not
	// wire this to that leaf without confirming with the operator which key
	// material it actually holds. Optional; empty -> no authorized_keys.
	RunnerPublicKey string

	// SES SMTP (cross-cloud per F1-05 / ADR-0001). SMTPHost defaults to the AWS SES
	// SMTP endpoint for SESRegion. SMTPKVPath is the Vault KV-v2 leaf holding the
	// IAM SMTP creds (keys "username"/"password"). Default "infra/staging/sso/smtp".
	// Empty SMTPKVPath -> the SMTP env lines are omitted (matches the old
	// empty-user/password behavior).
	SESRegion  string // AWS region for the SES SMTP host; default "eu-west-1"
	SMTPKVPath string
	// PassobuildSenderEmail backs KC_PASSOBUILD_SMTP_FROM. Optional.
	PassobuildSenderEmail string
}

func (s SSODOBootstrapSpec) withDefaults() SSODOBootstrapSpec {
	if strings.TrimSpace(s.DomainName) == "" {
		s.DomainName = "pyxcloud.io"
	}
	if strings.TrimSpace(s.SESRegion) == "" {
		s.SESRegion = "eu-west-1"
	}
	if strings.TrimSpace(s.KCDBURLKVPath) == "" {
		s.KCDBURLKVPath = "infra/staging/sso/keycloak-db-url"
	}
	if strings.TrimSpace(s.KCDBKVPath) == "" {
		s.KCDBKVPath = "infra/staging/sso/keycloak-db"
	}
	if strings.TrimSpace(s.AdminKVPath) == "" {
		s.AdminKVPath = "infra/staging/sso/keycloak-admin"
	}
	if strings.TrimSpace(s.SpacesKVPath) == "" {
		s.SpacesKVPath = "infra/staging/do/spaces-keys"
	}
	return s
}

// SSODOVaultDataSources returns, in deterministic order, the `data
// "vault_kv_secret_v2"` HCL blocks this bootstrap's render-time secrets need
// (see vault_datasource.go). SMTPKVPath is included only when set (matches the
// bootstrap's own "empty -> SMTP omitted" behavior).
func (s SSODOBootstrapSpec) SSODOVaultDataSources() []string {
	s = s.withDefaults()
	paths := []string{s.KCDBURLKVPath, s.KCDBKVPath, s.AdminKVPath, s.SpacesKVPath}
	if strings.TrimSpace(s.SMTPKVPath) != "" {
		paths = append(paths, s.SMTPKVPath)
	}
	var docs []string
	for _, path := range paths {
		doc, _ := VaultKVDataSourceHCL(path)
		docs = append(docs, doc)
	}
	return docs
}

// RenderSSODOBootstrapUserData renders the DigitalOcean SSO (Keycloak) cloud-init
// as a bash script with the secrets INJECTED (not Terraform variables), suitable
// for a droplet_autoscale pool with no instance role. It is the DO counterpart of
// RenderSSOBootstrapUserData: install Java 21 + Keycloak (same pinned versions),
// boot-fetch the SPI/theme/realm bundle from DO Spaces with the injected keys,
// write the file vault, generate the local HTTPS keystore, write keycloak.conf
// (KC_DB_URL -> the DO keycloak-db, sslmode=require; KC_VAULT=file) + the systemd
// unit (JIT env UNSET; SES SMTP cross-cloud), `kc.sh build` then start with
// --optimized --import-realm.
func RenderSSODOBootstrapUserData(spec SSODOBootstrapSpec) (string, error) {
	s := spec.withDefaults()
	if strings.TrimSpace(s.Environment) == "" {
		return "", fmt.Errorf("sso-do-bootstrap: environment is required (drives <env>-auth.%s and the keystore file names)", s.DomainName)
	}
	// The two secrets with no provisioned Vault leaf still must be injected (no
	// on-droplet fetch on DO). The Vault-sourced fields always have a default KV
	// path (withDefaults above), so there is nothing to validate for those — a
	// missing/wrong Vault leaf now fails at `terraform apply`, not at render.
	for _, req := range []struct{ name, val string }{
		{"VaultOIDCSecret", s.VaultOIDCSecret},
	} {
		if strings.TrimSpace(req.val) == "" {
			return "", fmt.Errorf("sso-do-bootstrap: %s must be injected at render (DO droplets have no instance role to fetch it, and no Vault leaf is provisioned for it)", req.name)
		}
	}

	_, kcDBURLLabel := VaultKVDataSourceHCL(s.KCDBURLKVPath)
	_, kcDBLabel := VaultKVDataSourceHCL(s.KCDBKVPath)
	_, adminLabel := VaultKVDataSourceHCL(s.AdminKVPath)
	_, spacesLabel := VaultKVDataSourceHCL(s.SpacesKVPath)
	kcDBURLRef := VaultKVRef(kcDBURLLabel, "url")
	kcDBUsernameRef := VaultKVRef(kcDBLabel, "username")
	kcDBPasswordRef := VaultKVRef(kcDBLabel, "password")
	adminPasswordRef := VaultKVRef(adminLabel, "password")
	spacesAccessKeyRef := VaultKVRef(spacesLabel, "access_key")
	spacesSecretKeyRef := VaultKVRef(spacesLabel, "secret_key")
	var smtpUserRef, smtpPasswordRef string
	if strings.TrimSpace(s.SMTPKVPath) != "" {
		_, smtpLabel := VaultKVDataSourceHCL(s.SMTPKVPath)
		smtpUserRef = VaultKVRef(smtpLabel, "username")
		smtpPasswordRef = VaultKVRef(smtpLabel, "password")
	}

	host := fmt.Sprintf("%s-auth.%s", s.Environment, s.DomainName)

	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }

	w("#!/bin/bash")
	w("set -euo pipefail")
	w("# Canonical SSO (Keycloak %s) DigitalOcean bootstrap — pd-MIG-CUTOVER-F2-02.", ssoKeycloakVersion)
	w("# DO droplet_autoscale has no instance role: all secrets are INJECTED at render")
	w("# (the mcp EMBED_TOKEN_SECRET pattern); the runtime bundle is fetched from DO")
	w("# Spaces (S3-compatible endpoint) with injected keys.")
	w("")
	w("sudo apt update")
	w("sudo apt install -y wget unzip openssl python3")
	w("")
	w("# Java 21 (Temurin %s) — same pin as the AWS port.", ssoJDKVersion)
	w("if [ ! -d \"/opt/java/jdk-21\" ]; then")
	w("  sudo mkdir -p /opt/java")
	w("  wget -q %s -O /tmp/jdk21.tar.gz", ssoJDKURL)
	w("  sudo tar -xzf /tmp/jdk21.tar.gz -C /opt/java")
	w("  sudo mv /opt/java/jdk-21* /opt/java/jdk-21 || true")
	w("  rm -f /tmp/jdk21.tar.gz")
	w("fi")
	w("sudo update-alternatives --install /usr/bin/java java /opt/java/jdk-21/bin/java 2000")
	w("sudo update-alternatives --set java /opt/java/jdk-21/bin/java")
	w("")
	w("# Service user + the STABLE deploy-runner key (injected, no per-deploy churn).")
	w("sudo useradd -m -s /bin/bash main || true")
	w("sudo usermod -aG sudo main")
	w("echo \"main ALL=(ALL) NOPASSWD: ALL\" | sudo tee /etc/sudoers.d/main > /dev/null")
	if strings.TrimSpace(s.RunnerPublicKey) != "" {
		w("sudo mkdir -p /home/main/.ssh && sudo chmod 700 /home/main/.ssh")
		w("echo %q | sudo tee /home/main/.ssh/authorized_keys > /dev/null", s.RunnerPublicKey)
		w("sudo chmod 600 /home/main/.ssh/authorized_keys && sudo chown -R main:main /home/main/.ssh")
	}
	w("")
	w("# Keycloak %s — same pin/zip URL as the AWS port (public github, unchanged).", ssoKeycloakVersion)
	w("if [ ! -d \"/opt/keycloak\" ]; then")
	w("  cd /opt")
	w("  sudo wget %s -O keycloak.zip", ssoKeycloakZipURL)
	w("  sudo unzip keycloak.zip && sudo rm keycloak.zip")
	w("  sudo mv keycloak-* keycloak")
	w("fi")
	w("sudo chown -R main:main /opt/keycloak")
	w("sudo mkdir -p /opt/keycloak/data/import /opt/keycloak/data/transaction-logs")
	w("")
	w("# Vault OIDC client secret -> file vault (KC_VAULT=file). Injected value.")
	w("sudo mkdir -p /etc/keycloak/vault")
	w("echo -n %q | sudo tee /etc/keycloak/vault/pyx_vault_oidc_secret > /dev/null", s.VaultOIDCSecret)
	w("sudo chmod 700 /etc/keycloak/vault && sudo chmod 600 /etc/keycloak/vault/pyx_vault_oidc_secret")
	w("sudo chown -R main:main /etc/keycloak/vault")
	w("")
	w("# Boot-fetch the SSO runtime bundle (providers + themes + realm.json) from DO")
	w("# Spaces (S3-compatible). Keys are resolved by Terraform from Vault")
	w("# secret/infra/<env>/do/spaces-keys at apply time (no instance role on DO).")
	w("command -v aws >/dev/null 2>&1 || sudo snap install aws-cli --classic || sudo apt install -y awscli || true")
	w("mkdir -p /tmp/sso-bundle")
	w("export AWS_ACCESS_KEY_ID=\"%s\"", spacesAccessKeyRef)
	w("export AWS_SECRET_ACCESS_KEY=\"%s\"", spacesSecretKeyRef)
	w("if aws s3 cp %q /tmp/sso-bundle.tgz --endpoint-url %q; then", ssoDOBundleURI, ssoDOSpacesEndpoint)
	w("  tar -xzf /tmp/sso-bundle.tgz -C /tmp/sso-bundle")
	w("  sudo find /tmp/sso-bundle -maxdepth 2 -name 'pyx-event-listener-*.jar' -exec cp {} /opt/keycloak/providers/ \\;")
	w("  sudo find /tmp/sso-bundle -maxdepth 2 -name 'pyx-jit-allowlist-*.jar' -exec cp {} /opt/keycloak/providers/ \\;")
	w("  sudo find /tmp/sso-bundle -maxdepth 2 -name 'keycloak-magic-link.jar' -exec cp {} /opt/keycloak/providers/ \\;")
	w("  [ -d /tmp/sso-bundle/themes ] && sudo cp -r /tmp/sso-bundle/themes/. /opt/keycloak/themes/")
	w("  [ -f /tmp/sso-bundle/keycloak-realm.json ] && sudo cp /tmp/sso-bundle/keycloak-realm.json /opt/keycloak/data/import/realm.json")
	w("  sudo chown -R main:main /opt/keycloak/providers /opt/keycloak/themes /opt/keycloak/data/import")
	w("  rm -rf /tmp/sso-bundle /tmp/sso-bundle.tgz")
	w("fi")
	w("unset AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY")
	w("")
	w("# Self-signed keystore for LB<->instance TLS (the DO LB terminates public TLS).")
	w("KCPW=\"SamplePassword3451\"")
	w("sudo openssl req -x509 -newkey rsa:2048 -nodes -days 365 \\")
	w("  -keyout /opt/keycloak/data/import/selfsigned.key.pem \\")
	w("  -out /opt/keycloak/data/import/selfsigned.cert.pem -subj \"/CN=%s\"", host)
	w("sudo openssl pkcs12 -export -name keycloak -passout pass:\"$KCPW\" \\")
	w("  -in /opt/keycloak/data/import/selfsigned.cert.pem \\")
	w("  -inkey /opt/keycloak/data/import/selfsigned.key.pem \\")
	w("  -out /opt/keycloak/data/import/%s.p12", host)
	w("sudo /opt/java/jdk-21/bin/keytool -importkeystore -noprompt -srcstoretype PKCS12 \\")
	w("  -srckeystore /opt/keycloak/data/import/%s.p12 -srcstorepass \"$KCPW\" \\", host)
	w("  -destkeystore /opt/keycloak/data/import/%s.jks -deststoretype JKS \\", host)
	w("  -deststorepass \"$KCPW\" -destkeypass \"$KCPW\"")
	w("sudo chown -R main:main /opt/keycloak/data")
	w("")
	w("# keycloak.conf — KC_DB_URL points at the DO Managed keycloak-db (jdbc,")
	w("# sslmode=require, resolved by Terraform from Vault secret/infra/<env>/sso/")
	w("# keycloak-db-url at apply time). KC_CACHE=%s.", ssoCacheMode)
	w("sudo tee /etc/keycloak/keycloak.conf > /dev/null <<EOCONF")
	w("KC_DB_URL=%s", kcDBURLRef)
	w("KC_DB_USERNAME=%s", kcDBUsernameRef)
	w("KC_DB_PASSWORD='%s'", kcDBPasswordRef)
	w("KC_DB=postgres")
	w("KC_BOOTSTRAP_ADMIN_USERNAME=admin")
	w("KC_BOOTSTRAP_ADMIN_PASSWORD='%s'", adminPasswordRef)
	w("KC_HTTPS_KEY_STORE_FILE=/opt/keycloak/data/import/%s.jks", host)
	w("KC_HTTPS_KEY_STORE_PASSWORD=$KCPW")
	w("KC_METRICS_ENABLED=true")
	w("KC_HEALTH_ENABLED=true")
	w("KC_HOSTNAME=%s", host)
	w("KC_PROXY_HEADERS=xforwarded")
	w("KC_HTTP_ENABLED=true")
	w("KC_HTTP_PORT=8080")
	w("KC_CACHE=%s", ssoCacheMode)
	w("KC_HTTP_MANAGEMENT_HEALTH_ENABLED=true")
	w("KC_SHUTDOWN_DELAY=16s")
	w("KC_VAULT=file")
	w("KC_VAULT_DIR=/etc/keycloak/vault")
	w("EOCONF")
	w("sudo chown main:main /etc/keycloak/keycloak.conf && sudo chmod 640 /etc/keycloak/keycloak.conf")
	w("")
	w("# systemd unit — JIT-VPN SPI env is UNSET on DO (the SPI no-ops: the WireGuard")
	w("# SG it opens is an AWS resource). SES SMTP is cross-cloud (F1-05 / ADR-0001).")
	w("sudo tee /etc/systemd/system/keycloak.service > /dev/null <<EOL")
	w("[Unit]")
	w("Description=Keycloak Server")
	w("After=network.target")
	w("[Service]")
	w("User=main")
	w("Group=main")
	w("WorkingDirectory=/opt/keycloak")
	w("EnvironmentFile=/etc/keycloak/keycloak.conf")
	w("ExecStart=/opt/keycloak/bin/kc.sh start --verbose --import-realm --optimized")
	w("Restart=on-failure")
	w("Environment=KC_SPI_EXPORT_IMPORT_DIR_STRATEGY=OVERWRITE_EXISTING")
	if strings.TrimSpace(s.SMTPKVPath) != "" {
		w("Environment=KC_SMTP_HOST=email-smtp.%s.amazonaws.com", s.SESRegion)
		w("Environment=KC_SMTP_PORT=587")
		w("Environment=KC_SMTP_FROM=no-reply@%s", s.DomainName)
		w("Environment=KC_SMTP_USER=%s", smtpUserRef)
		w("Environment=KC_SMTP_PASSWORD=%s", smtpPasswordRef)
		w("Environment=KC_SMTP_AUTH=true")
		w("Environment=KC_SMTP_STARTTLS=true")
	}
	if strings.TrimSpace(s.PassobuildSenderEmail) != "" {
		w("Environment=KC_PASSOBUILD_SMTP_FROM=%s", s.PassobuildSenderEmail)
	}
	w("[Install]")
	w("WantedBy=multi-user.target")
	w("EOL")
	w("")
	w("# Augmentation build (must match the AWS port's features) then optimized start.")
	w("sudo -u main bash -c 'source /etc/keycloak/keycloak.conf && /opt/keycloak/bin/kc.sh build --db=postgres --health-enabled=true --metrics-enabled=true --features=%s --vault=file'", ssoBuildFeatures)
	w("sudo systemctl daemon-reload && sudo systemctl enable keycloak && sudo systemctl restart keycloak")

	return b.String(), nil
}

// WithSSODOUserData wires the rendered DigitalOcean SSO bootstrap onto the `sso`
// scale-group as UserDataByProvider["digitalocean"], leaving the generic UserData
// (the AWS/default bootstrap) and every OTHER service untouched. It is the DO
// counterpart of the PlatformBootstraps wiring: one canonical `sso` topology now
// carries the AWS bootstrap as its default and this DO bootstrap as the
// per-provider override, so a DigitalOcean render descends the DO user_data while
// an AWS render keeps the AWS one — no forked topology, no new component.
//
// It mutates the passed slice in place (and returns it) so it composes on top of
// PlatformScaleGroupComponents / ProdEstateComponents / DOBaselineComponents. The
// sso component must be present (it always is in those constructors).
func WithSSODOUserData(comps []AssembleComponent, doUserData string) []AssembleComponent {
	if strings.TrimSpace(doUserData) == "" {
		return comps
	}
	for i := range comps {
		if comps[i].Name != "sso" || comps[i].ScaleGroup == nil {
			continue
		}
		if comps[i].ScaleGroup.UserDataByProvider == nil {
			comps[i].ScaleGroup.UserDataByProvider = map[string]string{}
		}
		comps[i].ScaleGroup.UserDataByProvider[ProviderDigitalOcean] = doUserData
	}
	return comps
}
