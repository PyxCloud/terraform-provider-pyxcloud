package catalog

// Provider Adapter API (roadmap P6, pd-TF-PROVIDER-ADAPTER).
//
// A tier-2 provider is registered as DATA: an AdapterManifest (per-component HCL
// templates + terraform provider coordinates) and one catalog CSV (region/vm/os/mdb
// rows, same shape as the wave-2 snapshots) folded into the shared resolution
// indexes. Nothing here is provider-specific Go code: adding a provider means
// adding manifest + CSV data and one registration line in NewEmbedded — this is
// what the planned `pyx provider add` CLI consumes.
//
// Coverage honesty: an adapter provider counts as natively supported ONLY for the
// component kinds its manifest templates actually render (VM/network/SG always;
// managed-database only when MDBTemplate != ""). Every other canonical type falls
// through to the standard self-host mitigation path, exactly like the wave-2
// providers. Adapter manifests are validated against public terraform provider
// docs at authoring time; they are not live-API verified (see snapshot notes).

import (
	"context"
	"fmt"
	"strings"
	"text/template"
)

// AdapterManifest is the full data-only description of an adapter-backed provider.
type AdapterManifest struct {
	// Provider is the provider-facing name (terraform `provider` attribute).
	Provider string
	// CSP is the catalog csp token (matches the CSV `csp` column).
	CSP string
	// TFLocal is the terraform required_providers local name (resource prefix).
	TFLocal string
	// TFSource is the terraform registry source (org/name).
	TFSource string
	// Note documents provenance/limitations shown in render headers.
	Note string

	// VMTemplate renders one `{{range}}`-free block per instance (context: VMPlan).
	VMTemplate string
	// NetTemplate renders the network/VPC (context: NetworkPlan). Optional.
	NetTemplate string
	// SubnetTemplate renders ONE subnet (context: adapterSubnetCtx). Optional.
	SubnetTemplate string
	// SGTemplate renders the firewall/security group (context: SecurityGroupPlan).
	SGTemplate string
	// MDBTemplate renders one managed database (context: ManagedDatabasePlan).
	// Empty = the provider has no native managed DB; the standard mitigation
	// (self-host on VM) applies.
	MDBTemplate string
}

// adapterSubnetCtx is the template context for one subnet render.
type adapterSubnetCtx struct {
	Network NetworkPlan
	Subnet  SubnetPlan
}

// adapter_registry.go holds only the manifests; the empty registry map was
// removed in favour of the single declaration in adapter_manifests.go.

// AdapterFor returns the manifest for an adapter-backed provider, or nil.
func AdapterFor(provider string) *AdapterManifest {
	return AdapterManifests[strings.ToLower(strings.TrimSpace(provider))]
}

// IsAdapterProvider reports whether the provider renders via the adapter engine.
func IsAdapterProvider(provider string) bool {
	return AdapterFor(provider) != nil
}

// ── Rendering ─────────────────────────────────────────────────────────────────

var adapterFuncs = template.FuncMap{
	"tfName":              tfName,
	"subnetResourceLabel": subnetResourceLabel,
	"mul":                 func(a, b int) int { return a * b },
	"subnetOctet": func(cidr string) int {
		// third octet of an IPv4 CIDR, used to derive deterministic gateway IPs
		parts := strings.Split(cidr, ".")
		if len(parts) < 3 {
			return 0
		}
		n := 0
		for _, c := range parts[2] {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
		}
		return n
	},
}

func adapterTemplate(name, body string) *template.Template {
	return template.Must(template.New(name).Funcs(adapterFuncs).Parse(strings.TrimSpace(body) + "\n"))
}

// RenderAdapterVM renders one VM instance block via the manifest template.
func RenderAdapterVM(m *AdapterManifest, p VMPlan) (string, error) {
	t := adapterTemplate("vm", m.VMTemplate)
	var b strings.Builder
	for _, inst := range p.Instances {
		p2 := p
		p2.VMName = inst.Name
		if err := t.Execute(&b, p2); err != nil {
			return "", fmt.Errorf("adapter %s vm template: %w", m.Provider, err)
		}
	}
	return b.String(), nil
}

