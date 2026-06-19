package catalog

import (
	"context"
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
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

// mdbCatalogCSV is a snapshot of the backend `managed_database` table (wave-1
// providers: aws, gcp, do; the test regions present in the live ETL). It is the
// SAME table the wizard and Compare page resolve managed-database instance
// classes against. Embedding it keeps DB-class resolution catalog-driven and
// deterministic without a network round-trip; it is NOT a hand-authored class map.
//
//go:embed mdb_catalog.csv
var mdbCatalogCSV string

// oracleCatalogCSV is the wave-2 Oracle Cloud (OCI) catalog snapshot. The live
// provider-catalog ETL has no OCI rows yet (the censused `region` /
// `virtual_machine` / `virtual_machine_operating_system` / `managed_database`
// tables are AWS/GCP/DO only), so — exactly as documented in the PR and per
// SPEC §4 ("missing data / unverifiable resource → clean plan-time error;
// author from the public OCI catalog + document the gap") — these rows are
// authored from Oracle's public OCI shape / region / image / MySQL-shape
// catalogs. They live in ONE new file (not spread across the wave-1 CSVs) so a
// future live OCI ETL replaces this file wholesale and the wave-1 snapshots stay
// untouched. It is NOT a hand-authored provider map embedded in the binary: it is
// the SAME catalog shape (region/vm/os/mdb rows), just sourced manually until the
// ETL lands. The first column is a row KIND tag (region|vm|os|mdb) so all four
// row types share one snapshot; the remaining columns mirror the wave-1 CSVs.
//
//go:embed oracle_catalog.csv
var oracleCatalogCSV string

// linodeCatalogCSV is the wave-2 Linode (Akamai) catalog snapshot. Unlike the
// wave-1 per-table CSVs, it is a single multiplexed file: a leading `kind` column
// (region | vm | os | mdb) selects the row shape, and every row carries csp=linode
// implicitly. It is folded into the SAME region / virtual_machine / OS / managed_
// database maps the wave-1 snapshots populate, so Linode resolution reuses the
// identical ResolveRegion / ResolveSKU / ResolveImage / ResolveDBClass paths — no
// second resolution engine. See linode_catalog.csv for the provenance note (the
// live Linode ETL `linode_vm.csv` is not present in this repo, so the rows are
// authored from the public Linode catalog and the gap is documented there).
//
//go:embed linode_catalog.csv
var linodeCatalogCSV string

// IBM Cloud (wave-2) catalog snapshots. The live PyxCloud catalog ETL has no IBM
// rows yet (the censused `region`/`virtual_machine`/`managed_database` tables
// cover the wave-1 providers only), so these IBM snapshots are AUTHORED from the
// public IBM Cloud catalog (VPC regions/zones, instance profiles, stock images,
// and ICD database plans) and kept in dedicated IBM-only files so they merge
// without conflict alongside the wave-1 snapshots. They have the SAME column
// shape as the wave-1 CSVs and are parsed by the same parsers, then merged into
// the EmbeddedCatalog indexes — keeping IBM resolution catalog-driven and
// deterministic. This authoring gap (no live IBM ETL) is documented in the PR; a
// future IBM ETL drops these files and feeds the shared tables instead.
//
//go:embed ibm_catalog.csv
var ibmRegionCatalogCSV string

//go:embed ibm_vm_catalog.csv
var ibmVMCatalogCSV string

//go:embed ibm_vm_os_catalog.csv
var ibmVMOSCatalogCSV string

//go:embed ibm_mdb_catalog.csv
var ibmMDBCatalogCSV string

// alibabaCatalogCSV is the Alibaba Cloud (alicloud) wave-2 catalog snapshot
// (pd-TF-PROVIDERS-WAVE2: alibaba). It is the human-readable, Alibaba-only
// source-of-truth + gap record: there is NO live PyxCloud ETL for Alibaba yet, so
// these rows are AUTHORED from the public Alibaba catalog and documented as a gap.
// The SAME rows are mirrored into the four loader CSVs above (region/vm/os/mdb),
// which is what EmbeddedCatalog actually parses; this file is embedded for
// provenance and is validated non-empty at load (a build-time invariant). When the
// live Alibaba ETL lands it REPLACES these rows verbatim — the resolution code does
// not change (it is catalog-driven, never a hard-coded provider map).
//
//go:embed alibaba_catalog.csv
var alibabaCatalogCSV string

// AlibabaCatalogProvenance returns the embedded Alibaba source-of-truth/gap record
// (test/debug helper, and the provenance the gap documentation references).
func AlibabaCatalogProvenance() string { return alibabaCatalogCSV }

// stackitCatalogCSV is the wave-2 StackIt catalog snapshot (pd-TF-PROVIDERS-WAVE2:
// stackit). It is a SINGLE multi-section file (region / vm / mdb / os) parsed by
// parseStackItCatalog and merged into the same resolution maps the wave-1 rows
// populate, so StackIt resolution is catalog-driven exactly like aws/gcp/do.
// Authored from the public StackIt catalog because no live ETL exists for stackit
// yet (the gap is documented at the top of the file); swap for a live snapshot
// when stackit is censused — no code change needed.
//
//go:embed stackit_catalog.csv
var stackitCatalogCSV string

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

	// mdbRows are all managed_database rows; mdbByRegionEngine indexes them by
	// (csp, csp_region, engine) for DB-class resolution.
	mdbRows        []MDBRow
	mdbByRegionEng map[string][]MDBRow
}

