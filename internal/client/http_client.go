package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// HTTPClient is the live implementation of Client. It backs the provider's
// topology CRUD + Compare against the PyxCloud backend REST surface
// (TfProviderResource: /api/topology, /api/compare), authenticating with the
// SSO/OAuth bearer token. It replaces StubClient once a token is configured.
//
// Shape mapping (the crux): the provider models component properties flatly
// (Component{architecture,cpu,ram,os_name,...}), while the backend canonical
// topology the engine reads is the nested node shape
// (properties.virtual-machine.type.{architecture,cpu,ram} + .os.osName, the same
// shape PricingRanker.collectSpecs and CspTemplateResolver consume). This client
// maps Components → canonical nodes on the way out and back on the way in, so the
// backend prices/translates correctly and Read reconstructs the resource state.
type HTTPClient struct {
	cfg   Config
	http  *http.Client
	creds tokenSource
}

// NewHTTP returns a Client that talks to the live PyxCloud API at cfg.Endpoint.
// It authenticates via OAuth2.1 client_credentials (the provider's own client; no
// human login) when ClientID+ClientSecret+TokenURL are set, else with the static
// cfg.Token.
func NewHTTP(cfg Config) *HTTPClient {
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultEndpoint
	}
	var creds tokenSource
	if cfg.ClientID != "" && cfg.ClientSecret != "" && cfg.TokenURL != "" {
		creds = newClientCredentialsSource(cfg.TokenURL, cfg.ClientID, cfg.ClientSecret)
	} else if cfg.Token != "" {
		creds = staticToken(cfg.Token)
	}
	return &HTTPClient{
		cfg:   cfg,
		http:  &http.Client{Timeout: 30 * time.Second},
		creds: creds,
	}
}

var _ Client = (*HTTPClient)(nil)

func (c *HTTPClient) do(ctx context.Context, method, path string, body any) (*http.Response, []byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, nil, fmt.Errorf("encoding request: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	url := strings.TrimRight(c.cfg.Endpoint, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.creds != nil {
		tok, terr := c.creds.token(ctx)
		if terr != nil {
			return nil, nil, fmt.Errorf("obtaining access token: %w", terr)
		}
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, nil, err
	}
	return resp, data, nil
}

// topologyWire is the JSON envelope exchanged with /api/topology. Components are
// carried as canonical nodes under "canonical".
type topologyWire struct {
	ID        string           `json:"id,omitempty"`
	Name      string           `json:"name"`
	Provider  string           `json:"provider"`
	Region    string           `json:"region"`
	Canonical []map[string]any `json:"canonical"`
}

func (c *HTTPClient) toWire(t Topology) topologyWire {
	return topologyWire{
		ID:        t.ID,
		Name:      t.Name,
		Provider:  t.Provider,
		Region:    t.Region,
		Canonical: componentsToCanonical(t.Components),
	}
}

func (w topologyWire) toTopology() Topology {
	return Topology{
		ID:         w.ID,
		Name:       w.Name,
		Provider:   w.Provider,
		Region:     w.Region,
		Components: canonicalToComponents(w.Canonical),
	}
}

func (c *HTTPClient) CreateTopology(ctx context.Context, t Topology) (Topology, error) {
	resp, data, err := c.do(ctx, http.MethodPost, "/api/topology", c.toWire(t))
	if err != nil {
		return Topology{}, err
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return Topology{}, apiError("create topology", resp.StatusCode, data)
	}
	var w topologyWire
	if err := json.Unmarshal(data, &w); err != nil {
		return Topology{}, fmt.Errorf("decoding create response: %w", err)
	}
	// Preserve the caller's components verbatim; only adopt the server id.
	t.ID = w.ID
	return t, nil
}

func (c *HTTPClient) GetTopology(ctx context.Context, id string) (Topology, bool, error) {
	resp, data, err := c.do(ctx, http.MethodGet, "/api/topology/"+id, nil)
	if err != nil {
		return Topology{}, false, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return Topology{}, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return Topology{}, false, apiError("get topology", resp.StatusCode, data)
	}
	var w topologyWire
	if err := json.Unmarshal(data, &w); err != nil {
		return Topology{}, false, fmt.Errorf("decoding get response: %w", err)
	}
	return w.toTopology(), true, nil
}

