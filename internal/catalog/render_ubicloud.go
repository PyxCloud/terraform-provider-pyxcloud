package catalog

import (
	_ "embed"
	"fmt"
	"strings"
)

// This file is the COMPLETE wave-2 Ubicloud surface for the PyxCloud Terraform
// provider. It is intentionally self-contained in ONE new file so the concurrent
// wave-1 / sibling-provider PRs never conflict with it. It holds:
//
//  1. the Ubicloud catalog snapshot loader (ubicloud_catalog.csv -> the SAME
//     EmbeddedCatalog maps wave-1 resolves against), so the supported Translate*
//     functions resolve Ubicloud regions/SKUs/images with zero changes; and
//  2. the Ubicloud renderers — for the FEW components Ubicloud genuinely supports
//     via its official, verified Terraform provider
//     (ubicloud/terraform-provider-ubicloud), and clean, plan-time `unsupported`
//     errors (naming the alternative) for everything it does NOT.
//
// HONEST COVERAGE — Ubicloud has THIN Terraform support. The official provider
// (verified against its registry docs) exposes EXACTLY these resources:
//
//	ubicloud_vm, ubicloud_postgres, ubicloud_private_subnet, ubicloud_firewall,
//	ubicloud_firewall_rule, ubicloud_project
//
// There is NO bucket / object-storage resource (Ubicloud has an S3-compatible
// object API but no Terraform resource for it), NO load-balancer, NO cache, NO
// queue/stream, NO DNS/CDN/WAF, NO Kubernetes, NO secrets-manager, and NO
// serverless resource. We therefore implement only what we can VERIFY and emit a
// clean `unsupported` error (naming the closest alternative) for the rest. This
// is the correct and expected outcome for Ubicloud — we do NOT invent resources
// or schemas.
//
//	┌───────────────────────────┬──────────────────────────────┬──────────────┐
//	│ canonical component       │ ubicloud terraform resource  │ status        │
//	├───────────────────────────┼──────────────────────────────┼──────────────┤
//	│ network / private-subnet  │ ubicloud_private_subnet      │ SUPPORTED     │
//	│ security-group / firewall │ ubicloud_firewall(+_rule)    │ SUPPORTED     │
//	│ virtual-machine           │ ubicloud_vm                  │ SUPPORTED     │
//	│ managed-database (pg)     │ ubicloud_postgres            │ SUPPORTED (pg)│
//	│ managed-database (mysql)  │ —                            │ UNSUPPORTED   │
//	│ object-storage            │ — (no TF resource)           │ UNSUPPORTED   │
//	│ scale-group / lb / cache  │ —                            │ UNSUPPORTED   │
//	│ queue / stream / dns      │ —                            │ UNSUPPORTED   │
//	│ cdn / waf / k8s           │ —                            │ UNSUPPORTED   │
//	│ secrets / serverless      │ —                            │ UNSUPPORTED   │
//	└───────────────────────────┴──────────────────────────────┴──────────────┘
//
// The ONLY edits outside new files are: ProviderUbicloud in the provider-name map
// (catalog.go), one merge call in NewEmbedded (embedded.go), the `standard`
// family rank (virtualmachine.go), and one Ubicloud case per component
// render-dispatch + ResourceType switch. Everything provider-specific lives here.

// Provider-facing name for Ubicloud (Terraform `ubicloud` provider) and the
// catalog csp token. These mirror the wave-1 constants in catalog.go.
const (
	ProviderUbicloud = "ubicloud"
	cspUbicloud      = "ubicloud"
)

// ubicloudProjectVar / ubicloudSSHKeyVar are the Terraform input variables the
// Ubicloud provider requires that are NOT part of PyxCloud's abstract model: the
// Ubicloud project id (every resource is project-scoped) and the VM public SSH
// key. They are provided out-of-band by the generated fixture / the operator's
// tfvars, exactly like wave-1's var.db_password / var.lambda_role_arn pattern.
const (
	ubicloudProjectVar = "var.ubicloud_project_id"
	ubicloudSSHKeyVar  = "var.ubicloud_ssh_public_key"
)

// errUbicloudUnsupported builds the standard clean, plan-time `unsupported` error
// for a canonical component Ubicloud's Terraform provider cannot express. It names
// the closest alternative so the operator is never left guessing — never a silent
// fallback, never an invented resource.
func errUbicloudUnsupported(component, alternative string) error {
	return fmt.Errorf(
		"%s is unsupported on ubicloud: the official Ubicloud Terraform provider "+
			"(ubicloud/terraform-provider-ubicloud) exposes no resource for it. %s "+
			"(this is a hard plan-time error, never a silent fallback)",
		component, alternative,
	)
}