var (
	_ RegionCatalog = (*EmbeddedCatalog)(nil)
	_ VMCatalog     = (*EmbeddedCatalog)(nil)
	_ MDBCatalog    = (*EmbeddedCatalog)(nil)
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
	// Merge the authored IBM Cloud (wave-2) region snapshot. Same column shape,
	// same parser; appended after the wave-1 rows.
	ibmRows, err := parseRegionCSV(ibmRegionCatalogCSV)
	if err != nil {
		return nil, err
	}
	rows = append(rows, ibmRows...)
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
	ibmVMRows, err := parseVMCSV(ibmVMCatalogCSV)
	if err != nil {
		return nil, err
	}
	vmRows = append(vmRows, ibmVMRows...)
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
	ibmOSRows, err := parseOSCSV(ibmVMOSCatalogCSV)
	if err != nil {
		return nil, err
	}
	osRows = append(osRows, ibmOSRows...)
	c.osByKey = make(map[string]OSImageRow, len(osRows))
	for _, r := range osRows {
		c.osByKey[osKey(r.CSP, r.CSPRegion, r.OSName, r.OSVersion, r.Architecture)] = r
	}

	mdbRows, err := parseMDBCSV(mdbCatalogCSV)
	if err != nil {
		return nil, err
	}
	ibmMDBRows, err := parseMDBCSV(ibmMDBCatalogCSV)
	if err != nil {
		return nil, err
	}
	mdbRows = append(mdbRows, ibmMDBRows...)
	c.mdbRows = mdbRows
	c.mdbByRegionEng = make(map[string][]MDBRow, len(mdbRows))
	for _, r := range mdbRows {
		k := mdbRegionEngineKey(r.CSP, r.CSPRegion, r.Engine)
		c.mdbByRegionEng[k] = append(c.mdbByRegionEng[k], r)
	}

	// Wave-2: merge the Azure catalog snapshot (azure_catalog.csv) into the same
	// indexes. Kept in its own loader/file so the wave-2 Azure PR is conflict-free
	// against the concurrently-edited wave-1 snapshots (loadAzure -> render_azure.go).
	if err := c.loadAzure(); err != nil {
		return nil, err
	}
	// Wave-2: fold the multiplexed Linode snapshot into the SAME maps so Linode
	// resolution reuses the identical wave-1 resolution paths (no second engine).
	if err := c.foldLinodeCatalog(linodeCatalogCSV); err != nil {
		return nil, err
	}
	// Wave-2: merge the Ubicloud catalog snapshot (ubicloud_catalog.csv) into the
	// same indexes. Kept in its own loader/file so the wave-2 Ubicloud PR is
	// conflict-free against the concurrently-edited wave-1 snapshots
	// (loadUbicloud -> render_ubicloud.go).
	if err := c.loadUbicloud(); err != nil {
		return nil, err
	}
	// Wave-2 Oracle Cloud (OCI) rows. One combined snapshot (region/vm/os/mdb)
	// merged into the SAME indexes the wave-1 rows use, so OCI resolves through
	// the identical ResolveRegion / ResolveSKU / ResolveImage / ResolveDBClass
	// path — no separate OCI engine.
	if err := c.loadOracleCatalog(oracleCatalogCSV); err != nil {
		return nil, err
	}
	// Wave-2 OVHcloud rows. The OVH PR shipped its own OVHCatalog + renderers;
	// fold its region/flavor snapshot into the SAME indexes so OVH resolves and
	// renders through the common Translate*/Render*HCL path like the other seven.
	if err := c.loadOVHCatalog(); err != nil {
		return nil, err
	}
	// Wave-2: merge the StackIt catalog (one multi-section file) into the same
	// resolution maps. Done after the wave-1 rows so the wave-1 indices are intact;
	// StackIt rows simply extend them (different csp token, no collision).
	if err := c.mergeStackIt(stackitCatalogCSV); err != nil {
		return nil, err
	}
	// Build-time invariant: the Alibaba provenance/gap record must be present
	// (it documents that the alicloud rows mirrored into the loader CSVs are
	// authored from the public catalog, not yet a live ETL). An empty embed is a
	// packaging error, not a runtime condition.
	if strings.TrimSpace(alibabaCatalogCSV) == "" {
		return nil, fmt.Errorf("parse alibaba catalog: embedded provenance snapshot is empty")
	}
	return c, nil
}

