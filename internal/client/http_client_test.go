package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func sampleTopology() Topology {
	return Topology{
		Name:     "production",
		Provider: "aws",
		Region:   "Dublin",
		Components: []Component{
			{Name: "app", Type: "virtual-machine", Count: 2,
				VM: &VMType{Architecture: "x86_64", CPU: "2", RAM: "4", OS: "ubuntu"}},
			{Name: "db", Type: "managed-database"},
		},
	}
}

func TestComponentsCanonicalRoundTrip(t *testing.T) {
	in := sampleTopology().Components
	got := canonicalToComponents(componentsToCanonical(in))
	if len(got) != len(in) {
		t.Fatalf("len = %d want %d", len(got), len(in))
	}
	if got[0].Name != "app" || got[0].Type != "virtual-machine" || got[0].Count != 2 {
		t.Errorf("vm node lost fields: %+v", got[0])
	}
	if got[0].VM == nil || got[0].VM.CPU != "2" || got[0].VM.RAM != "4" ||
		got[0].VM.Architecture != "x86_64" || got[0].VM.OS != "ubuntu" {
		t.Errorf("vm sizing not round-tripped: %+v", got[0].VM)
	}
	if got[1].Type != "managed-database" || got[1].VM != nil {
		t.Errorf("non-vm node should have nil VM: %+v", got[1])
	}
}

func TestHTTPClientCanonicalShape(t *testing.T) {
	// Assert the wire body carries the nested canonical shape the backend engine reads.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok123" {
			t.Errorf("missing/wrong bearer: %q", got)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		canon, _ := body["canonical"].([]any)
		if len(canon) == 0 {
			t.Fatalf("no canonical in body: %v", body)
		}
		node, _ := canon[0].(map[string]any)
		props, _ := node["properties"].(map[string]any)
		vm, ok := props["virtual-machine"].(map[string]any)
		if !ok {
			t.Fatalf("first node missing properties.virtual-machine: %v", node)
		}
		typ, _ := vm["type"].(map[string]any)
		if typ["cpu"] != "2" || typ["ram"] != "4" {
			t.Errorf("vm type not mapped: %v", typ)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "top-xyz", "name": "production"})
	}))
	defer srv.Close()

	c := NewHTTP(Config{Endpoint: srv.URL, Token: "tok123"})
	out, err := c.CreateTopology(context.Background(), sampleTopology())
	if err != nil {
		t.Fatalf("CreateTopology: %v", err)
	}
	if out.ID != "top-xyz" {
		t.Errorf("server id not adopted: %q", out.ID)
	}
	// Components preserved verbatim from the caller.
	if len(out.Components) != 2 || out.Components[0].VM == nil {
		t.Errorf("caller components not preserved: %+v", out.Components)
	}
}

func TestHTTPClientCompareMapsResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"provider": "aws", "region": "EU West", "hourly_usd": 0.05, "monthly_usd": 36.5, "priceable": true},
			},
		})
	}))
	defer srv.Close()

	c := NewHTTP(Config{Endpoint: srv.URL, Token: "t"})
	got, err := c.Compare(context.Background(), sampleTopology(),
		[]Candidate{{Provider: "aws", Region: "EU West"}})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if len(got) != 1 || got[0].Provider != "aws" || !got[0].Priceable || got[0].HourlyUSD != 0.05 {
		t.Errorf("compare result not mapped: %+v", got)
	}
}

func TestHTTPClientTranslate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["provider"] != "aws" || body["region"] != "Dublin" {
			t.Errorf("provider/region not sent: %v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"terraform":  []string{"resource \"aws_instance\" \"app\" {}", "resource \"aws_db_instance\" \"db\" {}"},
			"provider":   "aws",
			"region":     "Dublin",
			"csp_region": "eu-west-1",
		})
	}))
	defer srv.Close()

	c := NewHTTP(Config{Endpoint: srv.URL, Token: "t"})
	out, err := c.Translate(context.Background(), sampleTopology())
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if out.CSPRegion != "eu-west-1" || len(out.Terraform) != 2 {
		t.Errorf("translate result not mapped: %+v", out)
	}
}

func TestHTTPClientTranslateError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "no cspRegion for region 'Mars' on provider 'aws'"})
	}))
	defer srv.Close()
	c := NewHTTP(Config{Endpoint: srv.URL, Token: "t"})
	_, err := c.Translate(context.Background(), sampleTopology())
	if err == nil || !strings.Contains(err.Error(), "no cspRegion") {
		t.Errorf("expected surfaced backend error, got: %v", err)
	}
}

func TestHTTPClientGetNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := NewHTTP(Config{Endpoint: srv.URL, Token: "t"})
	_, found, err := c.GetTopology(context.Background(), "missing")
	if err != nil {
		t.Fatalf("GetTopology: %v", err)
	}
	if found {
		t.Error("expected found=false on 404")
	}
}

func TestHTTPClientImportDiscovery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/import/discovery" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body ImportDiscoveryRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.AccountBinding != "acct-prod" || body.Cloud != "aws" || body.Region != "eu-west-1" {
			t.Fatalf("request binding/cloud/region not mapped: %+v", body)
		}
		if len(body.ResourceTypes) != 1 || body.ResourceTypes[0] != "aws_instance" {
			t.Fatalf("resource types not mapped: %+v", body.ResourceTypes)
		}
		if _, ok := body.Filters["tag:env"]; !ok {
			t.Fatalf("filters not mapped: %+v", body.Filters)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resources":          []map[string]any{{"id": "i-123", "type": "aws_instance"}},
			"observability_only": true,
		})
	}))
	defer srv.Close()

	c := NewHTTP(Config{Endpoint: srv.URL, Token: "t"})
	out, err := c.ImportDiscovery(context.Background(), ImportDiscoveryRequest{
		AccountBinding: "acct-prod",
		Cloud:          "aws",
		Region:         "eu-west-1",
		Filters:        map[string]string{"tag:env": "prod"},
		ResourceTypes:  []string{"aws_instance"},
	})
	if err != nil {
		t.Fatalf("ImportDiscovery: %v", err)
	}
	if !out.ObservabilityOnly || !strings.Contains(out.ResourcesJSON(), `"i-123"`) {
		t.Fatalf("discovery response not mapped: %+v resources=%s", out, out.ResourcesJSON())
	}
}

func TestHTTPClientImportTopologyFeeRequired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/import/topology" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body ImportTopologyRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.Intent != ImportIntentDeployableTopology || body.MigrationFeeToken != "" {
			t.Fatalf("request intent/token not mapped as expected: %+v", body)
		}
		w.WriteHeader(http.StatusPaymentRequired)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"fee_required": true,
			"fee_paid":     false,
			"fee_reason":   "deployable import requires a migration fee",
			"checkout_url": "https://checkout.example/session",
		})
	}))
	defer srv.Close()

	c := NewHTTP(Config{Endpoint: srv.URL, Token: "t"})
	out, err := c.ImportTopology(context.Background(), ImportTopologyRequest{
		AccountBinding:      "acct-prod",
		Intent:              ImportIntentDeployableTopology,
		SourceCloud:         "aws",
		SourceRegion:        "eu-west-1",
		TargetCloud:         "gcp",
		TargetRegion:        "EU West",
		SelectedResourceIDs: []string{"i-123"},
	})
	if err == nil {
		t.Fatal("expected fee-required error")
	}
	feeErr, ok := err.(*FeeRequiredError)
	if !ok {
		t.Fatalf("error = %T %v, want *FeeRequiredError", err, err)
	}
	if !out.FeeRequired || out.FeePaid || out.CheckoutURL == "" || !strings.Contains(feeErr.Error(), "migration fee") {
		t.Fatalf("fee response/error not mapped: out=%+v err=%v", out, err)
	}
}

func TestHTTPClientImportTopologyMapsJSONOutputs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"canonical_topology": map[string]any{"components": []map[string]any{{"name": "app"}}},
			"rendered_terraform": map[string]any{"resource": map[string]any{"aws_instance": map[string]any{"app": map[string]any{}}}},
			"fee_required":       false,
			"fee_paid":           true,
		})
	}))
	defer srv.Close()

	c := NewHTTP(Config{Endpoint: srv.URL, Token: "t"})
	out, err := c.ImportTopology(context.Background(), ImportTopologyRequest{
		AccountBinding: "acct-prod",
		Intent:         ImportIntentDeployableTopology,
		SourceCloud:    "aws",
		TargetCloud:    "aws",
	})
	if err != nil {
		t.Fatalf("ImportTopology: %v", err)
	}
	if !strings.Contains(out.CanonicalTopologyJSON(), `"components"`) ||
		!strings.Contains(out.RenderedTerraformJSON(), `"aws_instance"`) ||
		out.FeeRequired || !out.FeePaid {
		t.Fatalf("topology response not mapped: %+v canonical=%s rendered=%s", out, out.CanonicalTopologyJSON(), out.RenderedTerraformJSON())
	}
}
