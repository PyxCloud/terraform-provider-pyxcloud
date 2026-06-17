package catalog

import (
	"fmt"
	"strings"
)

// KMSKeySpec is one customer-managed encryption key with an optional alias.
type KMSKeySpec struct {
	Alias          string // human alias, e.g. "vault-unseal" (→ alias/<alias> on AWS)
	Description    string
	EnableRotation bool // annual key rotation
}

// KMSSpec is the canonical key-management component: a set of customer-managed
// keys. AWS-complete (KMS); other providers map what they cleanly can or surface
// a hard "unsupported" error (SPEC §1).
type KMSSpec struct {
	Name     string
	Provider string
	Keys     []KMSKeySpec
}

// KMSPlan is the deterministic concrete translation.
type KMSPlan struct {
	Provider     string       `json:"provider"`
	CSP          string       `json:"csp"`
	Name         string       `json:"name"`
	Keys         []KMSKeySpec `json:"keys"`
	ResourceType string       `json:"resource_type"`
}

// TranslateKMS resolves a KMSSpec into a concrete plan. Global (no region lookup).
// AWS is fully supported; other providers are a hard "unsupported" error.
func TranslateKMS(spec KMSSpec) (KMSPlan, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return KMSPlan{}, fmt.Errorf("kms: name is required")
	}
	csp, ok := ProviderToCSP(spec.Provider)
	if !ok {
		return KMSPlan{}, fmt.Errorf("kms: unknown provider %q", spec.Provider)
	}
	if len(spec.Keys) == 0 {
		return KMSPlan{}, fmt.Errorf("kms: declare at least one key")
	}
	for _, k := range spec.Keys {
		if strings.TrimSpace(k.Alias) == "" {
			return KMSPlan{}, fmt.Errorf("kms: each key needs an alias")
		}
	}
	provider := strings.ToLower(strings.TrimSpace(spec.Provider))
	plan := KMSPlan{Provider: provider, CSP: csp, Name: spec.Name, Keys: spec.Keys}
	switch provider {
	case ProviderAWS:
		plan.ResourceType = "aws_kms_key"
	default:
		return KMSPlan{}, fmt.Errorf("kms: unsupported on provider %q (supported: aws KMS). "+
			"Hard plan-time error, never an invented resource", provider)
	}
	return plan, nil
}

// RenderKMSHCL renders a resolved KMSPlan.
func RenderKMSHCL(plan KMSPlan) (string, error) {
	if plan.Provider != ProviderAWS {
		return "", fmt.Errorf("kms: no renderer for provider %q", plan.Provider)
	}
	var b strings.Builder
	for _, k := range plan.Keys {
		rn := tfName(plan.Name + "-" + k.Alias)
		fmt.Fprintf(&b, "resource \"aws_kms_key\" %q {\n", rn)
		if k.Description != "" {
			fmt.Fprintf(&b, "  description         = %q\n", k.Description)
		}
		fmt.Fprintf(&b, "  enable_key_rotation = %t\n", k.EnableRotation)
		fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
		b.WriteString("}\n\n")

		fmt.Fprintf(&b, "resource \"aws_kms_alias\" %q {\n", rn)
		fmt.Fprintf(&b, "  name          = %q\n", "alias/"+k.Alias)
		fmt.Fprintf(&b, "  target_key_id = aws_kms_key.%s.key_id\n", rn)
		b.WriteString("}\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n", nil
}