func (c *HTTPClient) UpdateTopology(ctx context.Context, t Topology) (Topology, error) {
	if t.ID == "" {
		return Topology{}, fmt.Errorf("update requires a topology ID")
	}
	resp, data, err := c.do(ctx, http.MethodPut, "/api/topology/"+t.ID, c.toWire(t))
	if err != nil {
		return Topology{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Topology{}, apiError("update topology", resp.StatusCode, data)
	}
	return t, nil
}

func (c *HTTPClient) DeleteTopology(ctx context.Context, id string) error {
	resp, data, err := c.do(ctx, http.MethodDelete, "/api/topology/"+id, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK &&
		resp.StatusCode != http.StatusNotFound {
		return apiError("delete topology", resp.StatusCode, data)
	}
	return nil
}

// compareRequest / compareResponse mirror TfProviderResource POST /api/compare.
type compareRequest struct {
	Canonical  []map[string]any `json:"canonical"`
	Candidates []Candidate      `json:"candidates"`
}

type compareResponse struct {
	Results []CandidateCost `json:"results"`
}

func (c *HTTPClient) Compare(ctx context.Context, t Topology, candidates []Candidate) ([]CandidateCost, error) {
	body := compareRequest{
		Canonical:  componentsToCanonical(t.Components),
		Candidates: candidates,
	}
	resp, data, err := c.do(ctx, http.MethodPost, "/api/compare", body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, apiError("compare", resp.StatusCode, data)
	}
	var out compareResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decoding compare response: %w", err)
	}
	return out.Results, nil
}

// translateRequest mirrors TfProviderResource POST /api/translate.
type translateRequest struct {
	Canonical []map[string]any `json:"canonical"`
	Provider  string           `json:"provider"`
	Region    string           `json:"region"`
}

func (c *HTTPClient) Translate(ctx context.Context, t Topology) (TranslateResult, error) {
	body := translateRequest{
		Canonical: componentsToCanonical(t.Components),
		Provider:  t.Provider,
		Region:    t.Region,
	}
	resp, data, err := c.do(ctx, http.MethodPost, "/api/translate", body)
	if err != nil {
		return TranslateResult{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return TranslateResult{}, apiError("translate", resp.StatusCode, data)
	}
	var out TranslateResult
	if err := json.Unmarshal(data, &out); err != nil {
		return TranslateResult{}, fmt.Errorf("decoding translate response: %w", err)
	}
	return out, nil
}

func (c *HTTPClient) ImportDiscovery(ctx context.Context, req ImportDiscoveryRequest) (ImportDiscoveryResponse, error) {
	resp, data, err := c.do(ctx, http.MethodPost, "/api/import/discovery", req)
	if err != nil {
		return ImportDiscoveryResponse{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return ImportDiscoveryResponse{}, apiError("import discovery", resp.StatusCode, data)
	}
	var out ImportDiscoveryResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return ImportDiscoveryResponse{}, fmt.Errorf("decoding import discovery response: %w", err)
	}
	return out, nil
}

func (c *HTTPClient) ImportTopology(ctx context.Context, req ImportTopologyRequest) (ImportTopologyResponse, error) {
	resp, data, err := c.do(ctx, http.MethodPost, "/api/import/topology", req)
	if err != nil {
		return ImportTopologyResponse{}, err
	}
	var out ImportTopologyResponse
	if len(data) > 0 {
		if err := json.Unmarshal(data, &out); err != nil {
			return ImportTopologyResponse{}, fmt.Errorf("decoding import topology response: %w", err)
		}
	}
	if resp.StatusCode == http.StatusPaymentRequired {
		return out, newFeeRequiredError(resp.StatusCode, out)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return ImportTopologyResponse{}, apiError("import topology", resp.StatusCode, data)
	}
	if out.FeeRequired && !out.FeePaid {
		return out, newFeeRequiredError(resp.StatusCode, out)
	}
	return out, nil
}

func (c *HTTPClient) DeployEnvironment(ctx context.Context, envID string, accountBindingID string, hclDocs []string) (map[string]string, error) {
	reqBody := map[string]any{
		"accountBindingId": accountBindingID,
		"hcl":              hclDocs,
	}
	resp, data, err := c.do(ctx, http.MethodPost, "/api/environments/"+envID+"/deploy", reqBody)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, apiError("deploy environment", resp.StatusCode, data)
	}
	var out map[string]string
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decoding deploy response: %w", err)
	}
	return out, nil
}

