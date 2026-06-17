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

// Canonical providers PyxCloud can deploy to (mirrors ENABLED_LAUNCH_PROVIDERS).
const (
	ProviderAWS          = "aws"
	ProviderGCP          = "gcp"
	ProviderDigitalOcean = "digitalocean"
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
	Name  string  `json:"name"`
	Type  string  `json:"type"`
	Count int     `json:"count"`
	VM    *VMType `json:"vm,omitempty"` // populated for virtual-machine* components
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
