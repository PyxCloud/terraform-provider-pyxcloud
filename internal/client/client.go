package client

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"sync"
)

// Client is the interface the provider's resources and data sources depend on.
//
// MVP NOTE: network calls against the live PyxCloud API
// (https://passo.build) are NOT implemented yet. The concrete type below
// (StubClient) satisfies this interface in-memory so the provider compiles,
// vets, tests, and demos end-to-end without touching the network or any cloud.
// A future HTTPClient implementation will back these with real REST/GraphQL
// calls — see the TODOs on each method.
type Client interface {
	// CreateTopology persists a canonical topology and returns it with an ID assigned.
	CreateTopology(ctx context.Context, t Topology) (Topology, error)
	// GetTopology fetches a topology by ID. Returns (Topology{}, false, nil) when absent.
	GetTopology(ctx context.Context, id string) (Topology, bool, error)
	// UpdateTopology replaces the topology identified by t.ID.
	UpdateTopology(ctx context.Context, t Topology) (Topology, error)
	// DeleteTopology removes a topology by ID. Deleting an absent topology is a no-op.
	DeleteTopology(ctx context.Context, id string) error

	// Compare prices a canonical topology against each candidate (provider, region),
	// mirroring the console Compare page / backend PricingRanker.rank.
	Compare(ctx context.Context, t Topology, candidates []Candidate) ([]CandidateCost, error)

	// Translate descends a canonical topology + its chosen (provider, abstract
	// region) into concrete provider terraform, via the backend translation engine
	// (RegionResolver + CspTemplateResolver). This is the authoritative,
	// catalog-driven translation — the source of truth the resource plan reflects.
	Translate(ctx context.Context, t Topology) (TranslateResult, error)

	// ImportDiscovery inspects a backend-held account binding for importable
	// resources. It is read-only and observability-only.
	ImportDiscovery(ctx context.Context, req ImportDiscoveryRequest) (ImportDiscoveryResponse, error)
	// ImportTopology asks the backend to canonicalize selected discovered
	// resources. Deployable topology output may be gated by a migration fee.
	ImportTopology(ctx context.Context, req ImportTopologyRequest) (ImportTopologyResponse, error)

	DeployEnvironment(ctx context.Context, envID string, accountBindingID string, hclDocs []string) (map[string]string, error)
	RefreshEnvironment(ctx context.Context, envID string, accountBindingID string) (map[string]string, error)
	DestroyEnvironment(ctx context.Context, envID string, accountBindingID string) error
	FireEvent(ctx context.Context, eventType string, payload map[string]interface{}) error
}

// Config holds the provider-level connection settings.
type Config struct {
	Endpoint string // PyxCloud API base, e.g. "https://passo.build"
	Token    string // Static pre-issued bearer (PYXCLOUD_TOKEN) — tests / break-glass.

	// Machine auth (preferred): OAuth 2.1 client_credentials with the provider's
	// own confidential client, so the provider execution authenticates itself with
	// no human login. When ClientID+ClientSecret+TokenURL are set they take
	// precedence over a static Token.
	ClientID     string
	ClientSecret string
	TokenURL     string // passobuild realm token endpoint
}

// DefaultEndpoint is the PyxCloud API base used when none is configured.
const DefaultEndpoint = "https://passo.build"

// StubClient is the MVP in-memory implementation of Client. It stores topologies
// in a map and computes a deterministic synthetic price so the Compare data
// source produces stable, demonstrable output without network access.
type StubClient struct {
	cfg Config

	mu    sync.Mutex
	store map[string]Topology
	seq   int
}

// NewStub returns a Client backed by in-memory storage and synthetic pricing.
func NewStub(cfg Config) *StubClient {
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultEndpoint
	}
	return &StubClient{cfg: cfg, store: map[string]Topology{}}
}

var _ Client = (*StubClient)(nil)

func (c *StubClient) CreateTopology(_ context.Context, t Topology) (Topology, error) {
	// TODO(pd-FEAT-TF-PROVIDER): POST {endpoint}/api/topology with bearer auth.
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	t.ID = fmt.Sprintf("top-%d", c.seq)
	c.store[t.ID] = t
	return t, nil
}

func (c *StubClient) GetTopology(_ context.Context, id string) (Topology, bool, error) {
	// TODO(pd-FEAT-TF-PROVIDER): GET {endpoint}/api/topology/{id}.
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.store[id]
	return t, ok, nil
}

func (c *StubClient) UpdateTopology(_ context.Context, t Topology) (Topology, error) {
	// TODO(pd-FEAT-TF-PROVIDER): PUT {endpoint}/api/topology/{id}.
	c.mu.Lock()
	defer c.mu.Unlock()
	if t.ID == "" {
		return Topology{}, fmt.Errorf("update requires a topology ID")
	}
	c.store[t.ID] = t
	return t, nil
}

func (c *StubClient) DeleteTopology(_ context.Context, id string) error {
	// TODO(pd-FEAT-TF-PROVIDER): DELETE {endpoint}/api/topology/{id}.
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.store, id)
	return nil
}