// mergeStackIt parses the multi-section StackIt catalog and merges its rows into
// the region / vm / mdb / os resolution maps. It mirrors how the wave-1 CSVs are
// loaded; the only difference is the sections live in one file (kept separate from
// the wave-1 CSVs to keep wave-2 merge-conflict-free).
func (c *EmbeddedCatalog) mergeStackIt(data string) error {
	sections, err := parseStackItCatalog(data)
	if err != nil {
		return err
	}
	for _, rec := range sections["region"] {
		if len(rec) != 6 {
			return fmt.Errorf("stackit catalog: region row needs 6 fields, got %d: %v", len(rec), rec)
		}
		row := RegionRow{
			MacroRegion: rec[0], Country: rec[1], RegionName: rec[2],
			CSPRegion: rec[3], CSPRegionDescription: rec[4], CSP: rec[5],
		}
		c.rows = append(c.rows, row)
		k := key(row.CSP, row.RegionName)
		if _, exists := c.byCSPRegion[k]; !exists {
			c.byCSPRegion[k] = row
		}
	}
	for _, rec := range sections["vm"] {
		if len(rec) != 9 {
			return fmt.Errorf("stackit catalog: vm row needs 9 fields, got %d: %v", len(rec), rec)
		}
		row := VMRow{
			Name: rec[0], Family: rec[1], CSP: rec[2], CSPRegion: rec[3],
			Architecture: rec[4], CPU: atoiOrZero(rec[5]), RAM: atoiOrZero(rec[6]),
			GPU: rec[7], SupportsAutoscale: strings.EqualFold(strings.TrimSpace(rec[8]), "true"),
		}
		c.vmRows = append(c.vmRows, row)
		k := vmRegionArchKey(row.CSP, row.CSPRegion, row.Architecture)
		c.vmByRegionArch[k] = append(c.vmByRegionArch[k], row)
	}
	for _, rec := range sections["mdb"] {
		if len(rec) != 7 {
			return fmt.Errorf("stackit catalog: mdb row needs 7 fields, got %d: %v", len(rec), rec)
		}
		row := MDBRow{
			Name: rec[0], Family: rec[1], CSP: rec[2], CSPRegion: rec[3],
			Engine: rec[4], CPU: atoiOrZero(rec[5]), RAM: atoiOrZero(rec[6]),
		}
		c.mdbRows = append(c.mdbRows, row)
		k := mdbRegionEngineKey(row.CSP, row.CSPRegion, row.Engine)
		c.mdbByRegionEng[k] = append(c.mdbByRegionEng[k], row)
	}
	for _, rec := range sections["os"] {
		if len(rec) != 6 {
			return fmt.Errorf("stackit catalog: os row needs 6 fields, got %d: %v", len(rec), rec)
		}
		row := OSImageRow{
			CSP: rec[0], CSPRegion: rec[1], OSName: rec[2],
			OSVersion: rec[3], Architecture: rec[4], Image: rec[5],
		}
		c.osByKey[osKey(row.CSP, row.CSPRegion, row.OSName, row.OSVersion, row.Architecture)] = row
	}
	return nil
}