func (c *HTTPClient) RefreshEnvironment(ctx context.Context, envID string, accountBindingID string) (map[string]string, error) {
	reqBody := map[string]any{
		"accountBindingId": accountBindingID,
	}
	resp, data, err := c.do(ctx, http.MethodPost, "/api/environments/"+envID+"/refresh", reqBody)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, apiError("refresh environment", resp.StatusCode, data)
	}
	var out map[string]string
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decoding refresh response: %w", err)
	}
	return out, nil
}

func (c *HTTPClient) DestroyEnvironment(ctx context.Context, envID string, accountBindingID string) error {
	reqBody := map[string]any{
		"accountBindingId": accountBindingID,
	}
	resp, data, err := c.do(ctx, http.MethodPost, "/api/environments/"+envID+"/destroy", reqBody)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return apiError("destroy environment", resp.StatusCode, data)
	}
	return nil
}

func (c *HTTPClient) FireEvent(ctx context.Context, eventType string, payload map[string]interface{}) error {
	reqBody := map[string]any{
		"type":    eventType,
		"payload": payload,
	}
	resp, data, err := c.do(ctx, http.MethodPost, "/api/events", reqBody)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		return apiError("fire event", resp.StatusCode, data)
	}
	return nil
}


func newFeeRequiredError(status int, out ImportTopologyResponse) *FeeRequiredError {
	reason := out.FeeReason
	if reason == "" {
		reason = "deployable import requires a migration fee"
	}
	return &FeeRequiredError{
		StatusCode:  status,
		ReasonText:  reason,
		CheckoutURL: out.CheckoutURL,
	}
}

func apiError(op string, status int, body []byte) error {
	msg := strings.TrimSpace(string(body))
	// Surface the backend's {"error":"..."} message when present.
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		msg = e.Error
	}
	if len(msg) > 500 {
		msg = msg[:500]
	}
	return fmt.Errorf("%s: backend returned %d: %s", op, status, msg)
}

// componentsToCanonical maps the provider's flat Component model into the
// backend canonical node shape (properties.virtual-machine.type/os) the engine
// reads. Non-VM components carry an empty properties object.
func componentsToCanonical(comps []Component) []map[string]any {
	out := make([]map[string]any, 0, len(comps))
	for _, comp := range comps {
		node := map[string]any{
			"name": comp.Name,
			"type": comp.Type,
		}
		if comp.Path != "" {
			node["path"] = comp.Path
		}
		if comp.Count > 0 {
			node["count"] = comp.Count
		}
		props := map[string]any{}
		vm := comp.VM
		if vm == nil && (comp.Architecture != "" || comp.CPU != "" || comp.RAM != "" || comp.OSName != "") {
			vm = &VMType{
				Architecture: comp.Architecture,
				CPU:          comp.CPU,
				RAM:          comp.RAM,
				OS:           comp.OSName,
			}
		}
		if vm != nil {
			props["virtual-machine"] = map[string]any{
				"type": map[string]any{
					"architecture": vm.Architecture,
					"cpu":          vm.CPU,
					"ram":          vm.RAM,
				},
				"os": map[string]any{
					"osName": vm.OS,
				},
			}
		}
		node["properties"] = props
		out = append(out, node)
	}
	return out
}

// canonicalToComponents is the inverse: reconstruct the provider's Component
// model from stored canonical nodes (used by Read).
func canonicalToComponents(nodes []map[string]any) []Component {
	out := make([]Component, 0, len(nodes))
	for _, n := range nodes {
		comp := Component{
			Path: asString(n["path"]),
			Name: asString(n["name"]),
			Type: asString(n["type"]),
		}
		comp.Count = asInt(n["count"])
		if props, ok := n["properties"].(map[string]any); ok {
			if vmAny, ok := props["virtual-machine"].(map[string]any); ok {
				vm := &VMType{}
				if t, ok := vmAny["type"].(map[string]any); ok {
					vm.Architecture = asString(t["architecture"])
					vm.CPU = asString(t["cpu"])
					vm.RAM = asString(t["ram"])
				}
				if osm, ok := vmAny["os"].(map[string]any); ok {
					vm.OS = asString(osm["osName"])
				}
				comp.VM = vm
				comp.Architecture = vm.Architecture
				comp.CPU = vm.CPU
				comp.RAM = vm.RAM
				comp.OSName = vm.OS
			}
		}
		out = append(out, comp)
	}
	return out
}

func asString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case float64:
		return strconv.FormatFloat(s, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(s)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", s)
	}
}

func asInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		i, _ := strconv.Atoi(n)
		return i
	default:
		return 0
	}
}
