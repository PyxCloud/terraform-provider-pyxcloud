// Package client models the PyxCloud "canonical topology" abstraction and the
// operations the Terraform provider performs against the PyxCloud platform.
//
// The canonical topology is PyxCloud's provider-independent description of an
// infrastructure stack — the same model the product wizard builds and the
// console "Compare" page prices across providers. Vocabulary here mirrors the
// backend:
//
//   - component types:    io.pyxcloud.service.pyxfile.TopologyInspector /
//     PricingRanker ("virtual-machine", "managed-database",
//     "load-balancer", "cache", "object-storage", ...)
//   - VM sizing shape:    PricingRanker.collectSpecs reads
//     properties.virtual-machine.type.{cpu,ram,architecture}
//     and properties.virtual-machine.os.osName
//   - macro-regions:      PricingRanker.ProviderCost.regionName ("EU West",
//     "US East", "Asia") — abstract, not a CSP region
//   - providers:          vibe-frontend deploymentOptions.js
//     ENABLED_LAUNCH_PROVIDERS = [aws, digitalocean, gcp]
//   - hours/month:        PricingRanker.HOURS_PER_MONTH = 730
package client

import (
	"bytes"
	"encoding/json"
)

// Canonical providers PyxCloud can deploy to (mirrors ENABLED_LAUNCH_PROVIDERS).
const (
	ProviderAWS          = "aws"
	ProviderGCP          = "gcp"
	ProviderDigitalOcean = "digitalocean"
)

const (
	ImportIntentObservability       = "observability"
	ImportIntentDeployableTopology  = "deployable_topology"
	defaultImportDiscoveryResources = "[]"
	defaultImportTopologyDocument   = "{}"
)

// HoursPerMonth turns an hourly price into a monthly estimate, matching the
// backend PricingRanker.HOURS_PER_MONTH constant.
const HoursPerMonth = 730.0

// VMType is the sizing of a virtual-machine component. Field names mirror the
// canonical shape PricingRanker reads off the topology
// (properties.virtual-machine.type.*).
type VMType struct {
	Architecture string `json:"architecture"` // e.g. "x86_64", "arm64"
	CPU          string `json:"cpu"`          // vCPU count, e.g. "2"
	RAM          string `json:"ram"`          // GiB of RAM, e.g. "4"
	OS           string `json:"osName"`       // e.g. "ubuntu", "debian"
}

// Component is one node in the canonical topology. Type is a canonical component
// type (see TopologyInspector / PricingRanker). Count is how many instances of
// this component the topology declares (1 for a single VM, N for a scale group).
type Component struct {
	Path         string  `json:"path,omitempty"`
	Name         string  `json:"name"`
	Type         string  `json:"type"`
	Count        int     `json:"count"`
	Architecture string  `json:"architecture,omitempty"`
	CPU          string  `json:"cpu,omitempty"`
	RAM          string  `json:"ram,omitempty"`
	OSName       string  `json:"os_name,omitempty"`
	Min          int     `json:"min,omitempty"`
	Max          int     `json:"max,omitempty"`
	Desired      int     `json:"desired,omitempty"`
	Health       string  `json:"health,omitempty"`
	VM           *VMType `json:"vm,omitempty"` // legacy/internal compatibility for virtual-machine* components
}

// Topology is the canonical, provider-independent stack description. Provider
// and Region pin a concrete deployment target; Components is the abstract model
// that the Compare data source can re-price against other targets.
type Topology struct {
	ID         string      `json:"id,omitempty"`
	Name       string      `json:"name"`
	Provider   string      `json:"provider"` // aws | gcp | digitalocean
	Region     string      `json:"region"`   // macro-region, e.g. "EU West"
	Components []Component `json:"components"`
}

// Candidate is a (provider, region) deployment target the Compare data source
// prices the topology against.
type Candidate struct {
	Provider string `json:"provider"`
	Region   string `json:"region"`
}

// CandidateCost is the priced result for one Candidate, mirroring
// PricingRanker.ProviderCost (provider, regionName, hourlyUsd, monthlyUsd).
type CandidateCost struct {
	Provider   string  `json:"provider"`
	Region     string  `json:"region"`
	HourlyUSD  float64 `json:"hourly_usd"`
	MonthlyUSD float64 `json:"monthly_usd"`
	Priceable  bool    `json:"priceable"` // false when no complete price match exists
}