// parseStackItCatalog splits the multi-section StackIt catalog into per-section
// CSV records. Sections start with "@section <name>"; '#' lines and blank lines
// are ignored. Each data line is comma-split (the values carry no embedded commas).
func parseStackItCatalog(data string) (map[string][][]string, error) {
	out := map[string][][]string{}
	current := ""
	for _, raw := range strings.Split(data, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "@section "); ok {
			current = strings.TrimSpace(rest)
			continue
		}
		if current == "" {
			return nil, fmt.Errorf("stackit catalog: data line before any @section: %q", line)
		}
		fields := strings.Split(line, ",")
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}
		out[current] = append(out[current], fields)
	}
	return out, nil
}

// loadOracleCatalog parses the combined wave-2 OCI snapshot (region/vm/os/mdb
// rows distinguished by a leading KIND column) and merges every row into the
// same maps/slices the wave-1 catalog populates. A malformed row is a hard error
// (a build-time invariant via MustEmbedded), never a silent skip.
func (c *EmbeddedCatalog) loadOracleCatalog(data string) error {
	r := csv.NewReader(strings.NewReader(data))
	r.FieldsPerRecord = -1 // variable: each KIND has its own column count
	records, err := r.ReadAll()
	if err != nil {
		return fmt.Errorf("parse oracle catalog: %w", err)
	}
	for i, rec := range records {
		if i == 0 {
			continue // header
		}
		if len(rec) == 0 || strings.TrimSpace(rec[0]) == "" {
			continue // blank separator line
		}
		kind := strings.ToLower(strings.TrimSpace(rec[0]))
		switch kind {
		case "region":
			if len(rec) != 7 {
				return fmt.Errorf("parse oracle catalog: region row %d wants 7 fields, got %d", i, len(rec))
			}
			row := RegionRow{
				MacroRegion: rec[1], Country: rec[2], RegionName: rec[3],
				CSPRegion: rec[4], CSPRegionDescription: rec[5], CSP: rec[6],
			}
			c.rows = append(c.rows, row)
			k := key(row.CSP, row.RegionName)
			if _, exists := c.byCSPRegion[k]; !exists {
				c.byCSPRegion[k] = row
			}
		case "vm":
			if len(rec) != 10 {
				return fmt.Errorf("parse oracle catalog: vm row %d wants 10 fields, got %d", i, len(rec))
			}
			row := VMRow{
				Name: rec[1], Family: rec[2], CSP: rec[3], CSPRegion: rec[4],
				Architecture: rec[5], CPU: atoiOrZero(rec[6]), RAM: atoiOrZero(rec[7]),
				GPU: rec[8], SupportsAutoscale: strings.EqualFold(strings.TrimSpace(rec[9]), "true"),
			}
			c.vmRows = append(c.vmRows, row)
			k := vmRegionArchKey(row.CSP, row.CSPRegion, row.Architecture)
			c.vmByRegionArch[k] = append(c.vmByRegionArch[k], row)
		case "os":
			if len(rec) != 7 {
				return fmt.Errorf("parse oracle catalog: os row %d wants 7 fields, got %d", i, len(rec))
			}
			row := OSImageRow{
				CSP: rec[1], CSPRegion: rec[2], OSName: rec[3], OSVersion: rec[4],
				Architecture: rec[5], Image: rec[6],
			}
			c.osByKey[osKey(row.CSP, row.CSPRegion, row.OSName, row.OSVersion, row.Architecture)] = row
		case "mdb":
			if len(rec) != 8 {
				return fmt.Errorf("parse oracle catalog: mdb row %d wants 8 fields, got %d", i, len(rec))
			}
			row := MDBRow{
				Name: rec[1], Family: rec[2], CSP: rec[3], CSPRegion: rec[4],
				Engine: rec[5], CPU: atoiOrZero(rec[6]), RAM: atoiOrZero(rec[7]),
			}
			c.mdbRows = append(c.mdbRows, row)
			k := mdbRegionEngineKey(row.CSP, row.CSPRegion, row.Engine)
			c.mdbByRegionEng[k] = append(c.mdbByRegionEng[k], row)
		default:
			return fmt.Errorf("parse oracle catalog: row %d has unknown kind %q (region|vm|os|mdb)", i, kind)
		}
	}
	return nil
}

