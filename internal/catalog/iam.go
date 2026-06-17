package catalog

import (
	"context"
	"fmt"
	"strings"
)

// IAM is the abstract `iam` component: a workload identity (a role assumable by a
// service) plus its permissions — the canonical form of the per-provider scripts'
// aws_iam_role / role_policy / instance_profile glue.
//
//   - AWS: aws_iam_role (assume_role_policy) + aws_iam_role_policy (inline) +
//     aws_iam_role_policy_attachment (managed ARNs) + optional aws_iam_instance_profile.
//   - GCP: google_service_account (the workload identity). Managed/inline policy
//     ARNs are AWS-shaped and do NOT map to GCP IAM roles, so they are a hard
//     plan-time error on GCP (declare GCP roles via a future binding field) — never
//     silently dropped.
//   - DigitalOcean: UNSUPPORTED (no IAM-role primitive). Clean plan-time error.
//
// Policy documents are raw IAM JSON (AWS-shaped) — the canonical policy form. A
// document is data, not a secret, so it lives in the topology/state.

// IAMPolicy is one inline policy: a name + a raw IAM policy JSON document.
type IAMPolicy struct {
	Name     string
	Document string // raw IAM policy JSON
}

// IAMSpec is the abstract IAM identity. Provider-neutral surface; AWS-shaped policy.
type IAMSpec struct {
	Name              string
	Region            string // abstract pyx region_name (provider validation only; IAM is global)
	Provider          string
	AssumeService     string      // principal allowed to assume the role, e.g. "ec2.amazonaws.com"
	InlinePolicies    []IAMPolicy // inline policy documents
	ManagedPolicyARNs []string    // managed policy ARNs to attach
	InstanceProfile   bool        // also emit an instance profile (EC2 attach)
}

// IAMPlan is the resolved concrete IAM translation.
type IAMPlan struct {
	Provider          string      `json:"provider"`
	CSP               string      `json:"csp"`
	Name              string      `json:"name"`
	AssumeService     string      `json:"assume_service"`
	InlinePolicies    []IAMPolicy `json:"inline_policies"`
	ManagedPolicyARNs []string    `json:"managed_policy_arns"`
	InstanceProfile   bool        `json:"instance_profile"`
	ResourceType      string      `json:"resource_type"`
}

// TranslateIAM resolves an IAMSpec. DO is unsupported; GCP rejects AWS-shaped policies.
func TranslateIAM(_ context.Context, _ RegionCatalog, spec IAMSpec) (IAMPlan, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return IAMPlan{}, fmt.Errorf("iam: name is required")
	}
	csp, ok := ProviderToCSP(spec.Provider)
	if !ok {
		return IAMPlan{}, fmt.Errorf("iam: unknown provider %q (aws | gcp | digitalocean)", spec.Provider)
	}
	plan := IAMPlan{
		Provider:          strings.ToLower(spec.Provider),
		CSP:               csp,
		Name:              spec.Name,
		AssumeService:     spec.AssumeService,
		InlinePolicies:    spec.InlinePolicies,
		ManagedPolicyARNs: spec.ManagedPolicyARNs,
		InstanceProfile:   spec.InstanceProfile,
	}
	switch plan.Provider {
	case ProviderAWS:
		if strings.TrimSpace(spec.AssumeService) == "" {
			plan.AssumeService = "ec2.amazonaws.com" // the common EC2 instance-role default
		}
		plan.ResourceType = "aws_iam_role"
	case ProviderGCP:
		if len(spec.InlinePolicies) > 0 || len(spec.ManagedPolicyARNs) > 0 {
			return IAMPlan{}, fmt.Errorf("iam: AWS-shaped inline/managed policies do not map to GCP IAM " +
				"(google_service_account has no role-policy attachment of this form) — declare GCP project roles instead " +
				"(this is a hard plan-time error, never a silent drop)")
		}
		plan.ResourceType = "google_service_account"
	case ProviderDigitalOcean:
		return IAMPlan{}, fmt.Errorf("iam: unsupported on digitalocean (no IAM-role primitive) — " +
			"use AWS/GCP for workload identities (hard plan-time error)")
	default:
		return IAMPlan{}, fmt.Errorf("iam: unsupported provider %q", spec.Provider)
	}
	return plan, nil
}
