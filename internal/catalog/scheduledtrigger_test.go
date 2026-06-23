package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestTranslateScheduledTriggerAWS asserts the EventBridge cron rule plan: region
// resolved, the cron(...)/rate(...) schedule carried, aws_cloudwatch_event_rule.
func TestTranslateScheduledTriggerAWS(t *testing.T) {
	t.Parallel()
	plan, err := TranslateScheduledTrigger(context.Background(), MustEmbedded(), ScheduledTriggerSpec{
		Name: "nightly", Region: "Frankfurt", Provider: "aws", Schedule: "cron(0 3 * * ? *)",
		InvokeTarget: "arn:aws:lambda:eu-central-1:123:function:f",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSPRegion != "eu-central-1" {
		t.Errorf("csp_region = %q, want eu-central-1", plan.CSPRegion)
	}
	if plan.ResourceType != "aws_cloudwatch_event_rule" {
		t.Errorf("resource_type = %q, want aws_cloudwatch_event_rule", plan.ResourceType)
	}
	if plan.Schedule != "cron(0 3 * * ? *)" {
		t.Errorf("schedule = %q, want it carried verbatim", plan.Schedule)
	}
	hcl, err := RenderScheduledTriggerHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "aws_cloudwatch_event_rule"`,
		`schedule_expression = "cron(0 3 * * ? *)"`,
		`resource "aws_cloudwatch_event_target"`,
		`arn  = "arn:aws:lambda:eu-central-1:123:function:f"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("AWS HCL missing %q\n%s", want, hcl)
		}
	}
}

// TestScheduledTriggerAWSBareCronWrapped asserts a bare 5-field cron is wrapped.
func TestScheduledTriggerAWSBareCronWrapped(t *testing.T) {
	t.Parallel()
	plan, err := TranslateScheduledTrigger(context.Background(), MustEmbedded(), ScheduledTriggerSpec{
		Name: "n", Region: "Frankfurt", Provider: "aws", Schedule: "0 3 * * *",
	})
	if err != nil {
		t.Fatal(err)
	}
	// dom=* dow=* -> dow becomes '?' and a year wildcard is appended.
	if plan.Schedule != "cron(0 3 * * ? *)" {
		t.Errorf("schedule = %q, want cron(0 3 * * ? *)", plan.Schedule)
	}
}

// TestTranslateScheduledTriggerDO asserts the DOKS CronJob plan + render.
func TestTranslateScheduledTriggerDO(t *testing.T) {
	t.Parallel()
	plan, err := TranslateScheduledTrigger(context.Background(), MustEmbedded(), ScheduledTriggerSpec{
		Name: "nightly", Region: "Frankfurt", Provider: "digitalocean",
		Schedule: "rate(1 hour)", Image: "registry.example/job:latest",
		Command: []string{"/bin/run", "--once"}, ClusterName: "prod-doks",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSPRegion != "fra1" {
		t.Errorf("csp_region = %q, want fra1", plan.CSPRegion)
	}
	if plan.ResourceType != "kubernetes_cron_job_v1" {
		t.Errorf("resource_type = %q, want kubernetes_cron_job_v1", plan.ResourceType)
	}
	if plan.Schedule != "0 * * * *" {
		t.Errorf("schedule = %q, want 0 * * * * (rate(1 hour) -> k8s cron)", plan.Schedule)
	}
	if plan.Namespace != "default" {
		t.Errorf("namespace = %q, want default", plan.Namespace)
	}
	hcl, err := RenderScheduledTriggerHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "kubernetes_cron_job_v1"`,
		`schedule                      = "0 * * * *"`,
		`image = "registry.example/job:latest"`,
		`command = ["/bin/run", "--once"]`,
		`data "digitalocean_kubernetes_cluster"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("DO HCL missing %q\n%s", want, hcl)
		}
	}
}

// TestScheduledTriggerDORequiresCluster asserts the hard plan-time error when the
// DOKS cluster is missing — never a silent fallback.
func TestScheduledTriggerDORequiresCluster(t *testing.T) {
	t.Parallel()
	_, err := TranslateScheduledTrigger(context.Background(), MustEmbedded(), ScheduledTriggerSpec{
		Name: "n", Region: "Frankfurt", Provider: "digitalocean", Image: "x:1",
	})
	if err == nil || !strings.Contains(err.Error(), "cluster_name is required") {
		t.Errorf("expected cluster_name required error, got %v", err)
	}
}

// TestScheduledTriggerUnsupportedProvider asserts a clean error for a provider
// with no scheduled-trigger primitive.
func TestScheduledTriggerUnsupportedProvider(t *testing.T) {
	t.Parallel()
	_, err := TranslateScheduledTrigger(context.Background(), MustEmbedded(), ScheduledTriggerSpec{
		Name: "n", Region: "Frankfurt", Provider: "gcp", Schedule: "rate(1 day)",
	})
	var unsup ErrComponentUnsupported
	if !errors.As(err, &unsup) {
		t.Fatalf("expected ErrComponentUnsupported, got %v", err)
	}
	if unsup.Component != TypeScheduledTrigger {
		t.Errorf("component = %q, want %q", unsup.Component, TypeScheduledTrigger)
	}
}

func TestCanonicalScheduledTriggerType(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"scheduled-trigger", "cron-job", "scheduled-task", "Scheduled-Trigger"} {
		if got, ok := CanonicalScheduledTriggerType(in); !ok || got != TypeScheduledTrigger {
			t.Errorf("CanonicalScheduledTriggerType(%q) = %q,%v", in, got, ok)
		}
	}
	if _, ok := CanonicalScheduledTriggerType("virtual-machine"); ok {
		t.Error("vm should not be a scheduled-trigger type")
	}
}
