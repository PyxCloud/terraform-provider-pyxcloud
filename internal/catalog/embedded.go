package catalog

import (
	"context"
	_ "embed"
	"encoding/csv"
	"fmt"
	"strings"
)

// regionCatalogCSV is a snapshot of the backend `region` table (wave-1
// providers: aws, gcp, do). It is the SAME flat join the wizard and the
// console Compare page resolve against. Embedding it keeps region resolution
// catalog-driven and deterministic without a network round-trip; it is NOT a
// hand-authored region map.
//
//go:embed region_catalog.csv
var regionCatalogCSV string

// vmCatalogCSV is a snapshot of the backend `virtual_machine` table (wave-1
// providers: aws, gcp, do; the test regions present in the live ETL). It is the
// SAME table the wizard and Compare page resolve instance types against.
// Embedding it keeps SKU resolution catalog-driven and deterministic without a
// network round-trip; it is NOT a hand-authored instance-type map.
//
//go:embed vm_catalog.csv
var vmCatalogCSV string

// vmOSCatalogCSV is a snapshot of the backend `virtual_machine_operating_system`
// table reduced to (csp, csp_region, os, version, arch) -> concrete image
// reference. AWS rows carry real AMI ids and DO rows carry real image slugs
// (sourced from the live ETL); GCP ubuntu has no usable id in the catalog
// (csp_os_name empty / a pinned URL), so its rows carry the canonical, stable
// GCP image-family form (project/family) per SPEC 5.3 ("GCP image family").
//
//go:embed vm_os_catalog.csv
var vmOSCatalogCSV string

// EmbeddedCatalog resolves regions, virtual_machine SKUs, and OS images against
// the embedded snapshots.
type EmbeddedCatalog struct {
	// rows keyed by (csp, lowercased region_name) for O(1) resolution.
	byCSPRegion map[string]RegionRow
	rows        []RegionRow

	// vmRows are all virtual_machine rows; vmByRegionArch indexes them by
	// (csp, csp_region, architecture) for SKU resolution.
	vmRows         []VMRow
	vmByRegionArch map[string][]VMRow

	// osByKey indexes OS image rows by (csp, csp_region, os, version, arch).
	osByKey map[string]OSImageRow
}

var (
	_ RegionCatalog = (*EmbeddedCatalog)(nil)
	_ VMCatalog     = (*EmbeddedCatalog)(nil)
)

func key(csp, regionName string) string {
	return csp + "|" + strings.ToLower(strings.TrimSpace(regionName))
}

// NewEmbedded parses the embedded snapshot into an EmbeddedCatalog.
func NewEmbedded() (*EmbeddedCatalog, error) {
	rows, err := parseRegionCSV(regionCatalogCSV)
	if err != nil {
		return nil, err
	}
	c := &EmbeddedCatalog{byCSPRegion: make(map[string]RegionRow, len(rows)), rows: rows}
	for _, r := range rows {
		// First row for a (csp, region_name) wins; the snapshot is already
		// de-duplicated, so this is deterministic.
		k := key(r.CSP, r.RegionName)
		if _, exists := c.byCSPRegion[k]; !exists {
			c.byCSPRegion[k] = r
		}
	}

	vmRows, err := parseVMCSV(vmCatalogCSV)
	if err != nil {
		return nil, err
	}
	c.vmRows = vmRows
	c.vmByRegionArch = make(map[string][]VMRow, len(vmRows))
	for _, r := range vmRows {
		k := vmRegionArchKey(r.CSP, r.CSPRegion, r.Architecture)
		c.vmByRegionArch[k] = append(c.vmByRegionArch[k], r)
	}

	osRows, err := parseOSCSV(vmOSCatalogCSV)
	if err != nil {
		return nil, err
	}
	c.osByKey = make(map[string]OSImageRow, len(osRows))
	for _, r := range osRows {
		c.osByKey[osKey(r.CSP, r.CSPRegion, r.OSName, r.OSVersion, r.Architecture)] = r
	}
	return c, nil
}

func vmRegionArchKey(csp, cspRegion, arch string) string {
	return strings.ToLower(csp) + "|" + strings.ToLower(cspRegion) + "|" + strings.ToLower(arch)
}

func osKey(csp, cspRegion, os, version, arch string) string {
	return strings.ToLower(csp) + "|" + strings.ToLower(cspRegion) + "|" +
		strings.ToLower(os) + "|" + strings.ToLower(version) + "|" + strings.ToLower(arch)
}

