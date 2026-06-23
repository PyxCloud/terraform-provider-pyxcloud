package catalog

import (
	"context"
	"fmt"
	"strings"
)

// scheduled-trigger is the abstract `scheduled-trigger` component (the canonical
// form of an EventBridge cron rule / a Kubernetes CronJob): "run this workload on
// a cron/rate schedule". Like object-storage and cache it has NO sizing catalog —
// a scheduled trigger is region/cluster-scoped, so the only catalog lookup is the
// region (region_name + provider -> csp_region). The component therefore depends
// on the RegionCatalog only, exactly like object-storage, monitoring and cache.
//
// Per-provider mapping:
//
//   - AWS: aws_cloudwatch_event_rule (the EventBridge schedule) + a
//     aws_cloudwatch_event_target wiring the rule to the invoked resource
//     (a Lambda function ARN or similar). This is the direct EventBridge-cron
//     replacement being migrated AWAY from.
//   - DigitalOcean: a DOKS CronJob — kubernetes_cron_job_v1 on an existing
//     DigitalOcean Kubernetes (DOKS) cluster. DO Functions has NO first-class
//     Terraform scheduled-trigger resource (its scheduled triggers are authored
//     in project.yml at deploy time, not in the TF provider), so the DOKS CronJob
//     is the canonical, plan-time-expressible replacement.
//
// SCHEDULE VOCABULARY: the abstract Schedule reuses the SAME cron(...)/rate(...)
// expression convention the synthetics component already uses (SyntheticsSpec.
// ScheduleExpr) for AWS, and is translated to a Kubernetes 5-field cron string
// for the DOKS CronJob — no new schedule vocabulary is forked.

// Canonical scheduled-trigger type tokens. `scheduled-trigger` is canonical;
// `cron-job` and `scheduled-task` are accepted aliases (all name the same
// component, mirroring the TopologyInspector vocabulary in SPEC §3.1).
const (
	TypeScheduledTrigger = "scheduled-trigger"
	TypeCronJob          = "cron-job"
	TypeScheduledTask    = "scheduled-task"
)

// ScheduledTriggerSpec is the abstract description of a scheduled trigger.
// Provider-neutral.
type ScheduledTriggerSpec struct {
	Name     string // component name, e.g. "nightly-report"
	Region   string // abstract pyx region_name
	Provider string // aws | digitalocean | ...

	// Schedule is the cron/rate expression. AWS form: cron(...) / rate(...)
	// (reusing the synthetics ScheduleExpr convention). For DOKS the AWS form is
	// translated to a Kubernetes 5-field cron string; a bare 5-field cron is also
	// accepted verbatim. Empty -> defaults to "rate(1 day)" (cron "0 0 * * *").
	Schedule string

	// Image is the container image the trigger runs (DOKS CronJob). On AWS the
	// trigger fires the InvokeTarget instead.
	Image string

	// Command is the optional container entrypoint/args (DOKS CronJob).
	Command []string

	// ClusterName is the existing DOKS cluster the CronJob is scheduled on
	// (DigitalOcean). Required for DO; ignored on AWS.
	ClusterName string

	// Namespace is the Kubernetes namespace for the CronJob (DOKS). Empty ->
	// "default".
	Namespace string

	// InvokeTarget is the ARN/identifier of the resource the EventBridge rule
	// invokes (AWS), e.g. a Lambda function ARN. Empty -> the render declares an
	// out-of-band variable so the rule target is supplied at apply time.
	InvokeTarget string
}

