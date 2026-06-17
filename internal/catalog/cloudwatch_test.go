package catalog

import (
	"context"
	"strings"
	"testing"
)

func TestTranslateMonitoringAWS(t *testing.T) {
	cat, err := NewEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	p, err := TranslateMonitoring(context.Background(), cat, MonitoringSpec{
		Name: "obs", Provider: "aws", Region: "Dublin",
		LogGroups: []LogGroup{{Name: "/pyx/app", RetentionDays: 30}},
		Alarms: []MetricAlarm{{
			Name: "cpu-high", Namespace: "AWS/EC2", MetricName: "CPUUtilization",
			ComparisonOperator: "GreaterThanThreshold", Threshold: 80, EvaluationPeriods: 2,
		}},
	})
	if err != nil {
		t.Fatalf("TranslateMonitoring: %v", err)
	}
	hcl, err := RenderMonitoringHCL(p)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"resource \"aws_cloudwatch_log_group\"",
		"name = \"/pyx/app\"",
		"retention_in_days = 30",
		"resource \"aws_cloudwatch_metric_alarm\"",
		"metric_name         = \"CPUUtilization\"",
		"threshold           = 80",
		"evaluation_periods  = 2",
		"period              = 300", // default
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("monitoring HCL missing %q\n---\n%s", want, hcl)
		}
	}
}

func TestTranslateMonitoringDOUnsupported(t *testing.T) {
	cat, _ := NewEmbedded()
	_, err := TranslateMonitoring(context.Background(), cat, MonitoringSpec{
		Name: "x", Provider: "digitalocean", Region: "Amsterdam",
		LogGroups: []LogGroup{{Name: "a"}},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported on digitalocean") {
		t.Errorf("expected DO unsupported, got %v", err)
	}
}

func TestAssembleHCLMonitoringComponent(t *testing.T) {
	cat, _ := NewEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "demo", Provider: "aws", Region: "Dublin",
		Components: []AssembleComponent{
			{Name: "obs", Type: "monitoring", Monitoring: &AssembleMonitoring{
				LogGroups: []LogGroup{{Name: "/pyx/demo", RetentionDays: 14}},
			}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL monitoring: %v", err)
	}
	if !strings.Contains(strings.Join(docs, "\n"), "aws_cloudwatch_log_group") {
		t.Errorf("assembled monitoring missing log group:\n%s", strings.Join(docs, "\n"))
	}
}
