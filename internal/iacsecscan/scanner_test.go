// Package iacsecscan tests for IaC security scanner.

import (
	"testing"
)

func TestScan_OpenPorts(t *testing.T) {
	tests := []struct {
		name      string
		resources []Resource
		want      int // number of findings
	}{
		{
			name: "open SSH port via security group rule",
			resources: []Resource{
				{
					Type: "aws_security_group_rule",
					Attributes: map[string]interface{}{
						"type":        "ingress",
						"from_port":   22,
						"to_port":     22,
						"protocol":    "tcp",
						"cidr_blocks": []interface{}{"0.0.0.0/0"},
					},
				},
			},
			want: 1,
		},
		{
			name: "open all ports via security group rule",
			resources: []Resource{
				{
					Type: "aws_security_group_rule",
					Attributes: map[string]interface{}{
						"type":        "ingress",
						"from_port":   0,
						"to_port":     0,
						"protocol":    "-1",
						"cidr_blocks": []interface{}{"0.0.0.0/0"},
					},
				},
			},
			want: 1,
		},
		{
			name: "non-open CIDR should not trigger",
			resources: []Resource{
				{
					Type: "aws_security_group_rule",
					Attributes: map[string]interface{}{
						"type":        "ingress",
						"from_port":   22,
						"to_port":     22,
						"protocol":    "tcp",
						"cidr_blocks": []interface{}{"10.0.0.0/8"},
					},
				},
			},
			want: 0,
		},
		{
			name: "security group with ingress block open",
			resources: []Resource{
				{
					Type: "aws_security_group",
					Attributes: map[string]interface{}{
						"name": "test-sg",
						"ingress": []interface{}{
							map[string]interface{}{
								"from_port":   3306,
								"to_port":     3306,
								"protocol":    "tcp",
								"cidr_blocks": []interface{}{"0.0.0.0/0"},
							},
						},
					},
				},
			},
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Scan(tt.resources)
			if len(got) != tt.want {
				t.Errorf("Scan() got %d findings, want %d", len(got), tt.want)
			}
		})
	}
}

func TestScan_PublicStorage(t *testing.T) {
	tests := []struct {
		name      string
		resources []Resource
		want      int
	}{
		{
			name: "public-read ACL",
			resources: []Resource{
				{
					Type: "aws_s3_bucket",
					Attributes: map[string]interface{}{
						"name": "my-bucket",
						"acl":  "public-read",
					},
				},
			},
			want: 1,
		},
		{
			name: "public access block not set",
			resources: []Resource{
				{
					Type: "aws_s3_bucket",
					Attributes: map[string]interface{}{
						"name":             "my-bucket",
						"block_public_acls": false,
					},
				},
			},
			want: 1,
		},
		{
			name: "private bucket no findings",
			resources: []Resource{
				{
					Type: "aws_s3_bucket",
					Attributes: map[string]interface{}{
						"name": "my-bucket",
						"acl":  "private",
					},
				},
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Scan(tt.resources)
			if len(got) != tt.want {
				t.Errorf("Scan() got %d findings, want %d", len(got), tt.want)
			}
		})
	}
}

func TestScan_IAMWildcards(t *testing.T) {
	tests := []struct {
		name      string
		resources []Resource
		want      int
	}{
		{
			name: "IAM policy with wildcard Action and Resource",
			resources: []Resource{
				{
					Type: "aws_iam_policy",
					Attributes: map[string]interface{}{
						"name":   "admin-policy",
						"policy": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`,
					},
				},
			},
			want: 1,
		},
		{
			name: "IAM policy with specific actions no finding",
			resources: []Resource{
				{
					Type: "aws_iam_policy",
					Attributes: map[string]interface{}{
						"name":   "readonly-policy",
						"policy": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::my-bucket/*"}]}`,
					},
				},
			},
			want: 0,
		},
		{
			name: "IAM role policy with wildcard array",
			resources: []Resource{
				{
					Type: "aws_iam_role_policy",
					Attributes: map[string]interface{}{
						"name":   "admin-role-policy",
						"policy": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["*"],"Resource":"*"}]}`,
					},
				},
			},
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Scan(tt.resources)
			if len(got) != tt.want {
				t.Errorf("Scan() got %d findings, want %d", len(got), tt.want)
			}
		})
	}
}