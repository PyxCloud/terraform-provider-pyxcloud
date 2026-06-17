package catalog

import (
	"context"
	"fmt"
	"strings"
)

// KMS is the abstract `kms` / `encryption-key` component: a managed encryption key
// — the canonical form of the per-provider scripts' aws_kms_key/_alias glue.
//
//   - AWS: aws_kms_key + aws_kms_alias.
//   - GCP: google_kms_key_ring + google_kms_crypto_key.
//   - DigitalOcean: UNSUPPORTED (no managed KMS primitive). Clean plan-time error.

// KMSSpec is the abstract encryption-key description.
type KMSSpec struct {
	Name              string
	Region            string
	Provider          string
	Description       string
	RotationDays      int  // key rotation period (AWS: enable rotation when > 0)
	DeletionWindowDays int // AWS deletion window (default 30)
}

// KMSPlan is the resolved concrete plan.
type KMSPlan struct {
	Provider           string `json:"provider"`
	CSP                string `json:"csp"`
	RegionName         string `json:"region_name"`
	CSPRegion          string `json:"csp_region"`
	Name               string `json:"name"`
	Description        string `json:"description"`
	RotationDays       int    `json:"rotation_days"`
	DeletionWindowDays int    `json:"deletion_window_days"`
	ResourceType       string `json:"resource_type"`
}

// TranslateKMS resolves a KMSSpec. DO is unsupported.
func TranslateKMS(ctx context.Context, cat RegionCatalog, spec KMSSpec) (KMSPlan, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return KMSPlan{}, fmt.Errorf("kms: name is required")
	}
	csp, ok := ProviderToCSP(spec.Provider)
	if !ok {
		return KMSPlan{}, fmt.Errorf("kms: unknown provider %q (aws | gcp | digitalocean)", spec.Provider)
	}
	if strings.EqualFold(spec.Provider, ProviderDigitalOcean) {
		return KMSPlan{}, fmt.Errorf("kms: unsupported on digitalocean (no managed KMS primitive) — " +
			"use AWS KMS / GCP Cloud KMS, or self-host a key store (hard plan-time error)")
	}
	if spec.RotationDays < 0 {
		return KMSPlan{}, fmt.Errorf("kms: rotation_days must be >= 0 (0 = no rotation)")
	}
	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return KMSPlan{}, err
	}
	dw := spec.DeletionWindowDays
	if dw <= 0 {
		dw = 30
	}
	plan := KMSPlan{
		Provider: strings.ToLower(spec.Provider), CSP: csp,
		RegionName: row.RegionName, CSPRegion: row.CSPRegion,
		Name: spec.Name, Description: spec.Description, RotationDays: spec.RotationDays,
		DeletionWindowDays: dw,
	}
	switch plan.Provider {
	case ProviderAWS:
		plan.ResourceType = "aws_kms_key"
	case ProviderGCP:
		plan.ResourceType = "google_kms_crypto_key"
	}
	return plan, nil
}

// RenderKMSHCL renders a KMSPlan. DO never reaches here.
func RenderKMSHCL(p KMSPlan) (string, error) {
	switch p.Provider {
	case ProviderAWS:
		return renderKMSAWS(p), nil
	case ProviderGCP:
		return renderKMSGCP(p), nil
	default:
		return "", fmt.Errorf("kms: render unsupported for provider %q", p.Provider)
	}
}

func renderKMSAWS(p KMSPlan) string {
	var b strings.Builder
	key := tfName(p.Name)
	fmt.Fprintf(&b, "resource \"aws_kms_key\" %q {\n", key)
	if p.Description != "" {
		fmt.Fprintf(&b, "  description             = %q\n", asciiOnly(p.Description))
	}
	fmt.Fprintf(&b, "  deletion_window_in_days = %d\n", p.DeletionWindowDays)
	fmt.Fprintf(&b, "  enable_key_rotation     = %t\n", p.RotationDays > 0)
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n\n")
	fmt.Fprintf(&b, "resource \"aws_kms_alias\" %q {\n", key)
	fmt.Fprintf(&b, "  name          = \"alias/%s\"\n", p.Name)
	fmt.Fprintf(&b, "  target_key_id = aws_kms_key.%s.key_id\n", key)
	b.WriteString("}\n")
	return b.String()
}

func renderKMSGCP(p KMSPlan) string {
	var b strings.Builder
	key := tfName(p.Name)
	fmt.Fprintf(&b, "resource \"google_kms_key_ring\" %q {\n", key)
	fmt.Fprintf(&b, "  name     = %q\n", p.Name+"-ring")
	fmt.Fprintf(&b, "  location = %q\n", p.CSPRegion)
	b.WriteString("}\n\n")
	fmt.Fprintf(&b, "resource \"google_kms_crypto_key\" %q {\n", key)
	fmt.Fprintf(&b, "  name     = %q\n", p.Name)
	fmt.Fprintf(&b, "  key_ring = google_kms_key_ring.%s.id\n", key)
	if p.RotationDays > 0 {
		fmt.Fprintf(&b, "  rotation_period = \"%ds\"\n", p.RotationDays*24*3600)
	}
	b.WriteString("}\n")
	return b.String()
}
