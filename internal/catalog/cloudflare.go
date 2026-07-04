package catalog

import (
	"context"
	"fmt"
	"strings"
)

// CloudflareDNS is the abstract `dns` component for Cloudflare — the canonical
// form of the per-provider scripts' cloudflare_dns_record glue. It is CROSS-CUTTING:
// Cloudflare is an edge/DNS provider used ALONGSIDE the cloud provider (an AWS env
// with Cloudflare DNS), so it renders cloudflare_* resources regardless of the
// environment's cloud provider, and requires the cloudflare/cloudflare terraform
// provider (auth via the CLOUDFLARE_API_TOKEN env var — the same way our scripts
// authenticate today).
//
// This is distinct from the catalog `dns-zone` component, which is cloud-native
// DNS (Route53 / Cloud DNS / DO). Our repos use Cloudflare, hence this component.

// DNSRecord is one Cloudflare DNS record.
type DNSRecord struct {
	Name    string // record name, e.g. "api" or "api.example.com"
	Type    string // A | AAAA | CNAME | TXT | MX | ...
	Content string // the record value (IP, target host, text)
	TTL     int    // seconds; 1 = automatic
	Proxied bool   // orange-cloud (proxied through Cloudflare)
}

// CloudflareDNSSpec is the abstract Cloudflare DNS description. ZoneID is supplied
// out of band (a var / the CLOUDFLARE_ZONE_ID env), never invented.
type CloudflareDNSSpec struct {
	Name    string
	ZoneID  string
	Records []DNSRecord
}

// CloudflareDNSPlan is the resolved concrete plan.
type CloudflareDNSPlan struct {
	Name         string      `json:"name"`
	ZoneID       string      `json:"zone_id"`
	Records      []DNSRecord `json:"records"`
	ResourceType string      `json:"resource_type"`
}

var dnsRecordTypes = map[string]bool{
	"A": true, "AAAA": true, "CNAME": true, "TXT": true, "MX": true, "NS": true,
	"SRV": true, "CAA": true, "PTR": true,
}

// TranslateCloudflareDNS validates and resolves a CloudflareDNSSpec. Provider-
// independent (Cloudflare is global), so no region/cloud-provider resolution.
func TranslateCloudflareDNS(_ context.Context, spec CloudflareDNSSpec) (CloudflareDNSPlan, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return CloudflareDNSPlan{}, fmt.Errorf("dns: name is required")
	}
	if len(spec.Records) == 0 {
		return CloudflareDNSPlan{}, fmt.Errorf("dns: at least one record is required")
	}
	for i, r := range spec.Records {
		if strings.TrimSpace(r.Name) == "" || strings.TrimSpace(r.Content) == "" {
			return CloudflareDNSPlan{}, fmt.Errorf("dns: record %d needs a name and content", i+1)
		}
		if !dnsRecordTypes[strings.ToUpper(strings.TrimSpace(r.Type))] {
			return CloudflareDNSPlan{}, fmt.Errorf("dns: record %q has unsupported type %q", r.Name, r.Type)
		}
	}
	return CloudflareDNSPlan{
		Name: spec.Name, ZoneID: spec.ZoneID, Records: spec.Records,
		ResourceType: "cloudflare_dns_record",
	}, nil
}