// ResolveSKU implements VMCatalog. It looks for an exact (cpu, ram) match in the
// requested csp/region/architecture; no match is a hard error listing the
// nearest available sizes (never a silent fallback to a different size).
func (c *EmbeddedCatalog) ResolveSKU(_ context.Context, csp, cspRegion, arch string, cpu, ram int) (VMRow, error) {
	candidates := c.vmByRegionArch[vmRegionArchKey(csp, cspRegion, arch)]
	var exact []VMRow
	for _, r := range candidates {
		if r.CPU == cpu && r.RAM == ram {
			exact = append(exact, r)
		}
	}
	if len(exact) > 0 {
		// Deterministic pick among instances with identical cpu/ram: prefer the
		// general-purpose / burstable family (the wizard's canonical default —
		// t3 on AWS x86_64, t4g on AWS arm64, e2 on GCP, the s- droplet on DO),
		// then fall back to the lexicographically smallest name. This makes the
		// resolution deterministic without hard-coding instance maps.
		best := exact[0]
		bestRank := familyRank(best)
		for _, r := range exact[1:] {
			rank := familyRank(r)
			if rank < bestRank || (rank == bestRank && r.Name < best.Name) {
				best = r
				bestRank = rank
			}
		}
		return best, nil
	}
	return VMRow{}, ErrSKUNotFound{
		CSP: csp, CSPRegion: cspRegion, Architecture: arch, CPU: cpu, RAM: ram,
		Nearest: nearestSizes(candidates, cpu, ram, 5),
	}
}

// ResolveImage implements VMCatalog.
func (c *EmbeddedCatalog) ResolveImage(_ context.Context, csp, cspRegion, os, version, arch string) (OSImageRow, error) {
	row, ok := c.osByKey[osKey(csp, cspRegion, os, version, arch)]
	if !ok {
		return OSImageRow{}, ErrOSImageNotFound{
			CSP: csp, CSPRegion: cspRegion, OSName: os, OSVersion: version, Architecture: arch,
		}
	}
	return row, nil
}

// VMRows returns all virtual_machine rows (test/debug helper).
func (c *EmbeddedCatalog) VMRows() []VMRow { return c.vmRows }

func parseVMCSV(data string) ([]VMRow, error) {
	r := csv.NewReader(strings.NewReader(data))
	r.FieldsPerRecord = 9
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse vm catalog: %w", err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("parse vm catalog: empty")
	}
	out := make([]VMRow, 0, len(records)-1)
	for i, rec := range records {
		if i == 0 {
			continue // header
		}
		out = append(out, VMRow{
			Name:              rec[0],
			Family:            rec[1],
			CSP:               rec[2],
			CSPRegion:         rec[3],
			Architecture:      rec[4],
			CPU:               atoiOrZero(rec[5]),
			RAM:               atoiOrZero(rec[6]),
			GPU:               rec[7],
			SupportsAutoscale: strings.EqualFold(strings.TrimSpace(rec[8]), "true"),
		})
	}
	return out, nil
}

func parseOSCSV(data string) ([]OSImageRow, error) {
	r := csv.NewReader(strings.NewReader(data))
	r.FieldsPerRecord = 6
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse os catalog: %w", err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("parse os catalog: empty")
	}
	out := make([]OSImageRow, 0, len(records)-1)
	for i, rec := range records {
		if i == 0 {
			continue // header
		}
		out = append(out, OSImageRow{
			CSP:          rec[0],
			CSPRegion:    rec[1],
			OSName:       rec[2],
			OSVersion:    rec[3],
			Architecture: rec[4],
			Image:        rec[5],
		})
	}
	return out, nil
}

// MustEmbedded is NewEmbedded that panics on a malformed embedded snapshot
// (a build-time invariant, so a panic here is a programmer error, not runtime).
func MustEmbedded() *EmbeddedCatalog {
	c, err := NewEmbedded()
	if err != nil {
		panic(fmt.Sprintf("catalog: embedded region snapshot is invalid: %v", err))
	}
	return c
}

// ResolveRegion implements RegionCatalog.
func (c *EmbeddedCatalog) ResolveRegion(_ context.Context, regionName, provider string) (RegionRow, error) {
	csp, ok := ProviderToCSP(provider)
	if !ok {
		return RegionRow{}, fmt.Errorf(
			"unknown provider %q: wave-1 launch providers are aws, gcp, digitalocean", provider)
	}
	row, ok := c.byCSPRegion[key(csp, regionName)]
	if !ok {
		return RegionRow{}, ErrRegionNotFound{RegionName: regionName, Provider: provider}
	}
	return row, nil
}

// Rows returns all catalog rows (test/debug helper).
func (c *EmbeddedCatalog) Rows() []RegionRow { return c.rows }

func parseRegionCSV(data string) ([]RegionRow, error) {
	r := csv.NewReader(strings.NewReader(data))
	r.FieldsPerRecord = 6
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse region catalog: %w", err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("parse region catalog: empty")
	}
	out := make([]RegionRow, 0, len(records)-1)
	for i, rec := range records {
		if i == 0 {
			continue // header
		}
		out = append(out, RegionRow{
			MacroRegion:          rec[0],
			Country:              rec[1],
			RegionName:           rec[2],
			CSPRegion:            rec[3],
			CSPRegionDescription: rec[4],
			CSP:                  rec[5],
		})
	}
	return out, nil
}
