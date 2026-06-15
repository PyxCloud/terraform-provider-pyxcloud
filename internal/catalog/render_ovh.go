package catalog

import (
	"context"
	_ "embed"
	"encoding/csv"
	"fmt"
	"net"
	"sort"
	"strings"
)

// ── wave-2: OVHcloud (pd-TF-PROVIDERS-WAVE2: ovh) ────────────────────────────
//
// This file is the OVHcloud half of the abstract-first, catalog-driven provider,
// mirroring the wave-1 render_<provider> pattern exactly (catalog-resolved
// csp_region/flavor -> structured plan -> render to .tf; plan-time errors, never a
// silent fallback or an invented resource; secure-by-default; the managed-database
// data-safety guard is reused verbatim).
//
// HONEST SCOPE. OVHcloud Public Cloud is OpenStack-based, and many compute
// primitives live in the OpenStack provider, NOT in `ovh/ovh`. We emit ONLY
// resources verified to exist in the ovh/ovh provider, and surface every other
// canonical component as a clean ErrComponentUnsupported naming the alternative:
//
//   SUPPORTED (verified in ovh/ovh):
//     network/vpc          -> ovh_cloud_project_network_private (+ _subnet)
//     managed-database     -> ovh_cloud_project_database        (+ data-safety guard)
//     managed-kubernetes   -> ovh_cloud_project_kube (+ _nodepool)
//     object-storage       -> ovh_cloud_project_storage (S3 high-perf container)
//
//   UNSUPPORTED via ovh/ovh (clean plan-time error, never invented):
//     virtual-machine          ovh_cloud_project_instance exists but is UUID-driven
//                              (flavor_id/image_id), not catalog-name-driven; it does
//                              not fit the abstract cpu/ram/os contract without an
//                              OpenStack image/flavor census we do not yet have.
//     virtual-machine-scale-group   no native VM ASG primitive in ovh/ovh.
//     security-group           no Public Cloud SG resource in ovh/ovh (OpenStack
//                              neutron security groups live in the OpenStack provider).
//     load-balancer            ovh_iploadbalancing is a separate order/cart billing
//                              product (not a Public Cloud place-scoped LB); does not
//                              fit the place/subnet model.
//     cache, managed-queue, event-streaming, dns-zone, cdn-service, waf-service,
//     secrets-manager, serverless-function  -> no clean ovh/ovh primitive.
//
// All OVH Public Cloud resources are scoped to a Public Cloud PROJECT; its id is
// provided out-of-band via the `ovh_service_name` Terraform variable (the same
// out-of-band-credential pattern wave-1 uses for IAM role ARNs / DB passwords), so
// the project id never lands in the canonical topology or in state plaintext.

// ProviderOVH is the wave-2 OVHcloud provider-facing name (Terraform `provider`).
const ProviderOVH = "ovh"

// cspOVH is the catalog csp token for OVHcloud (mirrors cspAWS/cspGCP/cspDO).
const cspOVH = "ovh"

// ovhCatalogCSV is the embedded OVHcloud catalog snapshot (region + flavor rows).
// See ovh_catalog.csv for the provenance / catalog-gap note: the live ETL does not
// yet census OVH, so these rows stand in for that catalog slice with the SAME flat
// shape, to be replaced wholesale when the OVH ETL lands (no code change).
//
//go:embed ovh_catalog.csv
var ovhCatalogCSV string

// ovhFlavorRow is one DB / kube node flavor from the OVH catalog snapshot.
type ovhFlavorRow struct {
	Kind   string // "db" | "node"
	Flavor string // concrete OVH flavor name, e.g. db1-7 / b3-8
	CPU    int
	RAM    int
	Plan   string // OVH cluster plan (db only): essential | business | enterprise
}

// OVHCatalog resolves OVH abstract regions and DB/kube flavors against the embedded
// snapshot. It is the OVH analogue of EmbeddedCatalog: the provider never embeds an
// OVH region/flavor map in code — resolution is the catalog itself.
type OVHCatalog struct {
	regionByName map[string]RegionRow // keyed by lowercased region_name
	regions      []RegionRow
	dbFlavors    []ovhFlavorRow
	nodeFlavors  []ovhFlavorRow
}

