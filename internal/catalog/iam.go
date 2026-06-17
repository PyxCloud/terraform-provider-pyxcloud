package catalog

import (
	"fmt"
	"strings"
)

// IAMPolicyDoc is one inline policy attached to the role: a name + a provider
// policy DOCUMENT. For AWS the document is an IAM policy JSON string (the form
// our repos already use); it is emitted verbatim, so the canonical model does not
// try to re-abstract AWS IAM's statement grammar.
type IAMPolicyDoc struct {
	Name     string // policy name, unique within the role
	Document string // AWS IAM policy JSON (verbatim)
}

// IAMSpec is the canonical description of an identity/role to attach to compute:
// a role with a trust (assume) principal, inline and/or managed policies, and an
// optional instance-profile (so a virtual-machine can reference it by name via
// VMSpec.InstanceProfile). Provider-neutral surface; AWS is fully supported,
// other providers map what they cleanly can (GCP → service account) or surface a
// hard "unsupported" error — never an invented resource (SPEC §1).
type IAMSpec struct {
	Name     string // role / identity name, e.g. "keycloak"
	Provider string // aws | gcp | digitalocean | ...

	// AssumeService is the trust principal — the service allowed to assume the
	// role. Defaults to the compute service for the provider (AWS ec2.amazonaws.com)
	// when empty, since the common case is "a role a VM/ASG assumes".
	AssumeService string

	InlinePolicies    []IAMPolicyDoc // inline policy documents (AWS JSON)
	ManagedPolicyARNs []string       // managed policy ARNs/ids to attach
	InstanceProfile   bool           // emit an instance-profile wrapping the role
}

// IAMPlan is the deterministic concrete translation of an IAMSpec.
type IAMPlan struct {
	Provider            string         `json:"provider"`
	CSP                 string         `json:"csp"`
	RoleName            string         `json:"role_name"`
	AssumeService       string         `json:"assume_service"`
	InlinePolicies      []IAMPolicyDoc `json:"inline_policies"`
	ManagedPolicyARNs   []string       `json:"managed_policy_arns"`
	InstanceProfile     bool           `json:"instance_profile"`
	InstanceProfileName string         `json:"instance_profile_name"`
	ResourceType        string         `json:"resource_type"` // top resource, e.g. aws_iam_role
}

// defaultAssumeService is the compute trust principal per provider.
var defaultAssumeService = map[string]string{
	ProviderAWS: "ec2.amazonaws.com",
}

// TranslateIAM resolves an IAMSpec into a concrete IAMPlan. IAM is global (no
// region/SKU catalog lookup). AWS is fully supported; GCP maps to a service
// account (managed/inline AWS policies do NOT map to GCP and are a hard error to
// avoid a silent drop); other providers are a hard "unsupported" error.
func TranslateIAM(spec IAMSpec) (IAMPlan, error) {
	name := strings.TrimSpace(spec.Name)
	if name == "" {
		return IAMPlan{}, fmt.Errorf("iam: name is required")
	}
	if strings.TrimSpace(spec.Provider) == "" {
		return IAMPlan{}, fmt.Errorf("iam: provider is required")
	}
	csp, ok := ProviderToCSP(spec.Provider)
	if !ok {
		return IAMPlan{}, fmt.Errorf("iam: unknown provider %q", spec.Provider)
	}
	provider := strings.ToLower(strings.TrimSpace(spec.Provider))

	assume := strings.TrimSpace(spec.AssumeService)
	if assume == "" {
		assume = defaultAssumeService[provider]
	}

	for _, p := range spec.InlinePolicies {
		if strings.TrimSpace(p.Name) == "" || strings.TrimSpace(p.Document) == "" {
			return IAMPlan{}, fmt.Errorf("iam: inline policy needs both name and document")
		}
	}

	plan := IAMPlan{
		Provider:          provider,
		CSP:               csp,
		RoleName:          name,
		AssumeService:     assume,
		InlinePolicies:    spec.InlinePolicies,
		ManagedPolicyARNs: spec.ManagedPolicyARNs,
		InstanceProfile:   spec.InstanceProfile,
	}

	switch provider {
	case ProviderAWS:
		plan.ResourceType = "aws_iam_role"
		if spec.InstanceProfile {
			plan.InstanceProfileName = name
		}
	case ProviderGCP:
		// GCP uses service accounts + project IAM bindings; AWS-style inline/managed
		// policy documents do not translate. Refuse rather than silently drop them.
		if len(spec.InlinePolicies) > 0 || len(spec.ManagedPolicyARNs) > 0 {
			return IAMPlan{}, fmt.Errorf("iam: AWS-style inline/managed policies do not map to GCP " +
				"(use GCP role bindings); declare a policy-free service account or target AWS")
		}
		// GCP attaches a service account to a VM by email; wiring the VM↔SA link is a
		// GCP follow-up, so we emit the SA but leave InstanceProfileName unset here.
		plan.ResourceType = "google_service_account"
	default:
		return IAMPlan{}, fmt.Errorf("iam: unsupported on provider %q (supported: aws; gcp service-account only). "+
			"This is a hard plan-time error, never an invented resource", provider)
	}
	return plan, nil
}