// RenderAdapterNetwork renders the VPC + subnets via the manifest templates.
func RenderAdapterNetwork(m *AdapterManifest, p NetworkPlan) (string, error) {
	var b strings.Builder
	if m.NetTemplate != "" {
		if err := adapterTemplate("net", m.NetTemplate).Execute(&b, p); err != nil {
			return "", fmt.Errorf("adapter %s net template: %w", m.Provider, err)
		}
	}
	if m.SubnetTemplate != "" {
		st := adapterTemplate("subnet", m.SubnetTemplate)
		for _, s := range p.Subnets {
			if err := st.Execute(&b, adapterSubnetCtx{Network: p, Subnet: s}); err != nil {
				return "", fmt.Errorf("adapter %s subnet template: %w", m.Provider, err)
			}
		}
	}
	return b.String(), nil
}

// RenderAdapterSG renders the firewall/SG via the manifest template.
func RenderAdapterSG(m *AdapterManifest, p SecurityGroupPlan) (string, error) {
	var b strings.Builder
	if err := adapterTemplate("sg", m.SGTemplate).Execute(&b, p); err != nil {
		return "", fmt.Errorf("adapter %s sg template: %w", m.Provider, err)
	}
	return b.String(), nil
}

// AdapterMDBSupported reports whether the adapter provider has a native
// managed-database render.
func AdapterMDBSupported(m *AdapterManifest) bool {
	return strings.TrimSpace(m.MDBTemplate) != ""
}

// RenderAdapterMDB renders one managed database via the manifest template.
func RenderAdapterMDB(m *AdapterManifest, p ManagedDatabasePlan) (string, error) {
	var b strings.Builder
	if err := adapterTemplate("mdb", m.MDBTemplate).Execute(&b, p); err != nil {
		return "", fmt.Errorf("adapter %s mdb template: %w", m.Provider, err)
	}
	return b.String(), nil
}

// adapterHeader is the provenance line every adapter doc carries so rendered
// modules are honest about what produced them.
func adapterHeader(m *AdapterManifest, kind string) string {
	note := strings.TrimSpace(m.Note)
	if note != "" {
		note = " " + note
	}
	return fmt.Sprintf("# pyxcloud adapter render: %s via %s (%s)%s\n", kind, m.TFLocal, m.TFSource, note)
}

// ── Catalog folding ───────────────────────────────────────────────────────────
//
// foldAdapterCatalog parses an adapter CSV (region/vm/os/mdb rows, EXACTLY the
// wave-2 snapshot shape) and merges it into the shared EmbeddedCatalog indexes,
// so the standard ResolveRegion/ResolveSKU/ResolveImage/ResolveDBClass paths
// work for adapter providers with no second engine.

