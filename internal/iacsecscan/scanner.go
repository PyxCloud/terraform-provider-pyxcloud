// Package iacsecscan provides IaC security scanning capabilities.

import (
	"fmt"
	"strings"
)

// Resource represents a Terraform resource with its type and attributes.
type Resource struct {
	Type       string
	Attributes map[string]interface{}
}

// Finding represents a security misconfiguration finding.
type Finding struct {
	ResourceType string
	ResourceName string
	Severity     string // "HIGH", "MEDIUM", "LOW"
	RuleID       string
	Description  string
	Remediation  string
}

// Scan runs all security checks on the given resources and returns findings.
func Scan(resources []Resource) []Finding {
	var findings []Finding
	for _, r := range resources {
		findings = append(findings, checkOpenPorts(r)...)
		findings = append(findings, checkPublicStorage(r)...)
		findings = append(findings, checkIAMWildcards(r)...)
	}
	return findings
}

// checkOpenPorts detects security group rules allowing inbound traffic from 0.0.0.0/0 on sensitive ports.
func checkOpenPorts(r Resource) []Finding {
	var findings []Finding
	// Support aws_security_group_rule and aws_security_group (ingress blocks)
	switch r.Type {
	case "aws_security_group_rule":
		if isOpenIngress(r.Attributes) {
			findings = append(findings, makeFinding(r, "OPEN-PORT-SGR", "Security group rule allows inbound from 0.0.0.0/0 on a sensitive port", "Restrict the source CIDR to a specific IP range or use a security group reference."))
		}
	case "aws_security_group":
		ingressList, ok := r.Attributes["ingress"].([]interface{})
		if !ok {
			return findings
		}
		for _, ing := range ingressList {
			ingMap, ok := ing.(map[string]interface{})
			if !ok {
				continue
			}
			// Create a temporary attributes map for the ingress block
			attrs := make(map[string]interface{})
			if cidr, ok := ingMap["cidr_blocks"]; ok {
				attrs["cidr_blocks"] = cidr
			}
			if fromPort, ok := ingMap["from_port"]; ok {
				attrs["from_port"] = fromPort
			}
			if toPort, ok := ingMap["to_port"]; ok {
				attrs["to_port"] = toPort
			}
			if protocol, ok := ingMap["protocol"]; ok {
				attrs["protocol"] = protocol
			}
			if isOpenIngress(attrs) {
				findings = append(findings, makeFinding(r, "OPEN-PORT-SG", "Security group ingress rule allows inbound from 0.0.0.0/0 on a sensitive port", "Restrict the source CIDR to a specific IP range or use a security group reference."))
			}
		}
	}
	return findings
}

// isOpenIngress checks if the attributes represent an ingress rule open to 0.0.0.0/0 on a sensitive port.
func isOpenIngress(attrs map[string]interface{}) bool {
	cidrBlocks, ok := attrs["cidr_blocks"]
	if !ok {
		return false
	}
	cidrList, ok := cidrBlocks.([]interface{})
	if !ok {
		return false
	}
	hasOpen := false
	for _, c := range cidrList {
		if c == "0.0.0.0/0" {
			hasOpen = true
			break
		}
	}
	if !hasOpen {
		return false
	}
	// Check if the port is sensitive
	fromPort, ok := attrs["from_port"]
	if !ok {
		return false
	}
	toPort, ok := attrs["to_port"]
	if !ok {
		toPort = fromPort
	}
	// Convert to int
	from := toInt(fromPort)
	to := toInt(toPort)
	if from == 0 && to == 0 {
		// All ports open
		return true
	}
	sensitivePorts := []int{22, 3389, 3306, 5432, 6379, 27017, 11211, 9200, 9300, 8080, 8443, 9090, 3000, 5000, 8000, 8888}
	for _, sp := range sensitivePorts {
		if sp >= from && sp <= to {
			return true
		}
	}
	return false
}

// checkPublicStorage detects S3 buckets with public access.
func checkPublicStorage(r Resource) []Finding {
	var findings []Finding
	if r.Type != "aws_s3_bucket" {
		return findings
	}
	// Check for public ACL
	acl, ok := r.Attributes["acl"]
	if ok && acl == "public-read" || acl == "public-read-write" {
		findings = append(findings, makeFinding(r, "PUBLIC-STORAGE-ACL", "S3 bucket has a public ACL", "Remove the public ACL and use bucket policies or IAM for access control."))
	}
	// Check for public access block not set or allowing public
	publicBlock, ok := r.Attributes["block_public_acls"]
	if ok && publicBlock == false {
		findings = append(findings, makeFinding(r, "PUBLIC-STORAGE-BLOCK", "S3 bucket does not block public ACLs", "Set block_public_acls = true in the aws_s3_bucket_public_access_block resource."))
	}
	// Check for bucket policy allowing public access (simplified)
	policy, ok := r.Attributes["policy"]
	if ok {
		policyStr, ok := policy.(string)
		if ok && strings.Contains(policyStr, `"Effect":"Allow"`) && strings.Contains(policyStr, `"Principal":"*"`) {
			findings = append(findings, makeFinding(r, "PUBLIC-STORAGE-POLICY", "S3 bucket policy allows public access", "Review the bucket policy and restrict the Principal to specific AWS accounts or roles."))
		}
	}
	return findings
}

// checkIAMWildcards detects IAM policies with wildcard Action or Resource.
func checkIAMWildcards(r Resource) []Finding {
	var findings []Finding
	switch r.Type {
	case "aws_iam_policy", "aws_iam_role_policy", "aws_iam_group_policy", "aws_iam_user_policy":
		policy, ok := r.Attributes["policy"]
		if !ok {
			return findings
		}
		policyStr, ok := policy.(string)
		if !ok {
			return findings
		}
		// Simple check for wildcard Action and Resource
		if strings.Contains(policyStr, `"Action":"*"`) && strings.Contains(policyStr, `"Resource":"*"`) {
			findings = append(findings, makeFinding(r, "IAM-WILDCARD", "IAM policy allows all actions on all resources", "Replace the wildcard Action and Resource with specific permissions following least privilege."))
		}
		// Also check for "Action": ["*"] (array form)
		if strings.Contains(policyStr, `"Action":["*"]`) && strings.Contains(policyStr, `"Resource":"*"`) {
			findings = append(findings, makeFinding(r, "IAM-WILDCARD-ARRAY", "IAM policy allows all actions on all resources (array form)", "Replace the wildcard Action and Resource with specific permissions following least privilege."))
		}
	}
	return findings
}

// makeFinding creates a Finding with common fields.
func makeFinding(r Resource, ruleID, description, remediation string) Finding {
	severity := "HIGH"
	if strings.HasPrefix(ruleID, "PUBLIC-STORAGE") {
		severity = "MEDIUM"
	}
	return Finding{
		ResourceType: r.Type,
		ResourceName: fmt.Sprintf("%v", r.Attributes["name"]),
		Severity:     severity,
		RuleID:       ruleID,
		Description:  description,
		Remediation:  remediation,
	}
}

// toInt converts an interface{} to int, supporting float64 and int.
func toInt(v interface{}) int {
	switch val := v.(type) {
	case float64:
		return int(val)
	case int:
		return val
	case int64:
		return int(val)
	default:
		return 0
	}
}