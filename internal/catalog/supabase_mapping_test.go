package catalog

import (
	"testing"
)

func TestLookupSupabaseService_byName(t *testing.T) {
	cases := []struct {
		input         string
		wantFound     bool
		wantKind      SupabaseServiceKind
		wantCanonical string
	}{
		{"db", true, SupabaseKindCanonical, TypeManagedDatabase},
		{"auth", true, SupabaseKindAbsorbed, ""},
		{"rest", true, SupabaseKindEliminated, ""},
		{"realtime", true, SupabaseKindCanonical, TypeEventStreaming},
		{"storage", true, SupabaseKindCanonical, TypeObjectStorage},
		{"imgproxy", true, SupabaseKindCanonical, TypeVirtualMachine},
		{"meta", true, SupabaseKindEliminated, ""},
		{"functions", true, SupabaseKindCanonical, TypeServerlessFunction},
		{"analytics", true, SupabaseKindAbsorbed, ""},
		{"kong", true, SupabaseKindCanonical, TypeLoadBalancer},
		{"studio", true, SupabaseKindAbsorbed, ""},
		{"vector", true, SupabaseKindAbsorbed, ""},
		{"redis", true, SupabaseKindCanonical, TypeCache},
		{"unknown-service", false, "", ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			e, ok := LookupSupabaseService(tc.input)
			if ok != tc.wantFound {
				t.Fatalf("LookupSupabaseService(%q) found=%v want %v", tc.input, ok, tc.wantFound)
			}
			if !ok {
				return
			}
			if e.Kind != tc.wantKind {
				t.Errorf("kind=%q want %q", e.Kind, tc.wantKind)
			}
			if e.CanonicalType != tc.wantCanonical {
				t.Errorf("canonicalType=%q want %q", e.CanonicalType, tc.wantCanonical)
			}
		})
	}
}

func TestLookupSupabaseService_byImage(t *testing.T) {
	cases := []struct {
		image     string
		wantFound bool
		wantSvc   string
	}{
		{"supabase/gotrue:v2.99.0", true, "auth"},
		{"postgres:15-alpine", true, "db"},
		{"supabase/realtime:v2.28.32", true, "realtime"},
		{"supabase/storage-api:v0.47.3", true, "storage"},
		{"supabase/edge-runtime:v1.45.2", true, "functions"},
		{"kong:3.4.2", true, "kong"},
		{"redis:7.0.13", true, "redis"},
		{"timberio/vector:0.34.0-alpine", true, "vector"},
		{"nginx:latest", false, ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.image, func(t *testing.T) {
			e, ok := LookupSupabaseService(tc.image)
			if ok != tc.wantFound {
				t.Fatalf("LookupSupabaseService(%q) found=%v want %v", tc.image, ok, tc.wantFound)
			}
			if !ok {
				return
			}
			if e.SupabaseService != tc.wantSvc {
				t.Errorf("service=%q want %q", e.SupabaseService, tc.wantSvc)
			}
		})
	}
}

func TestSupabaseCanonicalComponents(t *testing.T) {
	entries := SupabaseCanonicalComponents()
	// All returned entries must be SupabaseKindCanonical with a non-empty CanonicalType.
	for _, e := range entries {
		if e.Kind != SupabaseKindCanonical {
			t.Errorf("SupabaseCanonicalComponents returned non-canonical entry: %+v", e)
		}
		if e.CanonicalType == "" {
			t.Errorf("canonical entry %q has empty CanonicalType", e.SupabaseService)
		}
	}
	// Spot-check key entries are present.
	wantCanonical := map[string]bool{
		"db": false, "realtime": false, "storage": false,
		"functions": false, "redis": false, "kong": false,
	}
	for _, e := range entries {
		wantCanonical[e.SupabaseService] = true
	}
	for svc, found := range wantCanonical {
		if !found {
			t.Errorf("expected canonical entry for Supabase service %q not found", svc)
		}
	}
}

func TestSupabaseAbsorbedComponents(t *testing.T) {
	entries := SupabaseAbsorbedComponents()
	for _, e := range entries {
		if e.Kind != SupabaseKindAbsorbed {
			t.Errorf("SupabaseAbsorbedComponents returned non-absorbed entry: %+v", e)
		}
		if e.AbsorbedBy == "" {
			t.Errorf("absorbed entry %q has empty AbsorbedBy", e.SupabaseService)
		}
	}
	// auth, analytics, studio, vector must be absorbed.
	wantAbsorbed := map[string]bool{
		"auth": false, "analytics": false, "studio": false, "vector": false,
	}
	for _, e := range entries {
		wantAbsorbed[e.SupabaseService] = true
	}
	for svc, found := range wantAbsorbed {
		if !found {
			t.Errorf("expected absorbed entry for Supabase service %q not found", svc)
		}
	}
}