// foldLinodeCatalog parses the multiplexed Linode snapshot and folds its region /
// vm / os / mdb rows into the embedded catalog's existing maps. Each row's csp is
// forced to "linode"; a leading `kind` column selects the row shape. A malformed
// row is a hard error (a build-time invariant via MustEmbedded), never a silent skip.
func (c *EmbeddedCatalog) foldLinodeCatalog(data string) error {
	r := csv.NewReader(strings.NewReader(data))
	// 9 columns: kind + up to 8 payload columns (the widest row, vm, uses all 8).
	r.FieldsPerRecord = 9
	records, err := r.ReadAll()
	if err != nil {
		return fmt.Errorf("parse linode catalog: %w", err)
	}
	for i, rec := range records {
		if i == 0 {
			continue // header
		}
		kind := strings.ToLower(strings.TrimSpace(rec[0]))
		switch kind {
		case "region":
			// region,macro_region,country,region_name,csp_region,csp_region_description,,,
			row := RegionRow{
				MacroRegion:          rec[1],
				Country:              rec[2],
				RegionName:           rec[3],
				CSPRegion:            rec[4],
				CSPRegionDescription: rec[5],
				CSP:                  cspLinode,
			}
			c.rows = append(c.rows, row)
			k := key(cspLinode, row.RegionName)
			if _, exists := c.byCSPRegion[k]; !exists {
				c.byCSPRegion[k] = row
			}
		case "vm":
			// vm,name,family,csp_region,architecture,cpu,ram,gpu,supports_autoscale
			row := VMRow{
				Name:              rec[1],
				Family:            rec[2],
				CSP:               cspLinode,
				CSPRegion:         rec[3],
				Architecture:      rec[4],
				CPU:               atoiOrZero(rec[5]),
				RAM:               atoiOrZero(rec[6]),
				GPU:               rec[7],
				SupportsAutoscale: strings.EqualFold(strings.TrimSpace(rec[8]), "true"),
			}
			c.vmRows = append(c.vmRows, row)
			vk := vmRegionArchKey(row.CSP, row.CSPRegion, row.Architecture)
			c.vmByRegionArch[vk] = append(c.vmByRegionArch[vk], row)
		case "os":
			// os,csp_region,os_name,os_version,architecture,image,,,
			row := OSImageRow{
				CSP:          cspLinode,
				CSPRegion:    rec[1],
				OSName:       rec[2],
				OSVersion:    rec[3],
				Architecture: rec[4],
				Image:        rec[5],
			}
			c.osByKey[osKey(row.CSP, row.CSPRegion, row.OSName, row.OSVersion, row.Architecture)] = row
		case "mdb":
			// mdb,name,family,csp_region,engine,cpu,ram,,
			row := MDBRow{
				Name:      rec[1],
				Family:    rec[2],
				CSP:       cspLinode,
				CSPRegion: rec[3],
				Engine:    rec[4],
				CPU:       atoiOrZero(rec[5]),
				RAM:       atoiOrZero(rec[6]),
			}
			c.mdbRows = append(c.mdbRows, row)
			mk := mdbRegionEngineKey(row.CSP, row.CSPRegion, row.Engine)
			c.mdbByRegionEng[mk] = append(c.mdbByRegionEng[mk], row)
		default:
			return fmt.Errorf("linode catalog row %d: unknown kind %q (region | vm | os | mdb)", i+1, kind)
		}
	}
	return nil
}

func mdbRegionEngineKey(csp, cspRegion, engine string) string {
	return strings.ToLower(csp) + "|" + strings.ToLower(cspRegion) + "|" + strings.ToLower(engine)
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
func (c *EmbeddedCatalog) ResolveSKU(ctx context.Context, csp, cspRegion, arch string, cpu, ram int) (VMRow, error) {
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

		if err := validateSKUJIT(ctx, csp, cspRegion, best.Name); err != nil {
			return VMRow{}, fmt.Errorf("JIT validation failed: %w", err)
		}

		return best, nil
	}
	return VMRow{}, ErrSKUNotFound{
		CSP: csp, CSPRegion: cspRegion, Architecture: arch, CPU: cpu, RAM: ram,
		Nearest: nearestSizes(candidates, cpu, ram, 5),
	}
}

type awsInstanceTypeOfferings struct {
	InstanceTypeOfferings []struct {
		InstanceType string `json:"InstanceType"`
		Location     string `json:"Location"`
	} `json:"InstanceTypeOfferings"`
}

