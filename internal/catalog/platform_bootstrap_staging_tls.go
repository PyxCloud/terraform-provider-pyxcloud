package catalog

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	stagingTLSDomainRE    = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)
	stagingTLSEmailRE     = regexp.MustCompile(`^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$`)
	stagingTLSVaultPathRE = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)
	stagingTLSVaultKeyRE  = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

// StagingTLSBootstrapSpec describes certificate issuance performed by an
// already-authenticated converge process. No Vault login credential is part of
// this spec: staging-tls-issue requires a short-lived VAULT_TOKEN inherited in
// memory and removes the DNS credential from /run when it exits.
type StagingTLSBootstrapSpec struct {
	Domains     []string
	ACMEEmail   string
	VaultKVPath string
	VaultKey    string
}

func (s StagingTLSBootstrapSpec) withDefaults() StagingTLSBootstrapSpec {
	def := func(value, fallback string) string {
		if strings.TrimSpace(value) == "" {
			return fallback
		}
		return strings.TrimSpace(value)
	}
	if len(s.Domains) == 0 {
		s.Domains = []string{"staging.passo.build", "staging-console.pyxcloud.io"}
	}
	s.ACMEEmail = def(s.ACMEEmail, "security@pyxcloud.io")
	s.VaultKVPath = strings.Trim(def(s.VaultKVPath, "infra/staging/staging-tls/cloudflare"), "/")
	s.VaultKey = def(s.VaultKey, "dns_api_token")
	return s
}

func (s StagingTLSBootstrapSpec) validate() error {
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

// RenderStagingTLSBootstrap installs two secret-free host helpers:
// staging-tls-issue consumes the converge process's short-lived Vault token;
// staging-tls-check only alarms when external convergence must renew the cert.
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
	w("apt-get install -y --no-install-recommends certbot python3-certbot-dns-cloudflare")
	w("install -d -m 0700 /run/staging-tls")
	w("cat >/usr/local/sbin/staging-tls-issue <<'ISSUE'")
	w("#!/usr/bin/env bash")
	w("set -euo pipefail")
	w(`test -n "${VAULT_ADDR:-}" || { echo 'staging-tls: VAULT_ADDR is required' >&2; exit 1; }`)
	w(`test -n "${VAULT_TOKEN:-}" || { echo 'staging-tls: an in-memory VAULT_TOKEN is required' >&2; exit 1; }`)
	w("cleanup() {")
	w("  shred -u /run/staging-tls/cloudflare.ini /run/staging-tls/vault-curl.conf 2>/dev/null || true")
	w("  unset CLOUDFLARE_DNS_API_TOKEN VAULT_READ_RESP")
	w("}")
	w("trap cleanup EXIT INT TERM")
	w("install -d -m 0700 /run/staging-tls")
	w("printf 'header = \"X-Vault-Token: %%s\"\\n' \"$VAULT_TOKEN\" >/run/staging-tls/vault-curl.conf")
	w("chmod 0600 /run/staging-tls/vault-curl.conf")
	w("VAULT_READ_RESP=$(curl -fsS --config /run/staging-tls/vault-curl.conf \"$VAULT_ADDR/v1/secret/data/%s\")", s.VaultKVPath)
	w("CLOUDFLARE_DNS_API_TOKEN=$(printf '%%s' \"$VAULT_READ_RESP\" | python3 -c 'import json,sys; print(json.load(sys.stdin).get(\"data\",{}).get(\"data\",{}).get(\"%s\",\"\"))')", s.VaultKey)
	w("test -n \"$CLOUDFLARE_DNS_API_TOKEN\" || { echo 'staging-tls: DNS token missing' >&2; exit 1; }")
	w("printf 'dns_cloudflare_api_token = %%s\\n' \"$CLOUDFLARE_DNS_API_TOKEN\" >/run/staging-tls/cloudflare.ini")
	w("chmod 0600 /run/staging-tls/cloudflare.ini")
	w("certbot certonly --non-interactive --agree-tos --dns-cloudflare --dns-cloudflare-credentials /run/staging-tls/cloudflare.ini --dns-cloudflare-propagation-seconds 30 --keep-until-expiring --cert-name %s --email %s %s", primary, s.ACMEEmail, strings.Join(domainArgs, " "))
	w("test -s /etc/letsencrypt/live/%s/fullchain.pem", primary)
	w("test -s /etc/letsencrypt/live/%s/privkey.pem", primary)
	w("openssl x509 -in /etc/letsencrypt/live/%s/fullchain.pem -noout -checkend 86400", primary)
	w("ISSUE")
	w("chmod 0700 /usr/local/sbin/staging-tls-issue")
	w("")
	w("cat >/usr/local/sbin/staging-tls-check <<'CHECK'")
	w("#!/usr/bin/env bash")
	w("set -euo pipefail")
	w("CERT=/etc/letsencrypt/live/%s/fullchain.pem", primary)
	w("test -s \"$CERT\"")
	w("openssl x509 -checkend 604800 -noout -in \"$CERT\" || {")
	w("  echo 'staging TLS certificate needs external wrapped-token convergence within 7 days' | systemd-cat -p warning -t staging-tls-check")
	w("  exit 1")
	w("}")
	w("CHECK")
	w("chmod 0755 /usr/local/sbin/staging-tls-check")
	w("cat >/etc/systemd/system/staging-tls-check.service <<'UNIT'")
	w("[Unit]")
	w("Description=Check private staging certificate renewal horizon")
	w("[Service]")
	w("Type=oneshot")
	w("ExecStart=/usr/local/sbin/staging-tls-check")
	w("NoNewPrivileges=true")
	w("PrivateTmp=true")
	w("UNIT")
	w("cat >/etc/systemd/system/staging-tls-check.timer <<'UNIT'")
	w("[Unit]")
	w("Description=Daily check for externally converged staging TLS")
	w("[Timer]")
	w("OnCalendar=*-*-* 03:17:00")
	w("RandomizedDelaySec=30m")
	w("Persistent=true")
	w("[Install]")
	w("WantedBy=timers.target")
	w("UNIT")
	w("systemctl daemon-reload")
	w("systemctl enable --now staging-tls-check.timer")

	return b.String(), nil
}