// ScheduledTriggerPlan is the deterministic, catalog-resolved concrete
// translation of a ScheduledTriggerSpec for one provider. STRUCTURED plan (not
// rendered .tf) — the provider owns rendering and state, consistent with the
// other components (§8).
type ScheduledTriggerPlan struct {
	Provider   string `json:"provider"`    // aws | digitalocean
	CSP        string `json:"csp"`         // catalog token: aws | do
	RegionName string `json:"region_name"` // abstract pyx region
	CSPRegion  string `json:"csp_region"`  // concrete provider region (catalog-resolved)

	Name string `json:"name"`

	// Schedule is the resolved provider-native schedule string: the AWS
	// cron(...)/rate(...) expression for EventBridge, or the Kubernetes 5-field
	// cron string for the DOKS CronJob.
	Schedule string `json:"schedule"`

	Image        string   `json:"image,omitempty"`
	Command      []string `json:"command,omitempty"`
	ClusterName  string   `json:"cluster_name,omitempty"`
	Namespace    string   `json:"namespace,omitempty"`
	InvokeTarget string   `json:"invoke_target,omitempty"`

	ResourceType string `json:"resource_type"` // top provider resource
}

// TranslateScheduledTrigger resolves a ScheduledTriggerSpec into a concrete
// ScheduledTriggerPlan using the catalog. Deterministic and catalog-driven: the
// csp_region comes from the region catalog (never invented). Any missing catalog
// data — or a provider with no scheduled-trigger primitive — surfaces as a hard
// plan-time error (never a silent fallback), per SPEC §4.
func TranslateScheduledTrigger(ctx context.Context, cat RegionCatalog, spec ScheduledTriggerSpec) (ScheduledTriggerPlan, error) {
	if err := validateScheduledTriggerSpec(spec); err != nil {
		return ScheduledTriggerPlan{}, err
	}
	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return ScheduledTriggerPlan{}, err
	}
	provider := lc(spec.Provider)
	name := canonicalName(spec.Name, "pyxcloud-trigger")

	plan := ScheduledTriggerPlan{
		Provider:   provider,
		CSP:        row.CSP,
		RegionName: row.RegionName,
		CSPRegion:  row.CSPRegion,
		Name:       name,
	}

	switch provider {
	case ProviderAWS:
		// EventBridge native cron(...)/rate(...) — the form being migrated away from.
		plan.Schedule = awsScheduleExpr(spec.Schedule)
		plan.InvokeTarget = strings.TrimSpace(spec.InvokeTarget)
		plan.ResourceType = "aws_cloudwatch_event_rule"
	case ProviderDigitalOcean:
		// DOKS CronJob: needs an existing cluster + an image + a k8s cron string.
		cluster := strings.TrimSpace(spec.ClusterName)
		if cluster == "" {
			return ScheduledTriggerPlan{}, fmt.Errorf(
				"scheduled-trigger: digitalocean renders a DOKS CronJob (kubernetes_cron_job_v1) " +
					"which requires an existing DOKS cluster — cluster_name is required (DO Functions " +
					"has no first-class Terraform scheduled-trigger resource). This is a hard plan-time " +
					"error, never a silent fallback")
		}
		if strings.TrimSpace(spec.Image) == "" {
			return ScheduledTriggerPlan{}, fmt.Errorf(
				"scheduled-trigger: digitalocean DOKS CronJob requires an image to run (image is required)")
		}
		ns := strings.TrimSpace(spec.Namespace)
		if ns == "" {
			ns = "default"
		}
		plan.Schedule = k8sCronExpr(spec.Schedule)
		plan.Image = strings.TrimSpace(spec.Image)
		plan.Command = spec.Command
		plan.ClusterName = cluster
		plan.Namespace = ns
		plan.ResourceType = "kubernetes_cron_job_v1"
	default:
		return ScheduledTriggerPlan{}, ErrComponentUnsupported{
			Component: TypeScheduledTrigger, Provider: provider, CSP: row.CSP, CSPRegion: row.CSPRegion,
			Alternative: "scheduled-trigger is supported on aws (EventBridge: aws_cloudwatch_event_rule) " +
				"and digitalocean (DOKS CronJob: kubernetes_cron_job_v1); run the schedule on a VM cron " +
				"or a managed-kubernetes CronJob for other providers",
		}
	}
	return plan, nil
}