// ubicloudCatalogCSV is the wave-2 Ubicloud catalog snapshot (region +
// virtual_machine + OS image + managed_database rows), discriminated by a leading
// `kind` column. It is the Ubicloud analogue of the wave-1 region/vm/os/mdb CSVs,
// kept in its own file so the wave-2 PR is conflict-free against the concurrently
// edited wave-1 snapshots. See the file header for the per-kind column contract
// and the ETL provenance/gap note (there is NO live Ubicloud ETL in this repo).
//
//go:embed ubicloud_catalog.csv
var ubicloudCatalogCSV string

// loadUbicloud parses ubicloud_catalog.csv and MERGES its rows into the
// EmbeddedCatalog indexes, using the exact same keying as the wave-1 loaders so
// Ubicloud resolution is identical to AWS/GCP/DO (region -> csp_region,
// (csp,region,arch) -> SKU, (csp,region,os,ver,arch) -> image,
// (csp,region,engine) -> DB class). It is called once from NewEmbedded; a
// malformed snapshot is a hard build/parse error.
func (c *EmbeddedCatalog) loadUbicloud() error {
	lines := strings.Split(ubicloudCatalogCSV, "\n")
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ",")
		kind := strings.TrimSpace(fields[0])
		if kind == "kind" {
			continue // header
		}
		switch kind {
		case "region":
			if len(fields) != 7 {
				return fmt.Errorf("ubicloud_catalog.csv line %d: region needs 7 fields, got %d", i+1, len(fields))
			}
			row := RegionRow{
				MacroRegion:          strings.TrimSpace(fields[1]),
				Country:              strings.TrimSpace(fields[2]),
				RegionName:           strings.TrimSpace(fields[3]),
				CSPRegion:            strings.TrimSpace(fields[4]),
				CSPRegionDescription: strings.TrimSpace(fields[5]),
				CSP:                  strings.TrimSpace(fields[6]),
			}
			c.rows = append(c.rows, row)
			k := key(row.CSP, row.RegionName)
			if _, exists := c.byCSPRegion[k]; !exists {
				c.byCSPRegion[k] = row
			}
		case "vm":
			if len(fields) != 9 {
				return fmt.Errorf("ubicloud_catalog.csv line %d: vm needs 9 fields, got %d", i+1, len(fields))
			}
			row := VMRow{
				Name:              strings.TrimSpace(fields[1]),
				Family:            strings.TrimSpace(fields[2]),
				CSP:               cspUbicloud,
				CSPRegion:         strings.TrimSpace(fields[3]),
				Architecture:      strings.TrimSpace(fields[4]),
				CPU:               atoiOrZero(fields[5]),
				RAM:               atoiOrZero(fields[6]),
				GPU:               strings.TrimSpace(fields[7]),
				SupportsAutoscale: strings.EqualFold(strings.TrimSpace(fields[8]), "true"),
			}
			c.vmRows = append(c.vmRows, row)
			vk := vmRegionArchKey(row.CSP, row.CSPRegion, row.Architecture)
			c.vmByRegionArch[vk] = append(c.vmByRegionArch[vk], row)
		case "os":
			if len(fields) != 6 {
				return fmt.Errorf("ubicloud_catalog.csv line %d: os needs 6 fields, got %d", i+1, len(fields))
			}
			row := OSImageRow{
				CSP:          cspUbicloud,
				CSPRegion:    strings.TrimSpace(fields[1]),
				OSName:       strings.TrimSpace(fields[2]),
				OSVersion:    strings.TrimSpace(fields[3]),
				Architecture: strings.TrimSpace(fields[4]),
				Image:        strings.TrimSpace(fields[5]),
			}
			c.osByKey[osKey(row.CSP, row.CSPRegion, row.OSName, row.OSVersion, row.Architecture)] = row
		case "mdb":
			if len(fields) != 7 {
				return fmt.Errorf("ubicloud_catalog.csv line %d: mdb needs 7 fields, got %d", i+1, len(fields))
			}
			row := MDBRow{
				Name:      strings.TrimSpace(fields[1]),
				Family:    strings.TrimSpace(fields[2]),
				CSP:       cspUbicloud,
				CSPRegion: strings.TrimSpace(fields[3]),
				Engine:    strings.TrimSpace(fields[4]),
				CPU:       atoiOrZero(fields[5]),
				RAM:       atoiOrZero(fields[6]),
			}
			c.mdbRows = append(c.mdbRows, row)
			mk := mdbRegionEngineKey(row.CSP, row.CSPRegion, row.Engine)
			c.mdbByRegionEng[mk] = append(c.mdbByRegionEng[mk], row)
		default:
			return fmt.Errorf("ubicloud_catalog.csv line %d: unknown kind %q", i+1, kind)
		}
	}
	return nil
}