// NewOVHCatalog parses the embedded OVH snapshot.
func NewOVHCatalog() (*OVHCatalog, error) {
	c := &OVHCatalog{regionByName: map[string]RegionRow{}}
	r := csv.NewReader(strings.NewReader(ovhCatalogCSV))
	r.Comment = '#'
	r.FieldsPerRecord = 6
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse ovh catalog: %w", err)
	}
	for i, rec := range records {
		if i == 0 {
			continue // header (kind,a,b,c,d,e)
		}
		switch rec[0] {
		case "region":
			row := RegionRow{
				MacroRegion:          rec[1],
				Country:              rec[2],
				RegionName:           rec[3],
				CSPRegion:            rec[4],
				CSPRegionDescription: rec[5],
				CSP:                  cspOVH,
			}
			k := strings.ToLower(strings.TrimSpace(row.RegionName))
			if _, exists := c.regionByName[k]; !exists {
				c.regionByName[k] = row
			}
			c.regions = append(c.regions, row)
		case "flavor":
			f := ovhFlavorRow{
				Kind:   rec[1],
				Flavor: rec[2],
				CPU:    atoiOrZero(rec[3]),
				RAM:    atoiOrZero(rec[4]),
				Plan:   rec[5],
			}
			if f.Kind == "db" {
				c.dbFlavors = append(c.dbFlavors, f)
			} else {
				c.nodeFlavors = append(c.nodeFlavors, f)
			}
		default:
			return nil, fmt.Errorf("parse ovh catalog: unknown record kind %q", rec[0])
		}
	}
	if len(c.regions) == 0 {
		return nil, fmt.Errorf("parse ovh catalog: no region rows")
	}
	return c, nil
}

// MustOVHCatalog is NewOVHCatalog that panics on a malformed embedded snapshot
// (a build-time invariant, mirroring MustEmbedded).
func MustOVHCatalog() *OVHCatalog {
	c, err := NewOVHCatalog()
	if err != nil {
		panic(fmt.Sprintf("catalog: embedded ovh snapshot is invalid: %v", err))
	}
	return c
}

// Regions returns all OVH region rows (test/debug helper).
func (c *OVHCatalog) Regions() []RegionRow { return c.regions }

// ResolveRegion resolves an abstract pyx region_name into the OVH csp_region.
// Mirrors EmbeddedCatalog.ResolveRegion: a miss is a hard error, never a fallback.
func (c *OVHCatalog) ResolveRegion(regionName string) (RegionRow, error) {
	row, ok := c.regionByName[strings.ToLower(strings.TrimSpace(regionName))]
	if !ok {
		return RegionRow{}, ErrRegionNotFound{RegionName: regionName, Provider: ProviderOVH}
	}
	return row, nil
}

// ResolveDBFlavor resolves a requested (cpu, ram) into a concrete OVH DB flavor +
// plan from the catalog. Exact (cpu, ram) match wins; no match is a hard error
// listing the nearest available sizes (never a silent fallback to a different size),
// mirroring EmbeddedCatalog.ResolveDBClass.
func (c *OVHCatalog) ResolveDBFlavor(cpu, ram int) (ovhFlavorRow, error) {
	var exact []ovhFlavorRow
	for _, f := range c.dbFlavors {
		if f.CPU == cpu && f.RAM == ram {
			exact = append(exact, f)
		}
	}
	if len(exact) > 0 {
		best := exact[0]
		for _, f := range exact[1:] {
			if f.Flavor < best.Flavor {
				best = f
			}
		}
		return best, nil
	}
	return ovhFlavorRow{}, ovhFlavorNotFound("managed-database", c.dbFlavors, cpu, ram)
}

// ResolveNodeFlavor resolves a requested (cpu, ram) into a concrete OVH kube node
// flavor from the catalog (same exact-match-or-hard-error contract).
func (c *OVHCatalog) ResolveNodeFlavor(cpu, ram int) (ovhFlavorRow, error) {
	var exact []ovhFlavorRow
	for _, f := range c.nodeFlavors {
		if f.CPU == cpu && f.RAM == ram {
			exact = append(exact, f)
		}
	}
	if len(exact) > 0 {
		best := exact[0]
		for _, f := range exact[1:] {
			if f.Flavor < best.Flavor {
				best = f
			}
		}
		return best, nil
	}
	return ovhFlavorRow{}, ovhFlavorNotFound("managed-kubernetes", c.nodeFlavors, cpu, ram)
}

func ovhFlavorNotFound(component string, candidates []ovhFlavorRow, cpu, ram int) error {
	type scored struct {
		f ovhFlavorRow
		d int
	}
	s := make([]scored, 0, len(candidates))
	for _, f := range candidates {
		s = append(s, scored{f, abs(f.CPU-cpu)*4 + abs(f.RAM-ram)})
	}
	sort.SliceStable(s, func(i, j int) bool {
		if s[i].d != s[j].d {
			return s[i].d < s[j].d
		}
		return s[i].f.Flavor < s[j].f.Flavor
	})
	var sizes []string
	for i := 0; i < len(s) && i < 5; i++ {
		sizes = append(sizes, fmt.Sprintf("%s (%dvCPU/%dGiB)", s[i].f.Flavor, s[i].f.CPU, s[i].f.RAM))
	}
	nearest := "none in the OVH catalog"
	if len(sizes) > 0 {
		nearest = strings.Join(sizes, ", ")
	}
	return fmt.Errorf(
		"%s: no OVH flavor for cpu=%d ram=%dGiB: the OVH catalog snapshot has no flavor "+
			"matching that sizing. Nearest available: %s (hard plan-time error, never a "+
			"silent fallback)", component, cpu, ram, nearest)
}