// TranslateResult is the concrete, provider-specific terraform the backend emits
// for a canonical topology + a chosen (provider, abstract region). Terraform holds
// one rendered HCL document per canonical root node (the existing
// CspTemplateResolver output). CSPRegion is the concrete region the abstract
// Region resolved to. Mirrors TfProviderResource POST /api/translate.
type TranslateResult struct {
	Terraform []string `json:"terraform"`
	Provider  string   `json:"provider"`
	Region    string   `json:"region"`     // abstract pyx region_name
	CSPRegion string   `json:"csp_region"` // concrete, catalog-resolved
}

// ImportDiscoveryRequest asks the backend to inspect an already-bound cloud
// account. It carries a PyxCloud account binding reference only; raw cloud
// credentials must stay out of provider models and Terraform state.
type ImportDiscoveryRequest struct {
	AccountBinding string            `json:"account_binding"`
	Cloud          string            `json:"cloud,omitempty"`
	Region         string            `json:"region,omitempty"`
	Filters        map[string]string `json:"filters,omitempty"`
	ResourceTypes  []string          `json:"resource_types,omitempty"`
}

// ImportDiscoveryResponse is the backend's read-only resource inventory.
type ImportDiscoveryResponse struct {
	Resources         json.RawMessage `json:"resources,omitempty"`
	ObservabilityOnly bool            `json:"observability_only"`
}

func (r ImportDiscoveryResponse) ResourcesJSON() string {
	return rawJSONOutput(r.Resources, defaultImportDiscoveryResources)
}

// ImportTopologyRequest asks the backend to produce either observability-only
// import metadata or a deployable canonical topology. The optional fee token is
// a backend checkout/entitlement token, not cloud credentials, and callers should
// never copy it into outputs.
type ImportTopologyRequest struct {
	AccountBinding        string   `json:"account_binding"`
	Intent                string   `json:"intent"`
	SourceCloud           string   `json:"source_cloud,omitempty"`
	SourceRegion          string   `json:"source_region,omitempty"`
	TargetCloud           string   `json:"target_cloud,omitempty"`
	TargetRegion          string   `json:"target_region,omitempty"`
	SelectedResourceIDs   []string `json:"selected_resource_ids,omitempty"`
	SelectedResourceTypes []string `json:"selected_resource_types,omitempty"`
	MigrationFeeToken     string   `json:"migration_fee_token,omitempty"`
}

// ImportTopologyResponse captures backend-gated import output and fee state.
type ImportTopologyResponse struct {
	CanonicalTopology json.RawMessage `json:"canonical_topology,omitempty"`
	RenderedTerraform json.RawMessage `json:"rendered_terraform,omitempty"`
	TerraformJSON     json.RawMessage `json:"terraform_json,omitempty"`
	FeeRequired       bool            `json:"fee_required"`
	FeePaid           bool            `json:"fee_paid"`
	FeeReason         string          `json:"fee_reason,omitempty"`
	CheckoutURL       string          `json:"checkout_url,omitempty"`
}

func (r ImportTopologyResponse) CanonicalTopologyJSON() string {
	return rawJSONOutput(r.CanonicalTopology, defaultImportTopologyDocument)
}

func (r ImportTopologyResponse) RenderedTerraformJSON() string {
	if len(r.RenderedTerraform) > 0 {
		return rawJSONOutput(r.RenderedTerraform, defaultImportTopologyDocument)
	}
	return rawJSONOutput(r.TerraformJSON, defaultImportTopologyDocument)
}

// FeeRequiredError marks a backend payment gate. Providers can turn this into a
// precise diagnostic without persisting the fee token or raw credentials.
type FeeRequiredError struct {
	StatusCode  int
	ReasonText  string
	CheckoutURL string
}

func (e *FeeRequiredError) Error() string {
	if e.ReasonText != "" {
		return e.ReasonText
	}
	return "migration fee required"
}

func (e *FeeRequiredError) FeeReason() string  { return e.ReasonText }
func (e *FeeRequiredError) Checkout() string   { return e.CheckoutURL }
func (e *FeeRequiredError) FeeRequired() bool  { return true }
func (e *FeeRequiredError) BackendStatus() int { return e.StatusCode }

func rawJSONOutput(raw json.RawMessage, empty string) string {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return empty
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return empty
		}
		return s
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err == nil {
		return compact.String()
	}
	return string(raw)
}
