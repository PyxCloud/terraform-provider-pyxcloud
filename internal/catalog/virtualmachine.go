package catalog

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Architecture tokens (canonical, provider-neutral) — mirror the wizard shape.
const (
	ArchX8664 = "x86_64"
	ArchARM64 = "arm64"
)

// OS tokens (canonical) — mirror the wizard shape (ubuntu / debian).
const (
	OSUbuntu = "ubuntu"
	OSDebian = "debian"
)

// defaultOSVersions maps a canonical OS to the version we resolve when the user
// does not pin one. These are the LTS / stable defaults present in the catalog
// snapshot (vm_os_catalog.csv). Pinning os_version overrides this.
var defaultOSVersions = map[string]string{
	OSUbuntu: "24.04",
	OSDebian: "12",
}

// VMRow mirrors one row of the backend `virtual_machine` table (columns: name,
// family, csp, region, architecture, cpu, ram, gpu, supports_autoscale).
type VMRow struct {
	Name              string
	Family            string
	CSP               string // catalog csp token: aws | gcp | do
	CSPRegion         string // concrete provider region, e.g. eu-west-1
	Architecture      string // x86_64 | arm64
	CPU               int    // vCPU count
	RAM               int    // GiB
	GPU               string // "0" when none
	SupportsAutoscale bool
}

// OSImageRow mirrors one row of the backend `virtual_machine_operating_system`
// table, reduced to the (csp, csp_region, os, version, arch) -> image id we
// need. `Image` is the concrete provider image reference: an AWS AMI id, a DO
// image slug, or a GCP image (project/family).
type OSImageRow struct {
	CSP          string
	CSPRegion    string
	OSName       string
	OSVersion    string
	Architecture string
	Image        string
}

// VMSpec is the abstract description of a virtual-machine — the canonical
// `virtual-machine { architecture, cpu, ram, os }` with a `count`, placed in a
// network (region+VPC) with a security-group. Provider-neutral.
type VMSpec struct {
	Name         string // VM/component name, e.g. "web"
	Region       string // abstract pyx region_name, e.g. "Dublin"
	Provider     string // provider-facing name: aws | gcp | digitalocean
	Architecture string // x86_64 | arm64
	CPU          int    // requested vCPU
	RAM          int    // requested RAM (GiB)
	OS           string // ubuntu | debian
	OSVersion    string // optional; defaults per defaultOSVersions
	Count        int    // number of instances (>= 1)

	// Placement wiring (from the other components). Names are canonical and
	// resolved to provider references by the renderer.
	Network       string // canonical network/place name (the VPC)
	Subnet        string // canonical subnet name to place instances in
	SecurityGroup string // canonical security-group name to attach
}

// VMInstancePlan is one concrete instance in the translated plan.
type VMInstancePlan struct {
	Name string `json:"name"` // instance logical name, e.g. web-1
}

// VMPlan is the deterministic, catalog-resolved concrete translation of a VMSpec
// for one provider. STRUCTURED plan (not rendered .tf) — the provider owns
// rendering and state, consistent with NetworkPlan / SecurityGroupPlan (§8).
type VMPlan struct {
	Provider      string           `json:"provider"`       // aws | gcp | digitalocean
	CSP           string           `json:"csp"`            // catalog token: aws | gcp | do
	RegionName    string           `json:"region_name"`    // abstract pyx region
	CSPRegion     string           `json:"csp_region"`     // concrete provider region (catalog-resolved)
	VMName        string           `json:"vm_name"`        // logical VM/component name
	InstanceType  string           `json:"instance_type"`  // concrete SKU from `virtual_machine` (e.g. t3.medium)
	Architecture  string           `json:"architecture"`   // resolved architecture
	CPU           int              `json:"cpu"`            // resolved vCPU
	RAM           int              `json:"ram"`            // resolved RAM (GiB)
	OSName        string           `json:"os_name"`        // ubuntu | debian
	OSVersion     string           `json:"os_version"`     // resolved version
	Image         string           `json:"image"`          // concrete provider image (AMI / family / slug)
	Instances     []VMInstancePlan `json:"instances"`      // count instances
	NetworkName   string           `json:"network_name"`   // VPC/network it lives in
	SubnetName    string           `json:"subnet_name"`    // subnet (where applicable)
	SecurityGroup string           `json:"security_group"` // SG/firewall to attach
	ResourceType  string           `json:"resource_type"`  // top provider resource, e.g. aws_instance
}

