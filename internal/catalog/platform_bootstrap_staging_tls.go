package catalog

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	stagingTLSShellNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	stagingTLSDomainRE    = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)
	stagingTLSEmailRE     = regexp.MustCompile(`^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$`)
	stagingTLSVaultPathRE = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)
	stagingTLSVaultKeyRE  = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

// StagingTLSBootstrapSpec describes ACME DNS-01 certificate automation for a
// private staging origin. The Cloudflare token value is never a Terraform
// input: the rendered host fetches it directly from Vault at issuance time.
type StagingTLSBootstrapSpec struct {
	Domains          []string
	ACMEEmail        string
	VaultAddrVar     string
	VaultRoleIDVar   string
	VaultSecretIDVar string
	VaultKVPath      string
	VaultKey         string
}

func (s StagingTLSBootstrapSpec) withDefaults() StagingTLSBootstrapSpec {
	def := func(value, fallback string) string {
		if strings.TrimSpace(value) == "" {
			return fallback
		}
		return strings.TrimSpace(value)
	}
	if len(s.Domains) == 0 {
		s.Domains = []string{"staging.passo.build", "staging-console.passo.build"}
	}
	s.ACMEEmail = def(s.ACMEEmail, "security@pyxcloud.io")
	s.VaultAddrVar = def(s.VaultAddrVar, "vault_addr")
	s.VaultRoleIDVar = def(s.VaultRoleIDVar, "staging_fe_vault_role_id")
	s.VaultSecretIDVar = def(s.VaultSecretIDVar, "staging_fe_vault_secret_id")
	s.VaultKVPath = strings.Trim(def(s.VaultKVPath, "infra/staging/staging-tls/cloudflare"), "/")
	s.VaultKey = def(s.VaultKey, "dns_api_token")
	return s
}

func (s StagingTLSBootstrapSpec) validate() error {
	for _, value := range []string{s.VaultAddrVar, s.VaultRoleIDVar, s.VaultSecretIDVar} {
		if !stagingTLSShellNameRE.MatchString(value) {
			return fmt.Errorf("staging TLS: unsafe Terraform variable name %q", value)
		}
	}
	if !stagingTLSEmailRE.MatchString(s.ACMEEmail) {
		return fmt.Errorf("staging TLS: invalid ACME email %q", s.ACMEEmail)
	}
	if s.VaultKVPath == "" || strings.Contains(s.VaultKVPath, "..") || !stagingTLSVaultPathRE.MatchString(s.VaultKVPath) {
		return fmt.Errorf("staging TLS: unsafe Vault KV path %q", s.VaultKVPath)
	}
	if !stagingTLSVaultKeyRE.MatchString(s.VaultKey) {
		return fmt.Errorf("staging TLS: unsafe Vault key %q", s.VaultKey)
	}
	if len(s.Domains) == 0 {
		return fmt.Errorf("staging TLS: at least one domain is required")
	}
	for _, domain := range s.Domains {
		if !stagingTLSDomainRE.MatchString(domain) || !strings.Contains(domain, ".") {
			return fmt.Errorf("staging TLS: unsafe domain %q", domain)
		}
	}
	return nil
}

// RenderStagingTLSBootstrap renders fail-closed ACME DNS-01 automation. It
// intentionally has no HTTP-01 path and no alternate certificate: nginx users
// must reference the resulting Let's Encrypt fullchain and private key.
func RenderStagingTLSBootstrap(spec StagingTLSBootstrapSpec) (string, error) {
	s := spec.withDefaults()
	if err := s.validate(); err != nil {
		return "", err
	}

	primary := s.Domains[0]
	domainArgs := make([]string, 0, len(s.Domains)*2)
	for _, domain := range s.Domains {
		domainArgs = append(domainArgs, "-d", domain)
	}

	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	w("#!/usr/bin/env bash")
	w("set -euo pipefail")
	w("export DEBIAN_FRONTEND=noninteractive")
	w("apt-get update -y")
	w("apt-get install -y ca-certificates curl openssl python3 certbot python3-certbot-dns-cloudflare")
	w("install -d -m 0700 /run/staging-tls")
	w("")
	w("cat >/usr/local/sbin/staging-tls-renew <<'RENEW'")
	w("#!/usr/bin/env bash")
	w("set -euo pipefail")
	w("cleanup() {")
	w("  if [ -f /run/staging-tls/cloudflare.ini ]; then shred -u /run/staging-tls/cloudflare.ini; fi")
	w("  unset CLOUDFLARE_DNS_API_TOKEN VAULT_TOKEN VAULT_SECRET_ID VAULT_LOGIN_RESP VAULT_READ_RESP")
	w("}")
	w("trap cleanup EXIT INT TERM")
	w("install -d -m 0700 /run/staging-tls")
	w("%s", VaultBootFetchSnippet(s.VaultAddrVar, s.VaultRoleIDVar, s.VaultSecretIDVar, s.VaultKVPath, s.VaultKey, "CLOUDFLARE_DNS_API_TOKEN"))
	w("cat >/run/staging-tls/cloudflare.ini <<CREDENTIALS")
	w("dns_cloudflare_api_token = $CLOUDFLARE_DNS_API_TOKEN")
	w("CREDENTIALS")
	w("chmod 0600 /run/staging-tls/cloudflare.ini")
	w("certbot certonly --non-interactive --agree-tos --dns-cloudflare --dns-cloudflare-credentials /run/staging-tls/cloudflare.ini --dns-cloudflare-propagation-seconds 30 --keep-until-expiring --cert-name %s --email %s %s", primary, s.ACMEEmail, strings.Join(domainArgs, " "))
	w("test -s /etc/letsencrypt/live/%s/fullchain.pem", primary)
	w("test -s /etc/letsencrypt/live/%s/privkey.pem", primary)
	w("openssl x509 -in /etc/letsencrypt/live/%s/fullchain.pem -noout -checkend 86400", primary)
	w("if systemctl is-active --quiet nginx; then nginx -t && systemctl reload nginx; fi")
	w("RENEW")
	w("chmod 0700 /usr/local/sbin/staging-tls-renew")
	w("")
	w("cat >/etc/systemd/system/staging-tls-renew.service <<'UNIT'")
	w("[Unit]")
	w("Description=Renew private staging origin certificate through ACME DNS-01")
	w("After=network-online.target")
	w("Wants=network-online.target")
	w("")
	w("[Service]")
	w("Type=oneshot")
	w("ExecStart=/usr/local/sbin/staging-tls-renew")
	w("PrivateTmp=true")
	w("NoNewPrivileges=true")
	w("UNIT")
	w("")
	w("cat >/etc/systemd/system/staging-tls-renew.timer <<'UNIT'")
	w("[Unit]")
	w("Description=Daily private staging origin certificate renewal check")
	w("")
	w("[Timer]")
	w("OnCalendar=*-*-* 03:17:00")
	w("RandomizedDelaySec=30m")
	w("Persistent=true")
	w("")
	w("[Install]")
	w("WantedBy=timers.target")
	w("UNIT")
	w("systemctl daemon-reload")
	w("systemctl start staging-tls-renew.service")
	w("systemctl enable --now staging-tls-renew.timer")
	w("test -s /etc/letsencrypt/live/%s/fullchain.pem", primary)
	w("test -s /etc/letsencrypt/live/%s/privkey.pem", primary)

	return b.String(), nil
}