// ── SUPPORTED: network / private-subnet ──────────────────────────────────────
//
// Ubicloud's network primitive is the private subnet (ubicloud_private_subnet).
// Unlike AWS/GCP/DO, Ubicloud does NOT take a user-supplied VPC CIDR or a list of
// sub-subnets: the platform assigns net4/net6 automatically (computed attributes).
// So a PyxCloud `place` maps to ONE ubicloud_private_subnet; the canonical CIDR /
// subnet list are not provider-settable and are intentionally dropped here (the
// abstract intent — an isolated private network in a location — is preserved). A
// firewall, when present, is associated via firewall_id by the security-group
// component.
func renderNetworkUbicloud(p NetworkPlan) string {
	name := tfName(p.VPCName)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"ubicloud_private_subnet\" %q {\n", name)
	fmt.Fprintf(&b, "  project_id = %s\n", ubicloudProjectVar)
	fmt.Fprintf(&b, "  location   = %q\n", p.CSPRegion)
	fmt.Fprintf(&b, "  name       = %q\n", tfName(p.VPCName))
	// NOTE: Ubicloud assigns net4/net6 itself; the canonical cidr/subnets
	// (%s / %d subnet(s)) are not provider-settable and are omitted by design.
	fmt.Fprintf(&b, "  # canonical cidr=%q with %d subnet(s) is platform-assigned on ubicloud (net4/net6 computed)\n", p.CIDR, len(p.Subnets))
	b.WriteString("}\n")
	return b.String()
}

// ── SUPPORTED: security-group / firewall ─────────────────────────────────────
//
// Ubicloud firewalls (ubicloud_firewall) carry their rules as separate
// ubicloud_firewall_rule resources (CIDR + "lo..hi" port_range). Ubicloud
// firewalls are INGRESS-only allow rules — there is no egress / source-SG concept
// — so egress and peer-SG rules are intentionally not emitted (the ingress allow
// rules are the security surface). A non-port protocol (icmp/all) opens the full
// 0..65535 range since Ubicloud rules are port-range-only (no protocol field).
func renderSGUbicloud(p SecurityGroupPlan) string {
	name := tfName(p.SGName)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"ubicloud_firewall\" %q {\n", name)
	fmt.Fprintf(&b, "  project_id = %s\n", ubicloudProjectVar)
	fmt.Fprintf(&b, "  location   = %q\n", p.CSPRegion)
	fmt.Fprintf(&b, "  name       = %q\n", tfName(p.SGName))
	if desc := asciiOnly(p.Description); desc != "" {
		fmt.Fprintf(&b, "  description = %q\n", desc)
	}
	b.WriteString("}\n")

	idx := 0
	for _, r := range p.Rules {
		// Ubicloud firewall rules are ingress allow rules only; skip egress and
		// peer-SG-scoped rules (no Ubicloud equivalent).
		if r.Direction != DirIngress || r.SourceSG != "" {
			continue
		}
		from, to := r.FromPort, r.ToPort
		if r.Protocol == ProtoICMP || r.Protocol == ProtoAll || (from == 0 && to == 0) {
			from, to = 0, 65535
		}
		for _, cidr := range r.CIDRs {
			idx++
			rn := fmt.Sprintf("%s_rule_%d", name, idx)
			fmt.Fprintf(&b, "\nresource \"ubicloud_firewall_rule\" %q {\n", rn)
			fmt.Fprintf(&b, "  project_id    = %s\n", ubicloudProjectVar)
			fmt.Fprintf(&b, "  location      = %q\n", p.CSPRegion)
			fmt.Fprintf(&b, "  firewall_name = ubicloud_firewall.%s.name\n", name)
			fmt.Fprintf(&b, "  cidr          = %q\n", cidr)
			fmt.Fprintf(&b, "  port_range    = %q\n", fmt.Sprintf("%d..%d", from, to))
			b.WriteString("}\n")
		}
	}
	return b.String()
}

