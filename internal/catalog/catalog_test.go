package catalog

import (
	"context"
	"errors"
	"testing"
)

func TestProviderToCSP(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		want string
		ok   bool
	}{
		"aws":          {"aws", true},
		"gcp":          {"gcp", true},
		"digitalocean": {"do", true}, // provider name -> catalog token
		"DigitalOcean": {"do", true}, // case-insensitive
		"  aws  ":      {"aws", true},
		"azure":        {"azure", true}, // wave-2: now enabled (pd-TF-W2-AZURE)
		"oracle":       {"", false},     // a wave-2 provider that is NOT yet enabled
		"":             {"", false},
	}
	for in, exp := range cases {
		got, ok := ProviderToCSP(in)
		if ok != exp.ok || got != exp.want {
			t.Errorf("ProviderToCSP(%q) = (%q,%v), want (%q,%v)", in, got, ok, exp.want, exp.ok)
		}
	}
}

func TestEmbeddedResolveRegion(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	ctx := context.Background()

	// Catalog-driven resolution: region_name + provider -> concrete csp_region.
	// These values come straight from the `region` table snapshot, not invented.
	cases := []struct {
		region, provider, wantCSPRegion, wantCSP string
	}{
		{"Dublin", "aws", "eu-west-1", "aws"},
		{"Frankfurt", "aws", "eu-central-1", "aws"},
		{"Frankfurt", "gcp", "europe-west3", "gcp"},
		{"Belgium", "gcp", "europe-west1", "gcp"},
		{"Amsterdam", "digitalocean", "ams3", "do"},
		{"Frankfurt", "digitalocean", "fra1", "do"},
		{"London", "digitalocean", "lon1", "do"},
		{"dublin", "aws", "eu-west-1", "aws"}, // case-insensitive region_name
	}
	for _, c := range cases {
		row, err := cat.ResolveRegion(ctx, c.region, c.provider)
		if err != nil {
			t.Errorf("ResolveRegion(%q,%q) error: %v", c.region, c.provider, err)
			continue
		}
		if row.CSPRegion != c.wantCSPRegion || row.CSP != c.wantCSP {
			t.Errorf("ResolveRegion(%q,%q) = csp_region=%q csp=%q, want %q/%q",
				c.region, c.provider, row.CSPRegion, row.CSP, c.wantCSPRegion, c.wantCSP)
		}
	}
}

func TestEmbeddedResolveRegionMissing(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	ctx := context.Background()

	// Dublin has no DigitalOcean region in the catalog -> hard error, no fallback.
	_, err := cat.ResolveRegion(ctx, "Dublin", "digitalocean")
	if err == nil {
		t.Fatal("expected ErrRegionNotFound for Dublin/digitalocean, got nil")
	}
	var notFound ErrRegionNotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("expected ErrRegionNotFound, got %T: %v", err, err)
	}

	// A region that does not exist for any provider.
	if _, err := cat.ResolveRegion(ctx, "Atlantis", "aws"); err == nil {
		t.Fatal("expected error for unknown region Atlantis")
	}

	// Unknown provider: a wave-2 provider that is NOT yet enabled. (Azure became
	// enabled in pd-TF-W2-AZURE, so "oracle" is the new not-yet-supported sentinel.)
	if _, err := cat.ResolveRegion(ctx, "Dublin", "oracle"); err == nil {
		t.Fatal("expected error for not-yet-enabled provider oracle")
	}
}

func TestEmbeddedSnapshotWellFormed(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	if len(cat.Rows()) == 0 {
		t.Fatal("embedded snapshot has no rows")
	}
	for _, r := range cat.Rows() {
		if r.RegionName == "" || r.CSPRegion == "" || r.CSP == "" {
			t.Errorf("malformed row: %+v", r)
		}
		switch r.CSP {
		case "aws", "gcp", "do", "azure", "linode", "ubicloud":
		default:
			t.Errorf("unexpected csp %q in catalog snapshot (row %+v)", r.CSP, r)
		}
	}
}
