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

// EmbeddedCatalog resolves regions against the embedded `region` snapshot.
type EmbeddedCatalog struct {
	// rows keyed by (csp, lowercased region_name) for O(1) resolution.
	byCSPRegion map[string]RegionRow
	rows        []RegionRow
}

var _ RegionCatalog = (*EmbeddedCatalog)(nil)

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
	return c, nil
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