// ovhKubeRegion derives the regional variant OVH managed-kubernetes expects (e.g.
// GRA -> GRA11) from a macro datacenter code. Managed-database/object-storage take
// the macro code directly; only kube needs the regional variant. Deterministic.
func ovhKubeRegion(cspRegion string) string {
	regional := map[string]string{
		"GRA": "GRA11", "SBG": "SBG5", "DE": "DE1", "UK": "UK1",
		"WAW": "WAW1", "BHS": "BHS5", "SGP": "SGP1", "SYD": "SYD1",
		"US-EAST-VA": "US-EAST-VA-1", "US-WEST-OR": "US-WEST-OR-1",
	}
	if r, ok := regional[strings.ToUpper(cspRegion)]; ok {
		return r
	}
	return cspRegion
}

// RenderOVHComponent is the single OVH entry point the shared dispatch calls for a
// canonical component placed on the OVH provider. It returns rendered .tf for a
// supported component, or a clean ErrComponentUnsupported (verified, never invented)
// for one OVH has no clean ovh/ovh primitive for. `spec` is the component-typed spec
// struct (NetworkSpec / ManagedDatabaseSpec / K8sSpec / ObjectStorageSpec); the
// `component` is the canonical type token (SPEC §3.1).
func RenderOVHComponent(ctx context.Context, cat *OVHCatalog, component string, spec interface{}) (string, error) {
	switch lc(component) {
	case "network", "vpc":
		s, ok := spec.(NetworkSpec)
		if !ok {
			return "", fmt.Errorf("ovh: network expects a NetworkSpec, got %T", spec)
		}
		plan, err := TranslateOVHNetwork(cat, s)
		if err != nil {
			return "", err
		}
		return RenderOVHNetworkHCL(plan), nil
	case "managed-database":
		s, ok := spec.(ManagedDatabaseSpec)
		if !ok {
			return "", fmt.Errorf("ovh: managed-database expects a ManagedDatabaseSpec, got %T", spec)
		}
		plan, err := TranslateOVHManagedDatabase(cat, s)
		if err != nil {
			return "", err
		}
		return RenderOVHManagedDatabaseHCL(plan), nil
	case TypeManagedKubernetes:
		s, ok := spec.(K8sSpec)
		if !ok {
			return "", fmt.Errorf("ovh: managed-kubernetes expects a K8sSpec, got %T", spec)
		}
		plan, err := TranslateOVHKubernetes(cat, s)
		if err != nil {
			return "", err
		}
		return RenderOVHKubernetesHCL(plan), nil
	case "object-storage", "blob-storage":
		s, ok := spec.(ObjectStorageSpec)
		if !ok {
			return "", fmt.Errorf("ovh: object-storage expects an ObjectStorageSpec, got %T", spec)
		}
		plan, err := TranslateOVHObjectStorage(cat, s)
		if err != nil {
			return "", err
		}
		return RenderOVHObjectStorageHCL(plan), nil
	default:
		return "", ovhUnsupported(component)
	}
}

