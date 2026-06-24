package catalog

import (
	"fmt"
	"strings"
)

// platform_bootstrap_sso.go — pd-DEP-MIGRATE-PLATFORM-MODULES (slice 1: SSO).
//
// platform_asgs.go already expresses the 5 platform services (SSO / VPN / obs /
// SAST / backend) in the canonical vocabulary as `virtual-machine-scale-group`s
// of 1. But a scale-group of a bare Ubuntu box is NOT the SSO module: the whole
// substance of the hand-written single-sign-on/main.tf is its ~200-line
// `keycloak_user_data` bootstrap (install Java 21 + Keycloak, boot-fetch the
// providers/themes/realm bundle from S3, generate the local HTTPS keystore,
// write keycloak.conf + the systemd unit, `kc.sh build`, import the realm, then
// reconcile the Vault OIDC client secret via kcadm). Until that bootstrap is a
// first-class part of the abstract component, "migrating SSO to the provider"
// would silently boot an empty VM.
//
// This file ports that bootstrap into the catalog as a PROVIDER-NEUTRAL,
// PARAMETERISED cloud-init. The hand-written script is welded to Terraform
// interpolations that only exist in that one root module — the RDS address
// (${aws_db_instance.db_instance.address}), the random_password/random_string
// resources, the SES SMTP IAM access key, var.jit_vpn_sg_id, var.environment.
// Those are exactly why a naive copy can't be abstracted. So the canonical
// component lifts each of them to a TYPED input: SSOBootstrapSpec carries the
// values (or, for the secrets, the *name of the Terraform variable* that holds
// them), and RenderSSOBootstrapUserData emits the script with `${var.<x>}`
// placeholders. The rendered cloud-init is then handed to the existing
// scale-group launch template via AssembleScaleGroup.UserData — no new
// translator, no new render path (SPEC §1: the abstract topology is the single
// source and the renderer descends it).
//
// SECURITY: like secrets.go / manageddatabase.go, NO secret VALUE is inlined.
// The DB/admin passwords and the SMTP password are referenced by Terraform
// variable name (e.g. ${var.kc_db_password}); the operator wires those vars to
// the same random_password / Secrets Manager source the hand-written module
// used. The script never embeds a literal credential.

// Pinned upstream versions — kept identical to the hand-written single-sign-on
// module so the canonical bootstrap is a faithful port, not a drift. Bump here
// (one place) when the platform upgrades Keycloak/JDK.
const (
	ssoKeycloakVersion = "26.1.4"
	ssoKeycloakZipURL  = "https://github.com/keycloak/keycloak/releases/download/" + ssoKeycloakVersion + "/keycloak-" + ssoKeycloakVersion + ".zip"
	ssoJDKVersion      = "21.0.6+7"
	ssoJDKURL          = "https://github.com/adoptium/temurin21-binaries/releases/download/jdk-21.0.6%2B7/OpenJDK21U-jdk_x64_linux_hotspot_21.0.6_7.tar.gz"
	// kc.sh build features — must match the hand-written module's build line.
	ssoBuildFeatures = "token-exchange,admin-fine-grained-authz"
	// Local-cache HA (KC_CACHE=local) — see the long comment in the hand-written
	// keycloak.conf: ispn + jdbc-ping on an auto-replacing ASG accumulated dead
	// peers and produced the recurring 502s, so each node keeps a LOCAL cache and
	// RDS is the source of truth, with ALB app-cookie stickiness.
	ssoCacheMode = "local"
)