func validateSKUJIT(ctx context.Context, csp, cspRegion, instanceType string) error {
	if os.Getenv("PYXCLOUD_BYPASS_JIT_CHECK") == "true" {
		return nil
	}

	switch csp {
	case "aws":
		awsPath, err := exec.LookPath("aws")
		if err != nil {
			return nil // AWS CLI not installed, skip
		}

		// Check if credentials are valid by running get-caller-identity
		ctxWithTimeout, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctxWithTimeout, awsPath, "sts", "get-caller-identity")
		if err := cmd.Run(); err != nil {
			return nil // Credentials not present or invalid, skip JIT check
		}

		// Query instance type offerings in this region
		ctxQuery, cancelQuery := context.WithTimeout(ctx, 5*time.Second)
		defer cancelQuery()
		queryCmd := exec.CommandContext(ctxQuery, awsPath, "ec2", "describe-instance-type-offerings",
			"--region", cspRegion,
			"--filters", fmt.Sprintf("Name=instance-type,Values=%s", instanceType),
			"--output", "json")

		output, err := queryCmd.Output()
		if err != nil {
			return nil // Ignore connection/transport errors, skip JIT check
		}

		var offerings awsInstanceTypeOfferings
		if err := json.Unmarshal(output, &offerings); err != nil {
			return nil
		}

		if len(offerings.InstanceTypeOfferings) == 0 {
			return fmt.Errorf("instance type %q is not offered in region %q according to live AWS API", instanceType, cspRegion)
		}

	case "gcp":
		gcloudPath, err := exec.LookPath("gcloud")
		if err != nil {
			return nil // gcloud CLI not installed, skip
		}

		ctxWithTimeout, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctxWithTimeout, gcloudPath, "auth", "print-access-token")
		if err := cmd.Run(); err != nil {
			return nil // Not authenticated, skip
		}

		ctxQuery, cancelQuery := context.WithTimeout(ctx, 5*time.Second)
		defer cancelQuery()
		zoneFilter := fmt.Sprintf("name=%s AND zone:( %s* )", instanceType, cspRegion)
		queryCmd := exec.CommandContext(ctxQuery, gcloudPath, "compute", "machine-types", "list",
			"--filter", zoneFilter,
			"--format", "json")

		output, err := queryCmd.Output()
		if err != nil {
			return nil
		}

		var list []any
		if err := json.Unmarshal(output, &list); err != nil {
			return nil
		}

		if len(list) == 0 {
			return fmt.Errorf("machine type %q is not offered in region %q according to live GCP API", instanceType, cspRegion)
		}
	}

	return nil
}

// ResolveDBClass implements MDBCatalog. It looks for an exact (cpu, ram) match in
// the requested csp/region/engine; no match is a hard error listing the nearest
// available sizes (never a silent fallback to a different size).
func (c *EmbeddedCatalog) ResolveDBClass(_ context.Context, csp, cspRegion, engine string, cpu, ram int) (MDBRow, error) {
	candidates := c.mdbByRegionEng[mdbRegionEngineKey(csp, cspRegion, engine)]
	var exact []MDBRow
	for _, r := range candidates {
		if r.CPU == cpu && r.RAM == ram {
			exact = append(exact, r)
		}
	}
	if len(exact) > 0 {
		// Deterministic pick among classes with identical cpu/ram: the
		// lexicographically smallest name (the catalog snapshot is already
		// de-duplicated per size, so this is stable). No hard-coded class map.
		best := exact[0]
		for _, r := range exact[1:] {
			if r.Name < best.Name {
				best = r
			}
		}
		return best, nil
	}
	return MDBRow{}, ErrDBClassNotFound{
		CSP: csp, CSPRegion: cspRegion, Engine: engine, CPU: cpu, RAM: ram,
		Nearest: nearestDBSizes(candidates, cpu, ram, 5),
	}
}

// MDBRows returns all managed_database rows (test/debug helper).
func (c *EmbeddedCatalog) MDBRows() []MDBRow { return c.mdbRows }

func parseMDBCSV(data string) ([]MDBRow, error) {
	r := csv.NewReader(strings.NewReader(data))
	r.FieldsPerRecord = 7
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse managed_database catalog: %w", err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("parse managed_database catalog: empty")
	}
	out := make([]MDBRow, 0, len(records)-1)
	for i, rec := range records {
		if i == 0 {
			continue // header
		}
		out = append(out, MDBRow{
			Name:      rec[0],
			Family:    rec[1],
			CSP:       rec[2],
			CSPRegion: rec[3],
			Engine:    rec[4],
			CPU:       atoiOrZero(rec[5]),
			RAM:       atoiOrZero(rec[6]),
		})
	}
	return out, nil
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
			"unknown provider %q: supported providers are aws, gcp, digitalocean, azure, linode, ubicloud, oracle, ibm, alicloud, ovh, stackit", provider)
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
