package catalog

import (
	"fmt"
	"strings"
)

// RenderScheduledTriggerHCL renders a ScheduledTriggerPlan into provider HCL.
// AWS -> aws_cloudwatch_event_rule (+ event target); DigitalOcean -> a DOKS
// CronJob (kubernetes_cron_job_v1). Any other provider never reaches here
// (TranslateScheduledTrigger rejects it with a clean ErrComponentUnsupported).
func RenderScheduledTriggerHCL(plan ScheduledTriggerPlan) (string, error) {
	switch plan.Provider {
	case ProviderAWS:
		return renderScheduledTriggerAWS(plan), nil
	case ProviderDigitalOcean:
		return renderScheduledTriggerDO(plan), nil
	default:
		return "", fmt.Errorf("render: unsupported provider %q for scheduled-trigger", plan.Provider)
	}
}

func renderScheduledTriggerAWS(p ScheduledTriggerPlan) string {
	name := tfName(p.Name)
	var b strings.Builder

	fmt.Fprintf(&b, "resource \"aws_cloudwatch_event_rule\" %q {\n", name)
	fmt.Fprintf(&b, "  name                = %q\n", p.Name)
	fmt.Fprintf(&b, "  schedule_expression = %q\n", p.Schedule)
	fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", p.Name)
	b.WriteString("}\n\n")

	// The rule needs a target to invoke. The target ARN is supplied either inline
	// (InvokeTarget) or out-of-band via a variable, so the schedule is expressible
	// at plan time without hard-coding a downstream resource.
	target := fmt.Sprintf("%q", p.InvokeTarget)
	if strings.TrimSpace(p.InvokeTarget) == "" {
		target = "var." + name + "_target_arn"
		fmt.Fprintf(&b, "variable %q {\n  type        = string\n  description = \"ARN the scheduled trigger invokes (e.g. a Lambda function).\"\n}\n\n", name+"_target_arn")
	}
	fmt.Fprintf(&b, "resource \"aws_cloudwatch_event_target\" %q {\n", name)
	fmt.Fprintf(&b, "  rule = aws_cloudwatch_event_rule.%s.name\n", name)
	fmt.Fprintf(&b, "  arn  = %s\n", target)
	b.WriteString("}\n")
	return b.String()
}

func renderScheduledTriggerDO(p ScheduledTriggerPlan) string {
	name := tfName(p.Name)
	var b strings.Builder

	// The DOKS CronJob runs on an existing cluster; reference it via a data source
	// so the CronJob lands on the right cluster's kube credentials.
	fmt.Fprintf(&b, "data \"digitalocean_kubernetes_cluster\" %q {\n", name+"_cluster")
	fmt.Fprintf(&b, "  name = %q\n", p.ClusterName)
	b.WriteString("}\n\n")

	fmt.Fprintf(&b, "resource \"kubernetes_cron_job_v1\" %q {\n", name)
	b.WriteString("  metadata {\n")
	fmt.Fprintf(&b, "    name      = %q\n", p.Name)
	fmt.Fprintf(&b, "    namespace = %q\n", p.Namespace)
	fmt.Fprintf(&b, "    labels    = { pyxcloud = \"true\" }\n")
	b.WriteString("  }\n")
	b.WriteString("  spec {\n")
	fmt.Fprintf(&b, "    schedule                      = %q\n", p.Schedule)
	b.WriteString("    concurrency_policy            = \"Forbid\"\n")
	b.WriteString("    successful_jobs_history_limit = 3\n")
	b.WriteString("    failed_jobs_history_limit     = 1\n")
	b.WriteString("    job_template {\n")
	b.WriteString("      metadata {}\n")
	b.WriteString("      spec {\n")
	b.WriteString("        template {\n")
	b.WriteString("          metadata {}\n")
	b.WriteString("          spec {\n")
	b.WriteString("            container {\n")
	fmt.Fprintf(&b, "              name  = %q\n", p.Name)
	fmt.Fprintf(&b, "              image = %q\n", p.Image)
	if len(p.Command) > 0 {
		fmt.Fprintf(&b, "              command = [%s]\n", quoteList(p.Command))
	}
	b.WriteString("            }\n")
	b.WriteString("            restart_policy = \"OnFailure\"\n")
	b.WriteString("          }\n")
	b.WriteString("        }\n")
	b.WriteString("      }\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	b.WriteString("}\n")
	return b.String()
}

// quoteList renders a []string as a comma-separated list of quoted HCL strings.
func quoteList(items []string) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, fmt.Sprintf("%q", it))
	}
	return strings.Join(parts, ", ")
}