// Compare computes a synthetic per-candidate cost. Real pricing will call the
// backend get_comparison_prices stored procedure path via PricingRanker.
//
// TODO(pd-FEAT-TF-PROVIDER): POST {endpoint}/api/compare with the canonical
// topology + candidates and return the live ranked costs.
func (c *StubClient) Compare(_ context.Context, t Topology, candidates []Candidate) ([]CandidateCost, error) {
	out := make([]CandidateCost, 0, len(candidates))
	for _, cand := range candidates {
		hourly := syntheticHourly(t, cand)
		out = append(out, CandidateCost{
			Provider:   cand.Provider,
			Region:     cand.Region,
			HourlyUSD:  hourly,
			MonthlyUSD: hourly * HoursPerMonth,
			Priceable:  hourly > 0,
		})
	}
	// Cheapest first, matching PricingRanker's ordering.
	sort.SliceStable(out, func(i, j int) bool { return out[i].HourlyUSD < out[j].HourlyUSD })
	return out, nil
}

// Translate is stub-only: the in-memory client has no backend translation engine,
// so it returns an explanatory placeholder per component. The live HTTPClient
// returns the real catalog-driven terraform. Stub output is never applied.
//
// TODO(pd-FEAT-TF-PROVIDER): n/a — translation is inherently a backend concern.
func (c *StubClient) Translate(_ context.Context, t Topology) (TranslateResult, error) {
	tf := make([]string, 0, len(t.Components))
	for _, comp := range t.Components {
		tf = append(tf, fmt.Sprintf(
			"# stub: no live backend translation for %q (%s) on %s/%s — set a token to use the live API\n",
			comp.Name, comp.Type, t.Provider, t.Region))
	}
	return TranslateResult{Terraform: tf, Provider: t.Provider, Region: t.Region}, nil
}

func (c *StubClient) ImportDiscovery(_ context.Context, req ImportDiscoveryRequest) (ImportDiscoveryResponse, error) {
	resources := fmt.Sprintf(`[{"account_binding":%q,"cloud":%q,"region":%q,"stub":true}]`,
		req.AccountBinding, req.Cloud, req.Region)
	return ImportDiscoveryResponse{
		Resources:         []byte(resources),
		ObservabilityOnly: true,
	}, nil
}

func (c *StubClient) ImportTopology(_ context.Context, req ImportTopologyRequest) (ImportTopologyResponse, error) {
	canonical := fmt.Sprintf(`{"account_binding":%q,"intent":%q,"selected_resource_ids":%s,"stub":true}`,
		req.AccountBinding, req.Intent, stringListJSON(req.SelectedResourceIDs))
	return ImportTopologyResponse{
		CanonicalTopology: []byte(canonical),
		RenderedTerraform: []byte(`{}`),
		FeeRequired:       false,
		FeePaid:           req.Intent == ImportIntentDeployableTopology && req.MigrationFeeToken != "",
	}, nil
}

func (c *StubClient) DeployEnvironment(ctx context.Context, envID string, accountBindingID string, hclDocs []string) (map[string]string, error) {
	return map[string]string{
		"status": "success",
		"url":    "https://environment-deployed.pyxcloud.io",
	}, nil
}

func (c *StubClient) RefreshEnvironment(ctx context.Context, envID string, accountBindingID string) (map[string]string, error) {
	return map[string]string{
		"status": "success",
		"url":    "https://environment-deployed.pyxcloud.io",
	}, nil
}

func (c *StubClient) DestroyEnvironment(ctx context.Context, envID string, accountBindingID string) error {
	return nil
}

func (c *StubClient) FireEvent(ctx context.Context, eventType string, payload map[string]interface{}) error {
	// Stub implementation: do-op (could log or save in-memory if needed)
	return nil
}


func stringListJSON(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
	b, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// syntheticHourly produces a deterministic, plausible hourly price from the
// topology sizing and a per-(provider,region) multiplier. Stub-only.
func syntheticHourly(t Topology, cand Candidate) float64 {
	var base float64
	for _, comp := range t.Components {
		n := comp.Count
		if n <= 0 {
			n = 1
		}
		switch comp.Type {
		case "virtual-machine", "virtual-machine-scale-group":
			cpu, ram := 1.0, 1.0
			if comp.VM != nil {
				if v, err := strconv.ParseFloat(comp.VM.CPU, 64); err == nil && v > 0 {
					cpu = v
				}
				if v, err := strconv.ParseFloat(comp.VM.RAM, 64); err == nil && v > 0 {
					ram = v
				}
			}
			base += float64(n) * (cpu*0.018 + ram*0.006)
		case "managed-database":
			base += float64(n) * 0.090
		case "load-balancer":
			base += float64(n) * 0.025
		case "cache":
			base += float64(n) * 0.040
		case "object-storage", "blob-storage":
			base += float64(n) * 0.005
		default:
			base += float64(n) * 0.010
		}
	}
	return round4(base * providerRegionMultiplier(cand))
}

// providerRegionMultiplier yields a stable in-range multiplier (~0.85–1.20) per
// (provider, region) so candidates rank differently and reproducibly.
func providerRegionMultiplier(cand Candidate) float64 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(cand.Provider + "|" + cand.Region))
	return 0.85 + float64(h.Sum32()%36)/100.0 // 0.85 .. 1.20
}

func round4(f float64) float64 {
	return float64(int64(f*10000+0.5)) / 10000
}