// SSOBootstrapSpec is the typed, provider-neutral input for the canonical SSO
// (Keycloak) platform bootstrap. Every value that the hand-written module pulled
// from a Terraform interpolation is lifted to an explicit field here so the
// component is self-describing and round-trippable. The secret fields name the
// Terraform variable that holds the secret (NOT the secret value) so nothing
// sensitive enters the abstract topology or Terraform state via this component.
type SSOBootstrapSpec struct {
	// Environment is the deploy environment (e.g. "beta"); drives the public
	// hostname (<env>-auth.pyxcloud.io) and the keystore file names. Required.
	Environment string
	// DomainName is the apex used for the SMTP From address (no-reply@<domain>).
	// Defaults to "pyxcloud.io".
	DomainName string
	// DBEndpointVar names the Terraform variable holding the Postgres host:port
	// (the hand-written module wired ${aws_db_instance.db_instance.address}).
	// Defaults to "kc_db_endpoint".
	DBEndpointVar string
	// DBUsernameVar / DBPasswordVar / AdminPasswordVar / VaultOIDCSecretVar name
	// the Terraform variables holding those credentials (NOT the values). They map
	// to the random_string/random_password resources in the hand-written module.
	DBUsernameVar      string // default "kc_db_username"
	DBPasswordVar      string // default "kc_db_password"
	AdminPasswordVar   string // default "kc_admin_password"
	VaultOIDCSecretVar string // default "kc_vault_oidc_secret"
	// ArtifactURLVar / ArtifactVersionVar name the variables holding the S3 URL +
	// version of the SSO runtime bundle (providers + themes + realm.json). The
	// boot-fetch is a no-op when the URL var resolves empty (first boot / no
	// bundle yet), matching the hand-written module.
	ArtifactURLVar     string // default "sso_artifact_url"
	ArtifactVersionVar string // default "sso_artifact_version"
	// RunnerPublicKeyVar names the variable holding the deploy runner's STABLE
	// public key (Secrets Manager beta-SsoRunnerSshKey in prod). Defaults to
	// "runner_public_key".
	RunnerPublicKeyVar string
	// JITVPNSecurityGroupVar names the variable holding the WireGuard SG id the
	// JIT-allowlist SPI opens on login (var.jit_vpn_sg_id). Defaults to
	// "jit_vpn_sg_id". The SPI is fail-safe: empty -> the SPI no-ops, login never
	// blocked.
	JITVPNSecurityGroupVar string
	// SMTPUserVar / SMTPPasswordVar name the variables holding the SES SMTP IAM
	// credentials (the hand-written module derived them from
	// aws_iam_access_key.smtp_user_key). Empty defaults -> SMTP env lines omitted.
	SMTPUserVar     string // default "kc_smtp_user"
	SMTPPasswordVar string // default "kc_smtp_password"
	// PassobuildSenderEmailVar names the variable holding the passo.build SES
	// sender (KC_PASSOBUILD_SMTP_FROM). Optional.
	PassobuildSenderEmailVar string // default "passobuild_sender_email"
	// Region is the abstract pyx region_name — only used to resolve the AWS region
	// for the SSM-agent / SES SMTP host on an AWS placement. Optional; the
	// canonical SSO box runs on AWS today, so an empty Region renders the
	// AWS-region var reference.
	RegionVar string // default "aws_region"
}

// withSSOBootstrapDefaults fills the production-faithful defaults for any unset
// variable-name field so callers can pass an almost-empty spec and still get the
// canonical wiring.
func (s SSOBootstrapSpec) withDefaults() SSOBootstrapSpec {
	def := func(v, d string) string {
		if strings.TrimSpace(v) == "" {
			return d
		}
		return v
	}
	s.DomainName = def(s.DomainName, "pyxcloud.io")
	s.DBEndpointVar = def(s.DBEndpointVar, "kc_db_endpoint")
	s.DBUsernameVar = def(s.DBUsernameVar, "kc_db_username")
	s.DBPasswordVar = def(s.DBPasswordVar, "kc_db_password")
	s.AdminPasswordVar = def(s.AdminPasswordVar, "kc_admin_password")
	s.VaultOIDCSecretVar = def(s.VaultOIDCSecretVar, "kc_vault_oidc_secret")
	s.ArtifactURLVar = def(s.ArtifactURLVar, "sso_artifact_url")
	s.ArtifactVersionVar = def(s.ArtifactVersionVar, "sso_artifact_version")
	s.RunnerPublicKeyVar = def(s.RunnerPublicKeyVar, "runner_public_key")
	s.JITVPNSecurityGroupVar = def(s.JITVPNSecurityGroupVar, "jit_vpn_sg_id")
	s.SMTPUserVar = def(s.SMTPUserVar, "kc_smtp_user")
	s.SMTPPasswordVar = def(s.SMTPPasswordVar, "kc_smtp_password")
	s.PassobuildSenderEmailVar = def(s.PassobuildSenderEmailVar, "passobuild_sender_email")
	s.RegionVar = def(s.RegionVar, "aws_region")
	return s
}