// ovhUnsupported returns the clean plan-time error for a canonical component the
// ovh/ovh provider has no primitive for, naming the verified alternative. NEVER an
// invented resource (SPEC §1). The region is unresolved here (the spec is rejected
// before resolution), consistent with ErrComponentUnsupported.
func ovhUnsupported(component string) error {
	alt := map[string]string{
		"virtual-machine": "the ovh/ovh ovh_cloud_project_instance resource exists but is " +
			"UUID-driven (flavor_id/image_id), not catalog-name-driven, so it cannot satisfy " +
			"the abstract cpu/ram/os contract without an OpenStack flavor/image census PyxCloud " +
			"does not yet have. Use managed-kubernetes (ovh_cloud_project_kube) for compute, or " +
			"declare the instance directly via the OpenStack provider",
		"virtual-machine-scale-group": "OVH Public Cloud has no native VM autoscaling primitive " +
			"in ovh/ovh. Use managed-kubernetes (ovh_cloud_project_kube_nodepool autoscaling)",
		"security-group": "ovh/ovh has no Public Cloud security-group resource; OpenStack neutron " +
			"security groups live in the OpenStack provider, not ovh/ovh",
		"load-balancer": "ovh_iploadbalancing is a separate order/cart billing product, not a " +
			"Public-Cloud place-scoped load balancer, so it does not fit the abstract " +
			"place/subnet model. Use a managed-kubernetes LoadBalancer Service, or provision " +
			"ovh_iploadbalancing manually out of band",
		"cache":               "ovh/ovh has no managed in-memory cache (Redis) Public Cloud resource",
		"managed-queue":       "ovh/ovh has no managed message-queue resource",
		"message-queue":       "ovh/ovh has no managed message-queue resource",
		"event-streaming":     "ovh/ovh has no managed event-streaming resource (Kafka is offered via ovh_cloud_project_database engine=kafka, not a clean stream primitive)",
		"event-bus":           "ovh/ovh has no managed event-bus resource",
		"dns-zone":            "ovh/ovh manages DNS only for OVH-registered domains (ovh_domain_zone_record on an existing ovh_domain_zone), not as a standalone managed zone for an arbitrary domain",
		"cdn-service":         "ovh/ovh has no Public Cloud CDN resource",
		"waf-service":         "ovh/ovh has no WAF resource",
		"secrets-manager":     "ovh/ovh has no managed secrets-manager resource",
		"serverless-function": "ovh/ovh has no serverless-function resource",
	}
	a, ok := alt[lc(component)]
	if !ok {
		a = "the ovh/ovh provider has no resource for this component"
	}
	return ErrComponentUnsupported{
		Component:   lc(component),
		Provider:    ProviderOVH,
		CSP:         cspOVH,
		Alternative: a,
	}
}

// ── network (ovh_cloud_project_network_private + _subnet) ─────────────────────

// OVHNetworkPlan is the catalog-resolved concrete translation of a NetworkSpec for
// OVH. STRUCTURED plan (the provider owns rendering/state), mirroring NetworkPlan.
type OVHNetworkPlan struct {
	RegionName string          `json:"region_name"`
	CSPRegion  string          `json:"csp_region"`
	VPCName    string          `json:"vpc_name"`
	Subnets    []OVHSubnetPlan `json:"subnets"`
}

// OVHSubnetPlan is one concrete OVH subnet. OVH subnets are expressed as a
// network CIDR + an allocation range (start/end), NOT an AWS-style AZ/CIDR pair.
type OVHSubnetPlan struct {
	Name    string `json:"name"`
	Network string `json:"network"` // CIDR, e.g. 10.0.1.0/24
	Start   string `json:"start"`   // first allocatable IP
	End     string `json:"end"`     // last allocatable IP
}

// TranslateOVHNetwork resolves a NetworkSpec into an OVHNetworkPlan. Reuses the
// shared validateSpec contract for CIDR sanity, but resolves the region against the
// OVH catalog (not the wave-1 ProviderToCSP path). Deterministic, catalog-driven.
func TranslateOVHNetwork(cat *OVHCatalog, spec NetworkSpec) (OVHNetworkPlan, error) {
	if strings.TrimSpace(spec.Region) == "" {
		return OVHNetworkPlan{}, fmt.Errorf("ovh network: region (abstract pyx region_name) is required")
	}
	if strings.TrimSpace(spec.CIDR) == "" {
		return OVHNetworkPlan{}, fmt.Errorf("ovh network: cidr is required, e.g. 10.0.0.0/16")
	}
	row, err := cat.ResolveRegion(spec.Region)
	if err != nil {
		return OVHNetworkPlan{}, err
	}
	name := spec.Name
	if name == "" {
		name = "pyxcloud"
	}
	plan := OVHNetworkPlan{RegionName: row.RegionName, CSPRegion: row.CSPRegion, VPCName: name}
	for i, cidr := range spec.Subnets {
		start, end, err := ovhSubnetRange(cidr)
		if err != nil {
			return OVHNetworkPlan{}, fmt.Errorf("ovh network: %w", err)
		}
		plan.Subnets = append(plan.Subnets, OVHSubnetPlan{
			Name:    fmt.Sprintf("%s-subnet-%d", name, i+1),
			Network: cidr,
			Start:   start,
			End:     end,
		})
	}
	return plan, nil
}