// ── SUPPORTED: virtual-machine ───────────────────────────────────────────────
//
// ubicloud_vm: size + boot_image come from the catalog (the resolved standard-N
// SKU and the boot_image slug); the VM joins the place's private subnet by
// private_subnet_id. public_key is required by the provider and supplied
// out-of-band (var). enable_ip4 is on by default (a reachable VM). count VMs are
// emitted, one resource each, mirroring the wave-1 VM renderer.
func renderVMUbicloud(p VMPlan) string {
	var b strings.Builder
	for _, inst := range p.Instances {
		rn := tfName(inst.Name)
		fmt.Fprintf(&b, "resource \"ubicloud_vm\" %q {\n", rn)
		fmt.Fprintf(&b, "  project_id = %s\n", ubicloudProjectVar)
		fmt.Fprintf(&b, "  location   = %q\n", p.CSPRegion)
		fmt.Fprintf(&b, "  name       = %q\n", tfName(inst.Name))
		fmt.Fprintf(&b, "  size       = %q\n", p.InstanceType)
		fmt.Fprintf(&b, "  boot_image = %q\n", p.Image)
		fmt.Fprintf(&b, "  public_key = %s\n", ubicloudSSHKeyVar)
		b.WriteString("  unix_user  = \"ubi\"\n")
		b.WriteString("  enable_ip4 = true\n")
		if p.NetworkName != "" {
			fmt.Fprintf(&b, "  private_subnet_id = ubicloud_private_subnet.%s.id\n", tfName(p.NetworkName))
		}
		b.WriteString("}\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// ── SUPPORTED: managed-database (Postgres only) ──────────────────────────────
//
// ubicloud_postgres: Ubicloud Managed Postgres bills the underlying VM size
// (size = standard-N from the catalog). HA maps to ha_type ("async" standby).
// storage_size carries the allocated GiB. Ubicloud Postgres is PostgreSQL only —
// a MySQL engine has NO Ubicloud resource and is rejected here with a clean
// unsupported error (the guard is at render so a hand-built mysql plan can never
// emit an invented resource).
func renderMDBUbicloud(p ManagedDatabasePlan) (string, error) {
	if !strings.EqualFold(p.Engine, DBEnginePostgres) {
		return "", errUbicloudUnsupported(
			fmt.Sprintf("managed-database engine %q", p.Engine),
			"Ubicloud Managed Database is PostgreSQL-only (ubicloud_postgres); use engine=postgres, "+
				"or place this database on a provider that offers managed MySQL (aws / gcp / digitalocean).",
		)
	}
	name := tfName(p.DBName)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"ubicloud_postgres\" %q {\n", name)
	fmt.Fprintf(&b, "  project_id = %s\n", ubicloudProjectVar)
	fmt.Fprintf(&b, "  location   = %q\n", p.CSPRegion)
	fmt.Fprintf(&b, "  name       = %q\n", name)
	fmt.Fprintf(&b, "  size       = %q\n", p.DBClass)
	if p.EngineVersion != "" {
		fmt.Fprintf(&b, "  version    = %q\n", majorPGVersion(p.EngineVersion))
	}
	if p.StorageGB > 0 {
		fmt.Fprintf(&b, "  storage_size = %d\n", p.StorageGB)
	}
	// HA: Ubicloud Postgres ha_type ("async" = standby replica). Single instance
	// ("none") otherwise. DeletionProtection has no in-place Ubicloud flag, so the
	// production intent is carried as a lifecycle prevent_destroy guard (mirrors the
	// wave-1 DigitalOcean managed-database renderer).
	if p.HA {
		b.WriteString("  ha_type    = \"async\"\n")
	} else {
		b.WriteString("  ha_type    = \"none\"\n")
	}
	if p.DeletionProtection {
		b.WriteString("  lifecycle {\n")
		b.WriteString("    prevent_destroy = true\n")
		b.WriteString("  }\n")
	}
	b.WriteString("}\n")
	return b.String(), nil
}

// majorPGVersion reduces a possibly-dotted engine version ("16", "16.4") to the
// PostgreSQL major line Ubicloud expects (e.g. "16").
func majorPGVersion(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.IndexByte(v, '.'); i >= 0 {
		return v[:i]
	}
	return v
}