// SSOBootstrapVariableNames returns, in deterministic order, the Terraform
// variable names this bootstrap references. The assembler/CLI uses it to emit
// the matching `variable "<x>" {}` declarations (the secret ones marked
// sensitive) so the rendered .tf is self-contained and `terraform validate`s.
func (s SSOBootstrapSpec) SSOBootstrapVariableNames() (plain []string, sensitive []string) {
	s = s.withDefaults()
	plain = []string{
		s.DBEndpointVar, s.DBUsernameVar, s.ArtifactURLVar, s.ArtifactVersionVar,
		s.RunnerPublicKeyVar, s.JITVPNSecurityGroupVar, s.RegionVar,
		s.SMTPUserVar, s.PassobuildSenderEmailVar,
	}
	sensitive = []string{
		s.DBPasswordVar, s.AdminPasswordVar, s.VaultOIDCSecretVar, s.SMTPPasswordVar,
	}
	return plain, sensitive
}

// RenderSSOBootstrapUserData renders the canonical Keycloak SSO cloud-init as a
// provider-neutral bash script with `${var.<x>}` placeholders. It is a faithful
// port of single-sign-on/main.tf's keycloak_user_data: install Java + Keycloak,
// boot-fetch the provider/theme/realm bundle, generate the local HTTPS keystore,
// write keycloak.conf + the systemd unit (KC_CACHE=local, JIT-VPN SPI env, SES
// SMTP), `kc.sh build`, and import the realm. The returned string is meant to be
// placed into AssembleScaleGroup.UserData for the `sso` service.
func RenderSSOBootstrapUserData(spec SSOBootstrapSpec) (string, error) {
	s := spec.withDefaults()
	if strings.TrimSpace(s.Environment) == "" {
		return "", fmt.Errorf("sso-bootstrap: environment is required (drives <env>-auth.%s and the keystore file names)", s.DomainName)
	}
	host := fmt.Sprintf("%s-auth.%s", s.Environment, s.DomainName)
	v := func(name string) string { return "${var." + name + "}" }

	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }

	w("#!/bin/bash")
	w("set -euo pipefail")
	w("# Canonical SSO (Keycloak %s) bootstrap — ported from single-sign-on/main.tf", ssoKeycloakVersion)
	w("# by the abstract provider (pd-DEP-MIGRATE-PLATFORM-MODULES). Provider-neutral;")
	w("# all secrets are Terraform variables, never inlined.")
	w("")
	w("sudo apt update")
	w("sudo apt install -y wget unzip openssl python3")
	w("")
	w("# Java 21 (Temurin %s)", ssoJDKVersion)
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
	w("# Service user + the STABLE deploy-runner key (no per-deploy user_data churn).")
	w("sudo useradd -m -s /bin/bash main || true")
	w("sudo usermod -aG sudo main")
	w("echo \"main ALL=(ALL) NOPASSWD: ALL\" | sudo tee /etc/sudoers.d/main > /dev/null")
	w("sudo mkdir -p /home/main/.ssh && sudo chmod 700 /home/main/.ssh")
	w("echo \"%s\" | sudo tee /home/main/.ssh/authorized_keys > /dev/null", v(s.RunnerPublicKeyVar))
	w("sudo chmod 600 /home/main/.ssh/authorized_keys && sudo chown -R main:main /home/main/.ssh")
	w("")
	w("# Keycloak %s", ssoKeycloakVersion)
	w("if [ ! -d \"/opt/keycloak\" ]; then")
	w("  cd /opt")
	w("  sudo wget %s -O keycloak.zip", ssoKeycloakZipURL)
	w("  sudo unzip keycloak.zip && sudo rm keycloak.zip")
	w("  sudo mv keycloak-* keycloak")
	w("fi")
	w("sudo chown -R main:main /opt/keycloak")
	w("sudo mkdir -p /opt/keycloak/data/import /opt/keycloak/data/transaction-logs")
	w("")
	w("# Vault OIDC client secret -> file vault (KC_VAULT=file).")
	w("sudo mkdir -p /etc/keycloak/vault")
	w("echo -n '%s' | sudo tee /etc/keycloak/vault/pyx_vault_oidc_secret > /dev/null", v(s.VaultOIDCSecretVar))
	w("sudo chmod 700 /etc/keycloak/vault && sudo chmod 600 /etc/keycloak/vault/pyx_vault_oidc_secret")
	w("sudo chown -R main:main /etc/keycloak/vault")
	w("")
	w("# Boot-fetch the SSO runtime bundle (providers + themes + realm.json) from S3.")
	w("# No-op when the URL var resolves empty (first boot / no bundle yet).")
	w("SSO_ARTIFACT_URL=\"%s\"", v(s.ArtifactURLVar))
	w("if [ -n \"$SSO_ARTIFACT_URL\" ]; then")
	w("  command -v aws >/dev/null 2>&1 || sudo snap install aws-cli --classic || sudo apt install -y awscli || true")
	w("  mkdir -p /tmp/sso-bundle")
	w("  if aws s3 cp \"$SSO_ARTIFACT_URL\" /tmp/sso-bundle.tgz; then")
	w("    tar -xzf /tmp/sso-bundle.tgz -C /tmp/sso-bundle")
	w("    sudo find /tmp/sso-bundle -maxdepth 2 -name 'pyx-event-listener-*.jar' -exec cp {} /opt/keycloak/providers/ \\;")
	w("    sudo find /tmp/sso-bundle -maxdepth 2 -name 'pyx-jit-allowlist-*.jar' -exec cp {} /opt/keycloak/providers/ \\;")
	w("    sudo find /tmp/sso-bundle -maxdepth 2 -name 'keycloak-magic-link.jar' -exec cp {} /opt/keycloak/providers/ \\;")
	w("    [ -d /tmp/sso-bundle/themes ] && sudo cp -r /tmp/sso-bundle/themes/. /opt/keycloak/themes/")
	w("    [ -f /tmp/sso-bundle/keycloak-realm.json ] && sudo cp /tmp/sso-bundle/keycloak-realm.json /opt/keycloak/data/import/realm.json")
	w("    sudo chown -R main:main /opt/keycloak/providers /opt/keycloak/themes /opt/keycloak/data/import")
	w("    rm -rf /tmp/sso-bundle /tmp/sso-bundle.tgz")
	w("  fi")
	w("fi")
	w("")
	w("# Self-signed keystore for ALB<->instance TLS (ALB terminates public TLS).")
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
	w("# keycloak.conf — KC_CACHE=%s (local-cache HA; RDS is the source of truth,", ssoCacheMode)
	w("# ALB app-cookie stickiness; ispn+jdbc-ping caused the recurring 502 churn).")
	w("sudo tee /etc/keycloak/keycloak.conf > /dev/null <<EOCONF")
	w("KC_DB_URL=jdbc:postgresql://%s:5432/postgres", v(s.DBEndpointVar))
	w("KC_DB_USERNAME=%s", v(s.DBUsernameVar))
	w("KC_DB_PASSWORD='%s'", v(s.DBPasswordVar))
	w("KC_DB=postgres")
	w("KC_BOOTSTRAP_ADMIN_USERNAME=admin")
	w("KC_BOOTSTRAP_ADMIN_PASSWORD='%s'", v(s.AdminPasswordVar))
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
	w("# systemd unit — JIT-VPN SPI env (fail-safe) + SES SMTP.")
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
	w("Environment=JIT_VPN_SG_ID=%s", v(s.JITVPNSecurityGroupVar))
	w("Environment=JIT_DDB_TABLE=jit-allowlist")
	w("Environment=JIT_TARGET_REALM=pyxcloud-internal")
	w("Environment=AWS_REGION=%s", v(s.RegionVar))
	w("Environment=KC_SMTP_HOST=email-smtp.%s.amazonaws.com", v(s.RegionVar))
	w("Environment=KC_SMTP_PORT=587")
	w("Environment=KC_SMTP_FROM=no-reply@%s", s.DomainName)
	w("Environment=KC_SMTP_USER=%s", v(s.SMTPUserVar))
	w("Environment=KC_SMTP_PASSWORD=%s", v(s.SMTPPasswordVar))
	w("Environment=KC_SMTP_AUTH=true")
	w("Environment=KC_SMTP_STARTTLS=true")
	w("Environment=KC_PASSOBUILD_SMTP_FROM=%s", v(s.PassobuildSenderEmailVar))
	w("[Install]")
	w("WantedBy=multi-user.target")
	w("EOL")
	w("")
	w("# Initial augmentation build (must match the hand-written features) then start.")
	w("sudo -u main bash -c 'source /etc/keycloak/keycloak.conf && /opt/keycloak/bin/kc.sh build --db=postgres --health-enabled=true --metrics-enabled=true --features=%s --vault=file'", ssoBuildFeatures)
	w("sudo systemctl daemon-reload && sudo systemctl enable keycloak && sudo systemctl restart keycloak")

	return b.String(), nil
}