// RenderCloudflareDNSHCL renders a CloudflareDNSPlan into cloudflare_dns_record
// resources. zone_id falls back to a var so it is supplied out of band.
func RenderCloudflareDNSHCL(p CloudflareDNSPlan) (string, error) {
	var b strings.Builder
	zoneRef := fmt.Sprintf("%q", p.ZoneID)
	if strings.TrimSpace(p.ZoneID) == "" {
		zoneRef = "var.cloudflare_zone_id"
		b.WriteString("variable \"cloudflare_zone_id\" {\n  type = string\n}\n\n")
	}
	for i, r := range p.Records {
		rn := tfName(fmt.Sprintf("%s-%d", p.Name, i+1))
		fmt.Fprintf(&b, "resource \"cloudflare_dns_record\" %q {\n", rn)
		fmt.Fprintf(&b, "  zone_id = %s\n", zoneRef)
		fmt.Fprintf(&b, "  name    = %q\n", r.Name)
		fmt.Fprintf(&b, "  type    = %q\n", strings.ToUpper(r.Type))
		fmt.Fprintf(&b, "  content = %q\n", r.Content)
		ttl := r.TTL
		if ttl <= 0 {
			ttl = 1 // automatic
		}
		fmt.Fprintf(&b, "  ttl     = %d\n", ttl)
		fmt.Fprintf(&b, "  proxied = %t\n", r.Proxied)
		b.WriteString("}\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n", nil
}

// cloudflareRequiredProviders is the terraform block that pins the Cloudflare
// provider source — emitted by AssembleHCL when any Cloudflare component is present
// (terraform would otherwise assume the non-existent hashicorp/cloudflare).
const cloudflareRequiredProviders = `terraform {
  required_providers {
    cloudflare = {
      source = "cloudflare/cloudflare"
    }
  }
}
`

// ── Cloudflare CDN (arbitrary-origin) ────────────────────────────────────────
//
// CloudflareCDN is the cross-cutting CDN component for non-Spaces origins on
// DigitalOcean (B5 gap). DigitalOcean's `digitalocean_cdn` can only front a
// Spaces (object-storage) origin; for any other origin (load-balancer / custom
// domain) we route through Cloudflare's proxy instead. The implementation emits:
//
//   - A proxied CNAME `cloudflare_dns_record` (orange-cloud) pointing to the
//     origin's hostname — Cloudflare's proxy IS the CDN layer.
//   - A `cloudflare_zone_settings_override` that sets Browser Cache TTL and
//     enables "Always Online" (origin-down resilience), giving a minimal but
//     meaningful CDN configuration out of the box.
//
// ZoneID is supplied out of band (var.cloudflare_zone_id) exactly as the
// CloudflareDNS component does; the host/subdomain defaults to the component name.

// CloudflareCDNSpec is the abstract description for a Cloudflare CDN front.
type CloudflareCDNSpec struct {
	Name       string // component name (used as the subdomain if Host is empty)
	ZoneID     string // Cloudflare zone ID (empty → var.cloudflare_zone_id)
	Host       string // subdomain to proxy, e.g. "cdn" or "assets" (defaults to Name)
	OriginHost string // the origin's public hostname (LB DNS name or custom domain)
}

// CloudflareCDNPlan is the resolved Cloudflare CDN plan.
type CloudflareCDNPlan struct {
	Name         string `json:"name"`
	ZoneID       string `json:"zone_id"`
	Host         string `json:"host"`
	OriginHost   string `json:"origin_host"`
	ResourceType string `json:"resource_type"`
}

// TranslateCloudfareCDN validates and resolves a CloudflareCDNSpec.
// Provider-independent: Cloudflare is global, so no region/cloud-provider resolution.
func TranslateCloudfareCDN(_ context.Context, spec CloudflareCDNSpec) (CloudflareCDNPlan, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return CloudflareCDNPlan{}, fmt.Errorf("cloudflare-cdn: name is required")
	}
	host := strings.TrimSpace(spec.Host)
	if host == "" {
		host = strings.TrimSpace(spec.Name)
	}
	return CloudflareCDNPlan{
		Name:         spec.Name,
		ZoneID:       spec.ZoneID,
		Host:         host,
		OriginHost:   strings.TrimSpace(spec.OriginHost),
		ResourceType: "cloudflare_dns_record",
	}, nil
}

// RenderCloudfareCDNHCL renders a CloudflareCDNPlan into a proxied CNAME record
// (enabling Cloudflare's CDN proxy) plus a zone_settings_override that applies
// sensible CDN defaults (browser-cache TTL + Always Online).
func RenderCloudfareCDNHCL(p CloudflareCDNPlan) (string, error) {
	var b strings.Builder
	rn := tfName(p.Name + "-cdn")

	zoneRef := fmt.Sprintf("%q", p.ZoneID)
	if strings.TrimSpace(p.ZoneID) == "" {
		zoneRef = "var.cloudflare_zone_id"
		b.WriteString("variable \"cloudflare_zone_id\" {\n  type = string\n}\n\n")
	}

	origin := p.OriginHost
	if origin == "" {
		origin = "origin.example.com"
	}

	// Proxied CNAME — the orange-cloud proxy IS the CDN layer.
	fmt.Fprintf(&b, "# pyxcloud cloudflare-cdn: arbitrary-origin CDN via Cloudflare proxy\n")
	fmt.Fprintf(&b, "resource \"cloudflare_dns_record\" %q {\n", rn)
	fmt.Fprintf(&b, "  zone_id = %s\n", zoneRef)
	fmt.Fprintf(&b, "  name    = %q\n", p.Host)
	b.WriteString("  type    = \"CNAME\"\n")
	fmt.Fprintf(&b, "  content = %q\n", origin)
	b.WriteString("  ttl     = 1\n") // 1 = automatic (required for proxied records)
	b.WriteString("  proxied = true\n")
	b.WriteString("}\n\n")

	// Zone-level CDN settings: Browser Cache TTL + Always Online + SSL. The
	// cloudflare/cloudflare v5 provider models each zone setting as its OWN
	// cloudflare_zone_setting resource (setting_id + value), replacing the removed
	// cloudflare_zone_settings_override block resource.
	zoneSettings := []struct {
		id    string
		value string // rendered verbatim (a number for TTL, a quoted string otherwise)
	}{
		{"browser_cache_ttl", "14400"}, // 4 hours
		{"always_online", "\"on\""},    // origin-down resilience
		{"ssl", "\"flexible\""},
	}
	for _, s := range zoneSettings {
		sn := tfName(p.Name + "-cdn-" + s.id)
		fmt.Fprintf(&b, "resource \"cloudflare_zone_setting\" %q {\n", sn)
		fmt.Fprintf(&b, "  zone_id    = %s\n", zoneRef)
		fmt.Fprintf(&b, "  setting_id = %q\n", s.id)
		fmt.Fprintf(&b, "  value      = %s\n", s.value)
		b.WriteString("}\n\n")
	}

	return strings.TrimRight(b.String(), "\n") + "\n", nil
}