// foldAdapterCatalog merges one adapter provider's CSV into the indexes.
func (c *EmbeddedCatalog) foldAdapterCatalog(m *AdapterManifest, csv string) error {
	for i, raw := range strings.Split(csv, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, ",")
		kind := strings.TrimSpace(f[0])
		switch kind {
		case "region":
			if len(f) != 7 {
				return fmt.Errorf("%s adapter csv line %d: region needs 7 fields, got %d", m.Provider, i+1, len(f))
			}
			row := RegionRow{
				MacroRegion:          strings.TrimSpace(f[1]),
				Country:              strings.TrimSpace(f[2]),
				RegionName:           strings.TrimSpace(f[3]),
				CSPRegion:            strings.TrimSpace(f[4]),
				CSPRegionDescription: strings.TrimSpace(f[5]),
				CSP:                  m.CSP,
			}
			c.rows = append(c.rows, row)
			k := key(row.CSP, row.RegionName)
			if _, exists := c.byCSPRegion[k]; !exists {
				c.byCSPRegion[k] = row
			}
		case "vm":
			if len(f) != 9 {
				return fmt.Errorf("%s adapter csv line %d: vm needs 9 fields, got %d", m.Provider, i+1, len(f))
			}
			row := VMRow{
				Name:              strings.TrimSpace(f[1]),
				Family:            strings.TrimSpace(f[2]),
				CSP:               m.CSP,
				CSPRegion:         strings.TrimSpace(f[3]),
				Architecture:      strings.TrimSpace(f[4]),
				CPU:               atoiOrZero(f[5]),
				RAM:               atoiOrZero(f[6]),
				GPU:               strings.TrimSpace(f[7]),
				SupportsAutoscale: strings.EqualFold(strings.TrimSpace(f[8]), "true"),
			}
			c.vmRows = append(c.vmRows, row)
			vk := vmRegionArchKey(row.CSP, row.CSPRegion, row.Architecture)
			c.vmByRegionArch[vk] = append(c.vmByRegionArch[vk], row)
		case "os":
			if len(f) != 6 {
				return fmt.Errorf("%s adapter csv line %d: os needs 6 fields, got %d", m.Provider, i+1, len(f))
			}
			row := OSImageRow{
				CSP:          m.CSP,
				CSPRegion:    strings.TrimSpace(f[1]),
				OSName:       strings.TrimSpace(f[2]),
				OSVersion:    strings.TrimSpace(f[3]),
				Architecture: strings.TrimSpace(f[4]),
				Image:        strings.TrimSpace(f[5]),
			}
			c.osByKey[osKey(row.CSP, row.CSPRegion, row.OSName, row.OSVersion, row.Architecture)] = row
		case "mdb":
			if len(f) != 7 {
				return fmt.Errorf("%s adapter csv line %d: mdb needs 7 fields, got %d", m.Provider, i+1, len(f))
			}
			row := MDBRow{
				Name:      strings.TrimSpace(f[1]),
				Family:    strings.TrimSpace(f[2]),
				CSP:       m.CSP,
				CSPRegion: strings.TrimSpace(f[3]),
				Engine:    strings.TrimSpace(f[4]),
				CPU:       atoiOrZero(f[5]),
				RAM:       atoiOrZero(f[6]),
			}
			c.mdbRows = append(c.mdbRows, row)
			mk := mdbRegionEngineKey(row.CSP, row.CSPRegion, row.Engine)
			c.mdbByRegionEng[mk] = append(c.mdbByRegionEng[mk], row)
		default:
			return fmt.Errorf("%s adapter csv line %d: unknown kind %q", m.Provider, i+1, kind)
		}
	}
	return nil
}

// registerAdapter validates the manifest and folds its CSV snapshot into the
// shared indexes. Called once per provider from NewEmbedded.
func (c *EmbeddedCatalog) registerAdapter(ctx context.Context, m *AdapterManifest, csv string) error {
	if m.Provider == "" || m.CSP == "" || m.TFLocal == "" || m.TFSource == "" {
		return fmt.Errorf("adapter %q: manifest is missing Provider/CSP/TFLocal/TFSource", m.Provider)
	}
	if m.VMTemplate == "" || m.SGTemplate == "" {
		return fmt.Errorf("adapter %q: VMTemplate and SGTemplate are required", m.Provider)
	}
	if _, ok := providerToCSP[m.Provider]; !ok {
		return fmt.Errorf("adapter %q: provider is not registered in providerToCSP", m.Provider)
	}
	if _, ok := cloudProviderSource[m.Provider]; !ok {
		return fmt.Errorf("adapter %q: provider is not registered in cloudProviderSource", m.Provider)
	}
	return c.foldAdapterCatalog(m, csv)
}

// AdapterResolveRegionCheck is a compile-time guard that the adapter path reuses
// the standard RegionCatalog resolution (no separate adapter engine).
func AdapterResolveRegionCheck(ctx context.Context, cat RegionCatalog, region, provider string) error {
	_, err := cat.ResolveRegion(ctx, region, provider)
	return err
}
