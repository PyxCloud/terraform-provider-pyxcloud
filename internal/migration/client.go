// Package migration is the PyxCloud provider-side OPAQUE CLIENT for
// provider→provider migration (MIGRATION.md). It ferries a sealed (encrypted)
// execution bundle from the trusted backend to a confidential runtime and reports
// coarse status back into Terraform state.
//
// NON-NEGOTIABLE: this package contains ZERO migration logic. The sequencing of
// CRIU / rsync / DB dump-restore / blob sync / secret re-seal / queue drain / DNS
// cutover is PyxCloud's industrial secret and lives ONLY on the backend, inside
// the sealed bundle. The provider:
//
//  1. generates a per-run ephemeral X25519 keypair (private key in memory only),
//  2. POSTs the plan request to the backend with the ephemeral public key +
//     attestation evidence,
//  3. receives a sealed opaque bundle (ciphertext bytes it never parses/inspects),
//  4. hands it to the confidential runtime, polls coarse status, surfaces
//     phase/%/verdict + rollback,
//  5. zeroizes the ephemeral private key on completion.
//
// The provider/runner only ever hold ciphertext + coarse status. Plaintext (the
// bundle, the cloud creds, the content key) only ever exists inside the runtime's
// sealed boundary.
package migration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/migration/runtime"
)

// DefaultEndpoint mirrors the provider client default.
const DefaultEndpoint = "https://passo.build"

// planPath is the backend plan endpoint (MIGRATION.md §2.1).
const planPath = "/api/migration/plan"

// Config configures the opaque client. Token is the PYXCLOUD_TOKEN bearer; it
// mirrors the existing provider client's auth shape.
type Config struct {
	Endpoint   string
	Token      string
	HTTPClient *http.Client
}

// Client is the provider-side opaque migration client.
type Client struct {
	cfg  Config
	http *http.Client
}

// NewClient builds an opaque migration client.
func NewClient(cfg Config) *Client {
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultEndpoint
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{cfg: cfg, http: hc}
}

// PlanRequest is the body of POST /api/migration/plan (MIGRATION.md §2.1). It
// carries the source topology+provider, the target provider, the macro place, the
// ephemeral public key, and the runtime's attestation evidence. NO migration
// steps — the backend computes those and seals them.
type PlanRequest struct {
	Place           string          `json:"place"`
	SourceProvider  string          `json:"sourceProvider"`
	TargetProvider  string          `json:"targetProvider"`
	SourceTopology  json.RawMessage `json:"sourceTopology"`
	EphemeralPubKey string          `json:"ephemeralPubKey"` // base64 X25519 public key
	Attestation     AttestationWire `json:"attestationEvidence"`
	DryRun          bool            `json:"dryRun"`
}

// AttestationWire is the on-the-wire attestation evidence forwarded to the BE.
type AttestationWire struct {
	Substrate   string `json:"substrate"`
	Format      string `json:"format"`
	Measurement string `json:"measurement"` // base64
	Nonce       string `json:"nonce"`       // base64
	Document    string `json:"document"`    // base64 signed attestation document
}

// PlanResponse is the backend response: the sealed opaque bundle + sealed scoped
// creds + the run id to poll. The provider treats Bundle/Creds as ciphertext.
type PlanResponse struct {
	RunID  string     `json:"runId"`
	Bundle SealedWire `json:"bundle"`
	Creds  SealedWire `json:"creds"`
	// ExpectedMeasurement is the measurement the bundle was sealed to (the BE
	// echoes what it bound the key release to). Opaque to the provider.
	ExpectedMeasurement string `json:"expectedMeasurement"` // base64
}

// SealedWire is the wire form of a Sealed payload (base64 KEM pubkey + ciphertext).
type SealedWire struct {
	KEMPub     string `json:"kemPub"`
	Ciphertext string `json:"ciphertext"`
}

func (s SealedWire) toSealed() (Sealed, error) {
	pub, err := base64.StdEncoding.DecodeString(s.KEMPub)
	if err != nil || len(pub) != 32 {
		return Sealed{}, fmt.Errorf("invalid sealed KEM public key")
	}
	ct, err := base64.StdEncoding.DecodeString(s.Ciphertext)
	if err != nil {
		return Sealed{}, fmt.Errorf("invalid sealed ciphertext")
	}
	var out Sealed
	copy(out.KEMPub[:], pub)
	out.Ciphertext = ct
	return out, nil
}

// PlanInput is what the caller (the resource) supplies to drive a migration plan.
type PlanInput struct {
	Place          string
	SourceProvider string
	TargetProvider string
	// SourceTopology is the canonical topology JSON. Opaque pass-through; the
	// provider does not interpret it for migration purposes.
	SourceTopology json.RawMessage
	DryRun         bool
}

// RequestPlan performs the plan handshake: it forwards the ephemeral public key +
// attestation evidence and returns the sealed opaque bundle. It never inspects
// the bundle.
func (c *Client) RequestPlan(ctx context.Context, in PlanInput, ephPub [32]byte, ev runtime.Evidence) (PlanResponse, error) {
	body := PlanRequest{
		Place:           in.Place,
		SourceProvider:  in.SourceProvider,
		TargetProvider:  in.TargetProvider,
		SourceTopology:  in.SourceTopology,
		EphemeralPubKey: base64.StdEncoding.EncodeToString(ephPub[:]),
		DryRun:          in.DryRun,
		Attestation: AttestationWire{
			Substrate:   string(ev.Substrate),
			Format:      ev.Format,
			Measurement: base64.StdEncoding.EncodeToString(ev.Measurement),
			Nonce:       base64.StdEncoding.EncodeToString(ev.Nonce),
			Document:    base64.StdEncoding.EncodeToString(ev.Document),
		},
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return PlanResponse{}, fmt.Errorf("migration plan: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Endpoint+planPath, bytes.NewReader(buf))
	if err != nil {
		return PlanResponse{}, fmt.Errorf("migration plan: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return PlanResponse{}, fmt.Errorf("migration plan: %w", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode != http.StatusOK {
		return PlanResponse{}, fmt.Errorf("migration plan: backend returned %d: %s", resp.StatusCode, string(rb))
	}
	var pr PlanResponse
	if err := json.Unmarshal(rb, &pr); err != nil {
		return PlanResponse{}, fmt.Errorf("migration plan: decode response: %w", err)
	}
	return pr, nil
}

// SealedInputsFrom adapts a PlanResponse into the runtime's SealedInputs. The
// bundle/creds stay ciphertext; this only base64-decodes the transport wrapper.
func SealedInputsFrom(pr PlanResponse, dryRun bool) (runtime.SealedInputs, error) {
	bundle, err := pr.Bundle.toSealed()
	if err != nil {
		return runtime.SealedInputs{}, fmt.Errorf("bundle: %w", err)
	}
	var creds Sealed
	if pr.Creds.Ciphertext != "" {
		creds, err = pr.Creds.toSealed()
		if err != nil {
			return runtime.SealedInputs{}, fmt.Errorf("creds: %w", err)
		}
	}
	meas, err := base64.StdEncoding.DecodeString(pr.ExpectedMeasurement)
	if err != nil {
		return runtime.SealedInputs{}, fmt.Errorf("expected measurement: %w", err)
	}
	return runtime.SealedInputs{
		Bundle:              runtime.Sealed{KEMPub: bundle.KEMPub, Ciphertext: bundle.Ciphertext},
		Creds:               runtime.Sealed{KEMPub: creds.KEMPub, Ciphertext: creds.Ciphertext},
		ExpectedMeasurement: meas,
		DryRun:              dryRun,
	}, nil
}