// RenderOVHNetworkHCL renders an OVHNetworkPlan into ovh_cloud_project_network_private
// + one ovh_cloud_project_network_private_subnet per subnet. The Public Cloud
// project id is provided out of band via var.ovh_service_name.
func RenderOVHNetworkHCL(p OVHNetworkPlan) string {
	name := tfName(p.VPCName)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"ovh_cloud_project_network_private\" %q {\n", name)
	b.WriteString("  service_name = var.ovh_service_name\n")
	fmt.Fprintf(&b, "  name         = %q\n", tfName(p.VPCName))
	fmt.Fprintf(&b, "  regions      = [%q]\n", p.CSPRegion)
	b.WriteString("}\n")
	for i, s := range p.Subnets {
		sn := fmt.Sprintf("%s_%d", name, i+1)
		fmt.Fprintf(&b, "\nresource \"ovh_cloud_project_network_private_subnet\" %q {\n", sn)
		b.WriteString("  service_name = var.ovh_service_name\n")
		fmt.Fprintf(&b, "  network_id   = ovh_cloud_project_network_private.%s.id\n", name)
		fmt.Fprintf(&b, "  region       = %q\n", p.CSPRegion)
		fmt.Fprintf(&b, "  network      = %q\n", s.Network)
		fmt.Fprintf(&b, "  start        = %q\n", s.Start)
		fmt.Fprintf(&b, "  end          = %q\n", s.End)
		// SECURE BY DEFAULT: no internet gateway on the private subnet (no_gateway),
		// DHCP on for in-VPC addressing. Egress is opt-in via a sibling gateway.
		b.WriteString("  dhcp         = true\n")
		b.WriteString("  no_gateway   = true\n")
		b.WriteString("}\n")
	}
	return b.String()
}

// ── managed-database (ovh_cloud_project_database) ─────────────────────────────

// OVHManagedDatabasePlan is the catalog-resolved concrete translation for OVH.
// It carries the same canonical fields as ManagedDatabasePlan so the SHARED
// data-safety guard (CheckManagedDatabaseDataSafety) can be reused via ToCommon().
type OVHManagedDatabasePlan struct {
	RegionName string `json:"region_name"`
	CSPRegion  string `json:"csp_region"`
	DBName     string `json:"db_name"`

	Engine        string `json:"engine"`     // postgres | mysql (canonical)
	OVHEngine     string `json:"ovh_engine"` // postgresql | mysql (OVH token)
	EngineVersion string `json:"engine_version"`
	Flavor        string `json:"flavor"` // concrete OVH db flavor, e.g. db1-7
	Plan          string `json:"plan"`   // OVH cluster plan: essential | business
	CPU           int    `json:"cpu"`
	RAM           int    `json:"ram"`
	DiskGB        int    `json:"disk_gb"`
	NodeCount     int    `json:"node_count"` // 1 normally, >=2 for HA
	HA            bool   `json:"ha"`

	DeletionProtection bool   `json:"deletion_protection"`
	NetworkName        string `json:"network_name"`
}

// ToCommon projects the OVH DB plan onto the shared ManagedDatabasePlan so the
// SAME data-safety guard the wave-1 providers use (CheckManagedDatabaseDataSafety)
// guards OVH too. OVH ovh_cloud_project_database forces a new resource on engine
// change (and a flavor family change is a different node lineage), exactly the
// replacement-forcing class the guard blocks. `Family` carries the flavor prefix
// (db1/db2) so a cross-family change trips the guard while a same-family resize does
// not. OVH clusters are encrypted at rest by default (no toggle), so Encrypted is
// constant true and never trips the guard.
func (p OVHManagedDatabasePlan) ToCommon() ManagedDatabasePlan {
	return ManagedDatabasePlan{
		Provider:           ProviderOVH,
		CSP:                cspOVH,
		RegionName:         p.RegionName,
		CSPRegion:          p.CSPRegion,
		DBName:             p.DBName,
		Engine:             p.Engine,
		EngineVersion:      p.EngineVersion,
		DBClass:            p.Flavor,
		Family:             ovhFlavorFamily(p.Flavor),
		CPU:                p.CPU,
		RAM:                p.RAM,
		StorageGB:          p.DiskGB,
		HA:                 p.HA,
		Encrypted:          true, // OVH managed DB is encrypted at rest by default
		DeletionProtection: p.DeletionProtection,
		NetworkName:        p.NetworkName,
	}
}

// ovhFlavorFamily extracts the OVH flavor family prefix (e.g. "db1-7" -> "db1"),
// the storage/class lineage the data-safety guard compares.
func ovhFlavorFamily(flavor string) string {
	if i := strings.IndexByte(flavor, '-'); i > 0 {
		return flavor[:i]
	}
	return flavor
}

// ovhDBEngine maps the canonical engine to the OVH ovh_cloud_project_database
// `engine` token. OVH uses "postgresql" (not "postgres") and "mysql".
func ovhDBEngine(engine string) string {
	if engine == DBEngineMySQL {
		return "mysql"
	}
	return "postgresql"
}