// VMCatalog is the resolution boundary for virtual-machine SKUs and OS images.
// Both the embedded snapshot and a future live-BE client satisfy it, so the
// provider never embeds instance-type tables of its own.
type VMCatalog interface {
	RegionCatalog
	// ResolveSKU resolves {csp, csp_region, architecture, cpu, ram} into the
	// concrete provider instance `name` from the `virtual_machine` catalog. It
	// returns ErrSKUNotFound (listing nearest sizes) when nothing matches.
	ResolveSKU(ctx context.Context, csp, cspRegion, arch string, cpu, ram int) (VMRow, error)
	// ResolveImage resolves {csp, csp_region, os, version, architecture} into the
	// concrete provider image id from the OS catalog. Returns ErrOSImageNotFound
	// when nothing matches.
	ResolveImage(ctx context.Context, csp, cspRegion, os, version, arch string) (OSImageRow, error)
}

// ErrSKUNotFound is returned when no virtual_machine row matches the request.
// It lists the nearest available sizes in that csp/region to guide the user.
type ErrSKUNotFound struct {
	CSP          string
	CSPRegion    string
	Architecture string
	CPU          int
	RAM          int
	Nearest      []VMRow
}

func (e ErrSKUNotFound) Error() string {
	var sizes []string
	for _, r := range e.Nearest {
		sizes = append(sizes, fmt.Sprintf("%s (%dvCPU/%dGiB %s)", r.Name, r.CPU, r.RAM, r.Architecture))
	}
	nearest := "none in this region/architecture"
	if len(sizes) > 0 {
		nearest = strings.Join(sizes, ", ")
	}
	return fmt.Sprintf(
		"no virtual_machine SKU for csp=%q csp_region=%q architecture=%q cpu=%d ram=%dGiB: "+
			"the PyxCloud virtual_machine catalog has no instance type matching that sizing. "+
			"Nearest available sizes: %s (this is a hard plan-time error, never a silent fallback)",
		e.CSP, e.CSPRegion, e.Architecture, e.CPU, e.RAM, nearest,
	)
}

// ErrOSImageNotFound is returned when no OS catalog row matches the request.
type ErrOSImageNotFound struct {
	CSP          string
	CSPRegion    string
	OSName       string
	OSVersion    string
	Architecture string
}

func (e ErrOSImageNotFound) Error() string {
	return fmt.Sprintf(
		"no OS image for csp=%q csp_region=%q os=%q version=%q architecture=%q: the PyxCloud "+
			"OS catalog has no image for this combination. Pick a supported os/version/architecture "+
			"(this is a hard plan-time error, never a silent fallback)",
		e.CSP, e.CSPRegion, e.OSName, e.OSVersion, e.Architecture,
	)
}

// TranslateVM resolves a VMSpec into a concrete VMPlan using the catalog.
// Deterministic and catalog-driven: the csp_region comes from the region
// catalog, the instance type from the virtual_machine catalog, and the image
// from the OS catalog. Any missing catalog data surfaces as a hard plan-time
// error (never a silent fallback).
func TranslateVM(ctx context.Context, cat VMCatalog, spec VMSpec) (VMPlan, error) {
	if err := validateVMSpec(spec); err != nil {
		return VMPlan{}, err
	}

	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return VMPlan{}, err
	}

	arch := strings.ToLower(strings.TrimSpace(spec.Architecture))
	if arch == "" {
		arch = ArchX8664
	}
	osName := strings.ToLower(strings.TrimSpace(spec.OS))
	if osName == "" {
		osName = OSUbuntu
	}
	osVersion := strings.TrimSpace(spec.OSVersion)
	if osVersion == "" {
		osVersion = defaultOSVersions[osName]
	}

	sku, err := cat.ResolveSKU(ctx, row.CSP, row.CSPRegion, arch, spec.CPU, spec.RAM)
	if err != nil {
		return VMPlan{}, err
	}

	img, err := cat.ResolveImage(ctx, row.CSP, row.CSPRegion, osName, osVersion, arch)
	if err != nil {
		return VMPlan{}, err
	}

	count := spec.Count
	if count <= 0 {
		count = 1
	}
	name := spec.Name
	if name == "" {
		name = "pyxcloud-vm"
	}
	instances := make([]VMInstancePlan, 0, count)
	for i := 0; i < count; i++ {
		instances = append(instances, VMInstancePlan{Name: fmt.Sprintf("%s-%d", name, i+1)})
	}

	plan := VMPlan{
		Provider:      strings.ToLower(spec.Provider),
		CSP:           row.CSP,
		RegionName:    row.RegionName,
		CSPRegion:     row.CSPRegion,
		VMName:        name,
		InstanceType:  sku.Name,
		Architecture:  arch,
		CPU:           sku.CPU,
		RAM:           sku.RAM,
		OSName:        osName,
		OSVersion:     osVersion,
		Image:         img.Image,
		Instances:     instances,
		NetworkName:   spec.Network,
		SubnetName:    spec.Subnet,
		SecurityGroup: spec.SecurityGroup,
	}

	switch plan.Provider {
	case ProviderAWS:
		plan.ResourceType = "aws_instance"
	case ProviderGCP:
		plan.ResourceType = "google_compute_instance"
	case ProviderDigitalOcean:
		plan.ResourceType = "digitalocean_droplet"
	case ProviderAzure:
		plan.ResourceType = "azurerm_linux_virtual_machine"
	case ProviderLinode:
		plan.ResourceType = "linode_instance"
	case ProviderUbicloud:
		plan.ResourceType = "ubicloud_vm"
	case ProviderOracle:
		plan.ResourceType = "oci_core_instance"
	}
	return plan, nil
}