// RenderIAMHCL renders a resolved IAMPlan into concrete provider terraform.
func RenderIAMHCL(plan IAMPlan) (string, error) {
	switch plan.Provider {
	case ProviderAWS:
		return renderIAMAWS(plan), nil
	case ProviderGCP:
		return renderIAMGCP(plan), nil
	default:
		return "", fmt.Errorf("iam: no renderer for provider %q", plan.Provider)
	}
}

func renderIAMAWS(p IAMPlan) string {
	var b strings.Builder
	role := tfName(p.RoleName)

	// Trust policy: allow the compute service to assume the role.
	assume := fmt.Sprintf(`jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Action    = "sts:AssumeRole"
      Effect    = "Allow"
      Principal = { Service = %q }
    }]
  })`, p.AssumeService)

	fmt.Fprintf(&b, "resource \"aws_iam_role\" %q {\n", role)
	fmt.Fprintf(&b, "  name               = %q\n", p.RoleName)
	fmt.Fprintf(&b, "  assume_role_policy = %s\n", assume)
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n\n")

	for _, pol := range p.InlinePolicies {
		pn := tfName(p.RoleName + "-" + pol.Name)
		fmt.Fprintf(&b, "resource \"aws_iam_role_policy\" %q {\n", pn)
		fmt.Fprintf(&b, "  name   = %q\n", pol.Name)
		fmt.Fprintf(&b, "  role   = aws_iam_role.%s.id\n", role)
		// Document is emitted verbatim (already IAM policy JSON) via jsonencode of
		// the decoded form would re-key it; instead wrap the raw JSON in a heredoc.
		fmt.Fprintf(&b, "  policy = %s\n", heredoc(pol.Document))
		b.WriteString("}\n\n")
	}

	for i, arn := range p.ManagedPolicyARNs {
		an := tfName(fmt.Sprintf("%s-managed-%d", p.RoleName, i))
		fmt.Fprintf(&b, "resource \"aws_iam_role_policy_attachment\" %q {\n", an)
		fmt.Fprintf(&b, "  role       = aws_iam_role.%s.name\n", role)
		fmt.Fprintf(&b, "  policy_arn = %q\n", arn)
		b.WriteString("}\n\n")
	}

	if p.InstanceProfile {
		fmt.Fprintf(&b, "resource \"aws_iam_instance_profile\" %q {\n", role)
		fmt.Fprintf(&b, "  name = %q\n", p.InstanceProfileName)
		fmt.Fprintf(&b, "  role = aws_iam_role.%s.name\n", role)
		b.WriteString("}\n\n")
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

func renderIAMGCP(p IAMPlan) string {
	var b strings.Builder
	sa := tfName(p.RoleName)
	fmt.Fprintf(&b, "resource \"google_service_account\" %q {\n", sa)
	fmt.Fprintf(&b, "  account_id   = %q\n", p.RoleName)
	fmt.Fprintf(&b, "  display_name = %q\n", p.RoleName)
	b.WriteString("}\n")
	return b.String()
}