// TranslateOVHManagedDatabase resolves a ManagedDatabaseSpec into an
// OVHManagedDatabasePlan. Deterministic, catalog-driven: region from the OVH region
// catalog, flavor+plan from the OVH flavor catalog (hard error on no match). HA maps
// to a 3-node cluster (OVH DB HA shape), single node otherwise.
func TranslateOVHManagedDatabase(cat *OVHCatalog, spec ManagedDatabaseSpec) (OVHManagedDatabasePlan, error) {
	if strings.TrimSpace(spec.Region) == "" {
		return OVHManagedDatabasePlan{}, fmt.Errorf("ovh managed-database: region (abstract pyx region_name) is required")
	}
	if spec.CPU < 1 || spec.RAM < 1 {
		return OVHManagedDatabasePlan{}, fmt.Errorf("ovh managed-database: cpu and ram (GiB) must each be >= 1, got cpu=%d ram=%d", spec.CPU, spec.RAM)
	}
	if e := lc(spec.Engine); e != "" {
		switch e {
		case DBEnginePostgres, "postgresql", "pg", DBEngineMySQL, "mariadb":
		default:
			return OVHManagedDatabasePlan{}, fmt.Errorf("ovh managed-database: invalid engine %q (postgres | mysql)", spec.Engine)
		}
	}
	row, err := cat.ResolveRegion(spec.Region)
	if err != nil {
		return OVHManagedDatabasePlan{}, err
	}
	flavor, err := cat.ResolveDBFlavor(spec.CPU, spec.RAM)
	if err != nil {
		return OVHManagedDatabasePlan{}, err
	}
	engine := canonicalEngine(spec.Engine)
	version := strings.TrimSpace(spec.Version)
	if version == "" {
		version = defaultEngineVersions[engine]
	}
	name := spec.Name
	if name == "" {
		name = "pyxcloud-db"
	}
	disk := spec.StorageGB
	if disk < MinStorageGB {
		disk = MinStorageGB
	}
	deletionProtection := true
	if spec.DeletionProtection != nil {
		deletionProtection = *spec.DeletionProtection
	}
	nodeCount := 1
	if spec.HA {
		nodeCount = 3 // OVH managed-DB HA = a 3-node cluster
	}
	return OVHManagedDatabasePlan{
		RegionName:         row.RegionName,
		CSPRegion:          row.CSPRegion,
		DBName:             name,
		Engine:             engine,
		OVHEngine:          ovhDBEngine(engine),
		EngineVersion:      version,
		Flavor:             flavor.Flavor,
		Plan:               flavor.Plan,
		CPU:                flavor.CPU,
		RAM:                flavor.RAM,
		DiskGB:             disk,
		NodeCount:          nodeCount,
		HA:                 spec.HA,
		DeletionProtection: deletionProtection,
		NetworkName:        spec.Network,
	}, nil
}

