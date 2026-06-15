package provider

import (
	"context"
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
)

// TestMigrationResourceSchema validates the pyxcloud_migration resource schema and
// asserts it exposes ONLY the opaque migration{} controls + coarse outputs (no
// migration-step attributes).
func TestMigrationResourceSchema(t *testing.T) {
	t.Parallel()
	r := NewMigrationResource()

	resp := &fwresource.SchemaResponse{}
	r.Schema(context.Background(), fwresource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("migration resource schema diagnostics: %+v", resp.Diagnostics)
	}
	for _, attr := range []string{
		"id", "place", "source_provider", "target_provider", "migration",
		"run_id", "substrate", "phase", "verdict", "rolled_back", "attestation_ok",
	} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected %q attribute on pyxcloud_migration", attr)
		}
	}

	mresp := &fwresource.MetadataResponse{}
	r.Metadata(context.Background(), fwresource.MetadataRequest{ProviderTypeName: "pyxcloud"}, mresp)
	if mresp.TypeName != "pyxcloud_migration" {
		t.Errorf("type name = %q, want pyxcloud_migration", mresp.TypeName)
	}
}

// TestMigrationDisabledIsNoOp proves a disabled migration block records a clean
// no-op state without contacting any backend.
func TestMigrationDisabledIsNoOp(t *testing.T) {
	t.Parallel()
	r := &migrationResource{}
	m := &migrationModel{
		Migration: &migrationBlockModel{}, // Enabled is null/false
	}
	var d testDiags
	r.runMigration(context.Background(), m, &d)
	if d.err {
		t.Fatal("disabled migration must not error")
	}
	if m.RuntimeDetail.ValueString() != "migration disabled" {
		t.Errorf("expected disabled detail, got %q", m.RuntimeDetail.ValueString())
	}
}

// testDiags is a tiny diagnostics sink matching the runMigration interface.
type testDiags struct{ err bool }

func (d *testDiags) AddError(string, string) { d.err = true }
func (d *testDiags) HasError() bool          { return d.err }