func validateVMSpec(spec VMSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("virtual-machine: region (abstract pyx region_name) is required")
	}
	if strings.TrimSpace(spec.Provider) == "" {
		return fmt.Errorf("virtual-machine: provider is required (aws | gcp | digitalocean)")
	}
	if _, ok := ProviderToCSP(spec.Provider); !ok {
		return fmt.Errorf("virtual-machine: unknown provider %q (aws | gcp | digitalocean)", spec.Provider)
	}
	if arch := strings.ToLower(strings.TrimSpace(spec.Architecture)); arch != "" && arch != ArchX8664 && arch != ArchARM64 {
		return fmt.Errorf("virtual-machine: invalid architecture %q (x86_64 | arm64)", spec.Architecture)
	}
	if os := strings.ToLower(strings.TrimSpace(spec.OS)); os != "" && os != OSUbuntu && os != OSDebian {
		return fmt.Errorf("virtual-machine: invalid os %q (ubuntu | debian)", spec.OS)
	}
	if spec.CPU < 1 {
		return fmt.Errorf("virtual-machine: cpu must be >= 1, got %d", spec.CPU)
	}
	if spec.RAM < 1 {
		return fmt.Errorf("virtual-machine: ram (GiB) must be >= 1, got %d", spec.RAM)
	}
	if spec.Count < 0 {
		return fmt.Errorf("virtual-machine: count must be >= 0 (0 defaults to 1), got %d", spec.Count)
	}
	return nil
}

// nearestSizes returns up to n rows from the candidate set sorted by distance
// from the requested (cpu, ram) — used to populate the no-match error message.
func nearestSizes(candidates []VMRow, cpu, ram, n int) []VMRow {
	type scored struct {
		row  VMRow
		dist int
	}
	scoredRows := make([]scored, 0, len(candidates))
	for _, r := range candidates {
		d := abs(r.CPU-cpu)*4 + abs(r.RAM-ram) // weight cpu mismatch a bit higher
		scoredRows = append(scoredRows, scored{row: r, dist: d})
	}
	sort.SliceStable(scoredRows, func(i, j int) bool {
		if scoredRows[i].dist != scoredRows[j].dist {
			return scoredRows[i].dist < scoredRows[j].dist
		}
		return scoredRows[i].row.Name < scoredRows[j].row.Name
	})
	out := make([]VMRow, 0, n)
	for i := 0; i < len(scoredRows) && i < n; i++ {
		out = append(out, scoredRows[i].row)
	}
	return out
}

// preferredFamilies lists, per catalog family token, the rank used to break a
// tie between instances of identical cpu/ram. Lower rank wins. These are the
// general-purpose / burstable families the wizard defaults to; anything not
// listed ranks after them (and ties break on name). This is a tie-break order,
// not an instance-type map — the candidate set still comes entirely from the
// `virtual_machine` catalog snapshot.
var preferredFamilies = map[string]int{
	"t3":       0, // AWS x86_64 burstable (t3.medium etc.)
	"t4g":      0, // AWS arm64 burstable (Graviton)
	"e2":       0, // GCP general-purpose
	"Droplet":  0, // DigitalOcean standard droplet
	"t3a":      1, // AWS AMD burstable
	"m5":       2, // AWS general-purpose
	"n2":       2, // GCP general-purpose
	"c5":       3, // AWS compute-optimised
	"standard": 0, // Ubicloud standard dedicated-CPU line (wave-2)
}

func familyRank(r VMRow) int {
	if rank, ok := preferredFamilies[r.Family]; ok {
		return rank
	}
	return 100
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func atoiOrZero(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}