// RenderOVHManagedDatabaseHCL renders an OVHManagedDatabasePlan into
// ovh_cloud_project_database. node count = repeated nodes {} blocks (OVH's model),
// each pinned to the csp_region. deletion_protection is carried as a lifecycle
// prevent_destroy (OVH has no in-place deletion-protection flag, mirroring DO).
func RenderOVHManagedDatabaseHCL(p OVHManagedDatabasePlan) string {
	name := tfName(p.DBName)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"ovh_cloud_project_database\" %q {\n", name)
	b.WriteString("  service_name = var.ovh_service_name\n")
	fmt.Fprintf(&b, "  description  = %q\n", p.DBName)
	fmt.Fprintf(&b, "  engine       = %q\n", p.OVHEngine)
	fmt.Fprintf(&b, "  version      = %q\n", p.EngineVersion)
	fmt.Fprintf(&b, "  plan         = %q\n", p.Plan)
	fmt.Fprintf(&b, "  flavor       = %q\n", p.Flavor)
	fmt.Fprintf(&b, "  disk_size    = %d\n", p.DiskGB)
	for i := 0; i < p.NodeCount; i++ {
		b.WriteString("\n  nodes {\n")
		if p.NetworkName != "" {
			fmt.Fprintf(&b, "    region     = %q\n", p.CSPRegion)
			// Private placement: the node joins the place's private network. The
			// network/subnet OpenStack ids come from the network component's outputs.
			fmt.Fprintf(&b, "    network_id = ovh_cloud_project_network_private.%s.regions_attributes[0].openstackid\n", tfName(p.NetworkName))
		} else {
			fmt.Fprintf(&b, "    region = %q\n", p.CSPRegion)
		}
		b.WriteString("  }\n")
	}
	// OVH has no in-place deletion-protection flag; carry the production intent as a
	// lifecycle prevent_destroy when deletion_protection is on (mirrors the DO path).
	if p.DeletionProtection {
		b.WriteString("\n  lifecycle {\n")
		b.WriteString("    prevent_destroy = true\n")
		b.WriteString("  }\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// ── managed-kubernetes (ovh_cloud_project_kube + _nodepool) ───────────────────

// OVHK8sPlan is the catalog-resolved concrete translation for OVH managed-kubernetes.
type OVHK8sPlan struct {
	RegionName   string `json:"region_name"`
	CSPRegion    string `json:"csp_region"` // kube regional variant, e.g. GRA11
	Name         string `json:"name"`
	Version      string `json:"version"`
	NodeFlavor   string `json:"node_flavor"` // concrete OVH node flavor, e.g. b3-8
	NodeCPU      int    `json:"node_cpu"`
	NodeRAM      int    `json:"node_ram"`
	MinNodes     int    `json:"min_nodes"`
	MaxNodes     int    `json:"max_nodes"`
	DesiredNodes int    `json:"desired_nodes"`
	NetworkName  string `json:"network_name"`
}

// TranslateOVHKubernetes resolves a K8sSpec into an OVHK8sPlan. Node flavor resolved
// from the OVH catalog by (cpu, ram); region resolved to the kube regional variant.
func TranslateOVHKubernetes(cat *OVHCatalog, spec K8sSpec) (OVHK8sPlan, error) {
	if strings.TrimSpace(spec.Region) == "" {
		return OVHK8sPlan{}, fmt.Errorf("ovh managed-kubernetes: region (abstract pyx region_name) is required")
	}
	if spec.NodeCPU < 1 || spec.NodeRAM < 1 {
		return OVHK8sPlan{}, fmt.Errorf("ovh managed-kubernetes: node cpu and ram (GiB) must each be >= 1, got cpu=%d ram=%d", spec.NodeCPU, spec.NodeRAM)
	}
	row, err := cat.ResolveRegion(spec.Region)
	if err != nil {
		return OVHK8sPlan{}, err
	}
	flavor, err := cat.ResolveNodeFlavor(spec.NodeCPU, spec.NodeRAM)
	if err != nil {
		return OVHK8sPlan{}, err
	}
	name := spec.Name
	if name == "" {
		name = "pyxcloud-kube"
	}
	minN, maxN, desN := spec.MinNodes, spec.MaxNodes, spec.DesiredNodes
	if maxN < 1 {
		maxN = 1
	}
	if minN < 0 {
		minN = 0
	}
	if desN < minN {
		desN = minN
	}
	if desN < 1 {
		desN = 1
	}
	if maxN < desN {
		maxN = desN
	}
	return OVHK8sPlan{
		RegionName:   row.RegionName,
		CSPRegion:    ovhKubeRegion(row.CSPRegion),
		Name:         name,
		Version:      strings.TrimSpace(spec.Version),
		NodeFlavor:   flavor.Flavor,
		NodeCPU:      flavor.CPU,
		NodeRAM:      flavor.RAM,
		MinNodes:     minN,
		MaxNodes:     maxN,
		DesiredNodes: desN,
		NetworkName:  spec.Network,
	}, nil
}

// RenderOVHKubernetesHCL renders an OVHK8sPlan into ovh_cloud_project_kube +
// ovh_cloud_project_kube_nodepool (autoscaling). The nodepool name must not contain
// "_" (OVH constraint), so we use the hyphenated logical name. Private placement
// wires private_network_id from the network component when present.
func RenderOVHKubernetesHCL(p OVHK8sPlan) string {
	name := tfName(p.Name)
	poolName := sanitiseBucketPrefix(p.Name) // [a-z0-9-], no "_" (OVH nodepool rule)
	if poolName == "" {
		poolName = "pyxcloud-pool"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"ovh_cloud_project_kube\" %q {\n", name)
	b.WriteString("  service_name = var.ovh_service_name\n")
	fmt.Fprintf(&b, "  name         = %q\n", tfName(p.Name))
	fmt.Fprintf(&b, "  region       = %q\n", p.CSPRegion)
	if p.Version != "" {
		fmt.Fprintf(&b, "  version      = %q\n", p.Version)
	}
	if p.NetworkName != "" {
		// SECURE BY DEFAULT: nodes join the place's private OpenStack network.
		fmt.Fprintf(&b, "  private_network_id = ovh_cloud_project_network_private.%s.regions_attributes[0].openstackid\n", tfName(p.NetworkName))
	}
	b.WriteString("}\n\n")

	fmt.Fprintf(&b, "resource \"ovh_cloud_project_kube_nodepool\" %q {\n", name+"_np")
	b.WriteString("  service_name  = var.ovh_service_name\n")
	fmt.Fprintf(&b, "  kube_id       = ovh_cloud_project_kube.%s.id\n", name)
	fmt.Fprintf(&b, "  name          = \"%s-np\"\n", poolName)
	fmt.Fprintf(&b, "  flavor_name   = %q\n", p.NodeFlavor)
	// Autoscaling node pool (the OVH analogue of the EKS/GKE/DOKS autoscaler).
	b.WriteString("  autoscale     = true\n")
	fmt.Fprintf(&b, "  desired_nodes = %d\n", p.DesiredNodes)
	fmt.Fprintf(&b, "  min_nodes     = %d\n", p.MinNodes)
	fmt.Fprintf(&b, "  max_nodes     = %d\n", p.MaxNodes)
	b.WriteString("}\n")
	return b.String()
}

// ── object-storage (ovh_cloud_project_storage) ───────────────────────────────

// OVHObjectStoragePlan is the catalog-resolved concrete translation for OVH.
type OVHObjectStoragePlan struct {
	RegionName  string `json:"region_name"`
	CSPRegion   string `json:"csp_region"`
	LogicalName string `json:"logical_name"`
	BucketName  string `json:"bucket_name"`
	Versioning  bool   `json:"versioning"`
	Public      bool   `json:"public"`
}

// TranslateOVHObjectStorage resolves an ObjectStorageSpec into an
// OVHObjectStoragePlan. ovh_cloud_project_storage is the S3-compatible high-perf
// container; region is the macro datacenter code (GRA/DE/...).
func TranslateOVHObjectStorage(cat *OVHCatalog, spec ObjectStorageSpec) (OVHObjectStoragePlan, error) {
	if strings.TrimSpace(spec.Region) == "" {
		return OVHObjectStoragePlan{}, fmt.Errorf("ovh object-storage: region (abstract pyx region_name) is required")
	}
	if strings.TrimSpace(spec.Name) == "" {
		return OVHObjectStoragePlan{}, fmt.Errorf("ovh object-storage: name is required")
	}
	row, err := cat.ResolveRegion(spec.Region)
	if err != nil {
		return OVHObjectStoragePlan{}, err
	}
	bucket := sanitiseBucketPrefix(spec.Name)
	if bucket == "" {
		bucket = "pyxcloud-bucket"
	}
	return OVHObjectStoragePlan{
		RegionName:  row.RegionName,
		CSPRegion:   row.CSPRegion,
		LogicalName: spec.Name,
		BucketName:  bucket,
		Versioning:  spec.Versioning,
		Public:      spec.Public,
	}, nil
}

// RenderOVHObjectStorageHCL renders an OVHObjectStoragePlan into
// ovh_cloud_project_storage. PRIVATE BY DEFAULT: OVH storage containers are private
// (no public ACL block is emitted; public exposure on OVH is granted out of band via
// an S3 bucket policy, opt-in only). Versioning is the versioning { status } block.
func RenderOVHObjectStorageHCL(p OVHObjectStoragePlan) string {
	label := tfName(p.LogicalName)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"ovh_cloud_project_storage\" %q {\n", label)
	b.WriteString("  service_name = var.ovh_service_name\n")
	fmt.Fprintf(&b, "  region_name  = %q\n", p.CSPRegion)
	fmt.Fprintf(&b, "  name         = %q\n", p.BucketName)
	status := "disabled"
	if p.Versioning {
		status = "enabled"
	}
	b.WriteString("  versioning = {\n")
	fmt.Fprintf(&b, "    status = %q\n", status)
	b.WriteString("  }\n")
	// SECURE BY DEFAULT: encryption at rest (SSE) on the container. OVH containers
	// are private; public-read is NOT emitted here (opt-in via an out-of-band policy).
	b.WriteString("  encryption = {\n")
	b.WriteString("    sse_algorithm = \"AES256\"\n")
	b.WriteString("  }\n")
	b.WriteString("}\n")
	return b.String()
}

// ── helpers ───────────────────────────────────────────────────────────────────

// ovhSubnetRange derives the first/last allocatable host IP of a subnet CIDR — the
// start/end OVH's ovh_cloud_project_network_private_subnet expects (it takes a
// network CIDR plus an allocation range, not a bare CIDR). Deterministic: start =
// network+2 (skip network addr + the gateway OVH reserves at .1), end = broadcast-1.
func ovhSubnetRange(cidr string) (start, end string, err error) {
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", "", fmt.Errorf("invalid subnet cidr %q: %w", cidr, err)
	}
	ip := n.IP.To4()
	if ip == nil {
		return "", "", fmt.Errorf("subnet cidr %q is not IPv4 (OVH private subnets are IPv4)", cidr)
	}
	mask := n.Mask
	// network base and broadcast (last) address.
	base := make([]byte, 4)
	last := make([]byte, 4)
	for i := 0; i < 4; i++ {
		base[i] = ip[i] & mask[i]
		last[i] = base[i] | ^mask[i]
	}
	startIP := addToIPv4(base, 2) // skip network (.0) and gateway (.1)
	endIP := addToIPv4(last, -1)  // skip broadcast
	return startIP, endIP, nil
}

func addToIPv4(b []byte, delta int) string {
	v := int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3])
	v += delta
	return fmt.Sprintf("%d.%d.%d.%d", byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}
