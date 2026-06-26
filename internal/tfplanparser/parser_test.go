package tfplanparser

import (
	"testing"
)

func TestParsePlan(t *testing.T) {
	planJSON := `{
		"format_version": "0.1",
		"resource_changes": [
			{
				"address": "aws_security_group.allow_tls",
				"type": "aws_security_group",
				"name": "allow_tls",
				"provider_name": "registry.terraform.io/hashicorp/aws",
				"change": {
					"actions": ["create"],
					"before": null,
					"after": {
						"name": "allow_tls",
						"description": "Allow TLS inbound traffic"
					}
				}
			},
			{
				"address": "aws_instance.web",
				"type": "aws_instance",
				"name": "web",
				"provider_name": "registry.terraform.io/hashicorp/aws",
				"change": {
					"actions": ["update"],
					"before": {
						"ami": "ami-0c55b159cbfafe1f0",
						"tags": {
							"Environment": "dev"
						}
					},
					"after": {
						"ami": "ami-0c55b159cbfafe1f0",
						"tags": {
							"Environment": "production"
						}
					}
				}
			},
			{
				"address": "aws_subnet.private",
				"type": "aws_subnet",
				"name": "private",
				"provider_name": "registry.terraform.io/hashicorp/aws",
				"change": {
					"actions": ["delete"],
					"before": {
						"cidr_block": "10.0.1.0/24"
					},
					"after": null
				}
			},
			{
				"address": "aws_vpc.main",
				"type": "aws_vpc",
				"name": "main",
				"provider_name": "registry.terraform.io/hashicorp/aws",
				"change": {
					"actions": ["no-op"],
					"before": {
						"cidr_block": "10.0.0.0/16"
					},
					"after": {
						"cidr_block": "10.0.0.0/16"
					}
				}
			}
		]
	}`

	summary, err := ParsePlanJSON([]byte(planJSON))
	if err != nil {
		t.Fatalf("failed to parse plan JSON: %v", err)
	}

	if summary.Added != 1 {
		t.Errorf("expected 1 added resource, got %d", summary.Added)
	}
	if summary.Changed != 1 {
		t.Errorf("expected 1 changed resource, got %d", summary.Changed)
	}
	if summary.Removed != 1 {
		t.Errorf("expected 1 removed resource, got %d", summary.Removed)
	}
	if summary.ResourcesChanged != 3 {
		t.Errorf("expected 3 resources changed in total, got %d", summary.ResourcesChanged)
	}

	if len(summary.DriftDetails) != 3 {
		t.Fatalf("expected 3 drift details, got %d", len(summary.DriftDetails))
	}

	// Verify update details and changed attributes
	var updateDetail *DriftDetail
	for _, d := range summary.DriftDetails {
		if d.Address == "aws_instance.web" {
			updateDetail = &d
		}
	}

	if updateDetail == nil {
		t.Fatal("expected update detail for aws_instance.web")
	}

	if updateDetail.ChangeAction != "update" {
		t.Errorf("expected change action to be 'update', got %q", updateDetail.ChangeAction)
	}

	if len(updateDetail.ChangedAttrs) != 1 || updateDetail.ChangedAttrs[0] != "tags.Environment" {
		t.Errorf("expected changed attributes to contain 'tags.Environment', got %v", updateDetail.ChangedAttrs)
	}
}
