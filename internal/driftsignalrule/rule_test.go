package driftsignalrule

import (
	"context"
	"testing"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/client"
	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/tfplanparser"
)

type mockClient struct {
	client.Client
	firedEvents []string
}

func (m *mockClient) FireEvent(ctx context.Context, eventType string, payload map[string]interface{}) error {
	m.firedEvents = append(m.firedEvents, eventType)
	return nil
}

func TestEvaluateAndSignal(t *testing.T) {
	tests := []struct {
		name         string
		summary      *tfplanparser.PlanSummary
		approvedSHAs []string
		changeSHA    string
		expectFired  bool
		expectRisky  bool
	}{
		{
			name:         "No changes - no drift signaled",
			summary:      &tfplanparser.PlanSummary{ResourcesChanged: 0},
			approvedSHAs: []string{"sha1"},
			changeSHA:    "sha2",
			expectFired:  false,
		},
		{
			name: "Approved change - no drift signaled",
			summary: &tfplanparser.PlanSummary{
				ResourcesChanged: 1,
				Added:            1,
				DriftDetails: []tfplanparser.DriftDetail{
					{Address: "aws_vpc.main", Type: "aws_vpc", ChangeAction: "create"},
				},
			},
			approvedSHAs: []string{"sha1", "sha2"},
			changeSHA:    "sha2",
			expectFired:  false,
		},
		{
			name: "Unapproved change - drift signaled",
			summary: &tfplanparser.PlanSummary{
				ResourcesChanged: 1,
				Changed:          1,
				DriftDetails: []tfplanparser.DriftDetail{
					{Address: "aws_instance.web", Type: "aws_instance", ChangeAction: "update"},
				},
			},
			approvedSHAs: []string{"sha1"},
			changeSHA:    "sha2",
			expectFired:  true,
			expectRisky:  false,
		},
		{
			name: "Unapproved risky change - drift signaled risky",
			summary: &tfplanparser.PlanSummary{
				ResourcesChanged: 1,
				Changed:          1,
				DriftDetails: []tfplanparser.DriftDetail{
					{Address: "aws_security_group.main", Type: "aws_security_group", ChangeAction: "update"},
				},
			},
			approvedSHAs: []string{"sha1"},
			changeSHA:    "sha2",
			expectFired:  true,
			expectRisky:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &mockClient{}
			event, err := EvaluateAndSignal(context.Background(), m, tt.summary, tt.approvedSHAs, tt.changeSHA)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.expectFired {
				if len(m.firedEvents) != 1 || m.firedEvents[0] != "drift_detected" {
					t.Errorf("expected 1 drift_detected event, got %v", m.firedEvents)
				}
				if event == nil || event.Type != "drift_detected" {
					t.Errorf("expected drift_detected event struct returned, got %v", event)
				}
				risky, ok := event.Payload["risky"].(bool)
				if !ok || risky != tt.expectRisky {
					t.Errorf("expected risky payload field to be %v, got %v", tt.expectRisky, event.Payload["risky"])
				}
			} else {
				if len(m.firedEvents) != 0 {
					t.Errorf("expected no events fired, got %v", m.firedEvents)
				}
				if event != nil {
					t.Errorf("expected nil event returned, got %v", event)
				}
			}
		})
	}
}