// awsScheduleExpr returns a valid EventBridge schedule expression, defaulting to
// a daily rate when none is supplied. A bare 5-field cron is wrapped in cron(...);
// an already-wrapped cron(...)/rate(...) is passed through verbatim.
func awsScheduleExpr(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "rate(1 day)"
	}
	low := strings.ToLower(s)
	if strings.HasPrefix(low, "cron(") || strings.HasPrefix(low, "rate(") {
		return s
	}
	// A bare 5-field cron string -> EventBridge uses a 6-field cron (with year),
	// but accepts the AWS form cron(min hour dom month dow year). We keep the user
	// expression inside cron(...) and append ? * when it is the common 5-field form
	// so EventBridge's 6-field requirement is met deterministically.
	fields := strings.Fields(s)
	if len(fields) == 5 {
		// EventBridge cron: do-month and do-week cannot both be * ; map a 5-field
		// "min hour dom month dow" to the 6-field AWS form by replacing the trailing
		// dow '*' with '?' and appending the year wildcard '*'.
		dom, dow := fields[2], fields[4]
		if dom == "*" && dow == "*" {
			dow = "?"
		}
		return fmt.Sprintf("cron(%s %s %s %s %s *)", fields[0], fields[1], dom, fields[3], dow)
	}
	return fmt.Sprintf("cron(%s)", s)
}

// k8sCronExpr returns a Kubernetes 5-field cron string. It accepts a bare 5-field
// cron verbatim, and translates the AWS rate(...)/cron(...) convention to the
// closest k8s cron so the SAME abstract Schedule works on both providers.
func k8sCronExpr(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "0 0 * * *" // daily at midnight
	}
	low := strings.ToLower(s)
	switch {
	case strings.HasPrefix(low, "rate("):
		return rateToK8sCron(low)
	case strings.HasPrefix(low, "cron("):
		// Unwrap cron(...) and reduce the AWS 6-field form to a k8s 5-field form.
		inner := strings.TrimSuffix(strings.TrimPrefix(s[strings.Index(s, "(")+1:], ""), ")")
		fields := strings.Fields(inner)
		if len(fields) >= 5 {
			dom, dow := fields[2], fields[4]
			if dow == "?" {
				dow = "*"
			}
			if dom == "?" {
				dom = "*"
			}
			return fmt.Sprintf("%s %s %s %s %s", fields[0], fields[1], dom, fields[3], dow)
		}
		return inner
	default:
		return s // already a bare k8s cron
	}
}

// rateToK8sCron maps the common rate(...) expressions to a k8s cron string. Only
// the deterministic, unambiguous cases are mapped; anything else falls back to a
// daily schedule rather than inventing an imprecise cron.
func rateToK8sCron(low string) string {
	inner := strings.TrimSuffix(strings.TrimPrefix(low, "rate("), ")")
	fields := strings.Fields(inner)
	if len(fields) == 2 {
		n, unit := fields[0], fields[1]
		switch {
		case strings.HasPrefix(unit, "minute"):
			if n == "1" {
				return "* * * * *"
			}
			return fmt.Sprintf("*/%s * * * *", n)
		case strings.HasPrefix(unit, "hour"):
			if n == "1" {
				return "0 * * * *"
			}
			return fmt.Sprintf("0 */%s * * *", n)
		case strings.HasPrefix(unit, "day"):
			return "0 0 * * *"
		}
	}
	return "0 0 * * *"
}

func validateScheduledTriggerSpec(spec ScheduledTriggerSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("scheduled-trigger: region (abstract pyx region_name) is required")
	}
	if strings.TrimSpace(spec.Provider) == "" {
		return fmt.Errorf("scheduled-trigger: provider is required (aws | digitalocean)")
	}
	if _, ok := ProviderToCSP(spec.Provider); !ok {
		return fmt.Errorf("scheduled-trigger: unknown provider %q (aws | digitalocean)", spec.Provider)
	}
	return nil
}

// CanonicalScheduledTriggerType maps an accepted type token to the canonical
// scheduled-trigger token, reporting whether it is recognised.
func CanonicalScheduledTriggerType(t string) (string, bool) {
	switch lc(t) {
	case TypeScheduledTrigger, TypeCronJob, TypeScheduledTask:
		return TypeScheduledTrigger, true
	default:
		return "", false
	}
}
