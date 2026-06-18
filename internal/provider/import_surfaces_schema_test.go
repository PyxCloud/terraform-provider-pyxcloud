package provider

import (
	"context"
	"strings"
	"testing"

	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
)

func TestProviderRegistersImportSurfaces(t *testing.T) {
	t.Parallel()
	p := New("test")()

	if got := len(p.Resources(context.Background())); got != 4 {
		t.Errorf("expected 4 resources including import_topology, got %d", got)
	}
	if got := len(p.DataSources(context.Background())); got != 2 {
		t.Errorf("expected 2 data sources including import_discovery, got %d", got)
	}
}

func TestImportDiscoverySchema(t *testing.T) {
	t.Parallel()
	ds := NewImportDiscoveryDataSource()

	resp := &fwdatasource.SchemaResponse{}
	ds.Schema(context.Background(), fwdatasource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("import discovery schema diagnostics: %+v", resp.Diagnostics)
	}
	for _, attr := range []string{"account_binding", "cloud", "region", "filters", "resource_types", "resources", "observability_only"} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected %q attribute on pyxcloud_import_discovery", attr)
		}
	}
	if resp.Schema.Attributes["resources"].(dsschema.StringAttribute).Computed != true {
		t.Error("resources must be computed JSON output")
	}
	if _, ok := resp.Schema.Attributes["access_key"]; ok {
		t.Error("import discovery must not expose raw cloud access_key")
	}

	mresp := &fwdatasource.MetadataResponse{}
	ds.Metadata(context.Background(), fwdatasource.MetadataRequest{ProviderTypeName: "pyxcloud"}, mresp)
	if mresp.TypeName != "pyxcloud_import_discovery" {
		t.Errorf("expected type name pyxcloud_import_discovery, got %s", mresp.TypeName)
	}
}

func TestImportTopologySchemaAvoidsRawCredentialState(t *testing.T) {
	t.Parallel()
	r := NewImportTopologyResource()

	resp := &fwresource.SchemaResponse{}
	r.Schema(context.Background(), fwresource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("import topology schema diagnostics: %+v", resp.Diagnostics)
	}
	for _, attr := range []string{
		"id",
		"account_binding",
		"intent",
		"source_cloud",
		"source_region",
		"target_cloud",
		"target_region",
		"selected_resource_ids",
		"selected_resource_types",
		"migration_fee_token",
		"canonical_topology",
		"rendered_terraform",
		"fee_required",
		"fee_paid",
		"fee_reason",
		"checkout_url",
	} {
		if _, ok := resp.Schema.Attributes[attr]; !ok {
			t.Errorf("expected %q attribute on pyxcloud_import_topology", attr)
		}
	}
	token := resp.Schema.Attributes["migration_fee_token"].(rschema.StringAttribute)
	if !token.Optional || !token.Sensitive {
		t.Errorf("migration_fee_token should be optional sensitive input, got optional=%v sensitive=%v", token.Optional, token.Sensitive)
	}
	for _, forbidden := range []string{"access_key", "secret_key", "client_secret", "credentials"} {
		if _, ok := resp.Schema.Attributes[forbidden]; ok {
			t.Errorf("import topology must not expose raw credential field %q", forbidden)
		}
	}

	mresp := &fwresource.MetadataResponse{}
	r.Metadata(context.Background(), fwresource.MetadataRequest{ProviderTypeName: "pyxcloud"}, mresp)
	if mresp.TypeName != "pyxcloud_import_topology" {
		t.Errorf("expected type name pyxcloud_import_topology, got %s", mresp.TypeName)
	}
}

func TestImportTopologyFeeRequiredDiagnosticMessage(t *testing.T) {
	t.Parallel()
	diag := importTopologyFeeRequiredDiagnostic(&FeeRequiredErrorShim{
		reason:      "deployable import requires a migration fee",
		checkoutURL: "https://checkout.example/session",
	})
	if diag.Summary() != "Migration fee required" {
		t.Fatalf("summary = %q", diag.Summary())
	}
	if detail := diag.Detail(); detail == "" || !containsAll(detail, []string{"deployable import", "checkout.example", "migration_fee_token"}) {
		t.Fatalf("detail missing expected guidance: %q", detail)
	}
}

type FeeRequiredErrorShim struct {
	reason      string
	checkoutURL string
}

func (e *FeeRequiredErrorShim) Error() string      { return e.reason }
func (e *FeeRequiredErrorShim) FeeReason() string  { return e.reason }
func (e *FeeRequiredErrorShim) Checkout() string   { return e.checkoutURL }
func (e *FeeRequiredErrorShim) FeeRequired() bool  { return true }
func (e *FeeRequiredErrorShim) BackendStatus() int { return 402 }

func containsAll(s string, needles []string) bool {
	for _, needle := range needles {
		if !contains(s, needle) {
			return false
		}
	}
	return true
}

func contains(s, needle string) bool {
	return strings.Contains(s, needle)
}
