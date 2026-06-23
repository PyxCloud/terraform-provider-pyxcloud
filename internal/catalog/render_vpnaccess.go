package catalog

import (
	"fmt"
	"strings"
)

// RenderVPNAccessHCL renders a VPNAccessPlan into provider HCL — the JIT VPN door.
// AWS emits the three coupled pieces that replace internal-vpn's manual add-peer.sh
// + jit-backing/terraform.tf:
//
//  1. aws_security_group "<name>-jit" — UDP <port>, ingress OWNED BY THE KEYCLOAK
//     SPI at runtime (lifecycle.ignore_changes = [ingress]) so terraform and the
//     SPI never fight; optional break-glass static ingress; open egress.
//  2. aws_dynamodb_table "<name>-jit-allowlist" — hash key sessionId, TTL on
//     ttlEpoch, PAY_PER_REQUEST, optional PITR (the SPI's session->rule store).
//  3. aws_iam_policy + aws_iam_role_policy_attachment — grants the Keycloak role
//     exactly the SG-ingress + DynamoDB actions the SPI needs, attached to it.
//
// It also emits the JIT door SG id as an output (the SPI's JIT_VPN_SG_ID env).
// Any non-AWS provider never reaches here (TranslateVPNAccess rejects it with a
// clean ErrComponentUnsupported).
func RenderVPNAccessHCL(plan VPNAccessPlan) (string, error) {
	switch plan.Provider {
	case ProviderAWS:
		return renderVPNAccessAWS(plan), nil
	default:
		return "", fmt.Errorf("render: unsupported provider %q for vpn-access", plan.Provider)
	}
}

func renderVPNAccessAWS(p VPNAccessPlan) string {
	name := tfName(p.LogicalName)
	jitSG := name + "_jit"
	table := name + "_jit_allowlist"
	policy := name + "_jit_policy"
	var b strings.Builder

	// VPC the JIT SG attaches to: an explicit VPC name (a sibling pyx_vpc), else the
	// account default VPC — matching the other env components' VPC handling.
	vpcRef := "data.aws_vpc.default.id"
	if p.VPC != "" {
		vpcRef = fmt.Sprintf("aws_vpc.%s.id", tfName(p.VPC))
	}

	// 1. The JIT WireGuard door SG. Ingress is the SPI's to manage at runtime.
	fmt.Fprintf(&b, "resource \"aws_security_group\" %q {\n", jitSG)
	fmt.Fprintf(&b, "  name        = \"%s-jit-sg\"\n", p.LogicalName)
	fmt.Fprintf(&b, "  description = \"WireGuard JIT %d/udp - ingress managed by the Keycloak SPI per logged-in IP\"\n", p.WireGuardPort)
	fmt.Fprintf(&b, "  vpc_id      = %s\n", vpcRef)
	// Optional break-glass static allow (admin lockout safety; default none).
	if len(p.BreakGlassCIDRs) > 0 {
		b.WriteString("  ingress {\n")
		fmt.Fprintf(&b, "    from_port   = %d\n", p.WireGuardPort)
		fmt.Fprintf(&b, "    to_port     = %d\n", p.WireGuardPort)
		b.WriteString("    protocol    = \"udp\"\n")
		fmt.Fprintf(&b, "    cidr_blocks = %s\n", hclCIDRList(p.BreakGlassCIDRs))
		b.WriteString("    description = \"break-glass-static\"\n")
		b.WriteString("  }\n")
	}
	b.WriteString("  egress {\n")
	b.WriteString("    from_port   = 0\n")
	b.WriteString("    to_port     = 0\n")
	b.WriteString("    protocol    = \"-1\"\n")
	b.WriteString("    cidr_blocks = [\"0.0.0.0/0\"]\n")
	b.WriteString("  }\n")
	// The SPI adds/removes per-IP `jit-<sessionId>` ingress rules at runtime;
	// terraform must NOT revert them.
	b.WriteString("  lifecycle { ignore_changes = [ingress] }\n")
	fmt.Fprintf(&b, "  tags = { Name = \"%s-jit-sg\", pyxcloud = \"true\" }\n", p.LogicalName)
	b.WriteString("}\n\n")

	// 2. The DynamoDB allowlist table — the SPI's session->ingress-rule store.
	fmt.Fprintf(&b, "resource \"aws_dynamodb_table\" %q {\n", table)
	fmt.Fprintf(&b, "  name         = %q\n", p.AllowlistTable)
	b.WriteString("  billing_mode = \"PAY_PER_REQUEST\"\n")
	b.WriteString("  hash_key     = \"sessionId\"\n")
	b.WriteString("  attribute {\n    name = \"sessionId\"\n    type = \"S\"\n  }\n")
	b.WriteString("  ttl {\n    attribute_name = \"ttlEpoch\"\n    enabled        = true\n  }\n")
	fmt.Fprintf(&b, "  point_in_time_recovery { enabled = %t }\n", p.PITR)
	fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", p.AllowlistTable)
	b.WriteString("}\n\n")

	// 3. IAM policy granting the Keycloak role the SG-ingress + DynamoDB actions the
	//    SPI needs, attached to the role. The SG-ingress actions need "*" (the SPI
	//    targets the SG by id at runtime); DynamoDB is scoped to the allowlist table.
	fmt.Fprintf(&b, "resource \"aws_iam_policy\" %q {\n", policy)
	fmt.Fprintf(&b, "  name   = \"%s-kc-jit-allowlist\"\n", p.LogicalName)
	b.WriteString("  policy = jsonencode({\n")
	b.WriteString("    Version = \"2012-10-17\"\n")
	b.WriteString("    Statement = [\n")
	b.WriteString("      {\n")
	b.WriteString("        Sid    = \"JitSgIngress\"\n")
	b.WriteString("        Effect = \"Allow\"\n")
	b.WriteString("        Action = [\n")
	b.WriteString("          \"ec2:AuthorizeSecurityGroupIngress\",\n")
	b.WriteString("          \"ec2:RevokeSecurityGroupIngress\",\n")
	b.WriteString("          \"ec2:DescribeSecurityGroups\",\n")
	b.WriteString("        ]\n")
	b.WriteString("        Resource = \"*\"\n")
	b.WriteString("      },\n")
	b.WriteString("      {\n")
	b.WriteString("        Sid    = \"JitDdb\"\n")
	b.WriteString("        Effect = \"Allow\"\n")
	b.WriteString("        Action = [\n")
	b.WriteString("          \"dynamodb:PutItem\",\n")
	b.WriteString("          \"dynamodb:GetItem\",\n")
	b.WriteString("          \"dynamodb:DeleteItem\",\n")
	b.WriteString("          \"dynamodb:UpdateItem\",\n")
	b.WriteString("          \"dynamodb:Scan\",\n")
	b.WriteString("        ]\n")
	fmt.Fprintf(&b, "        Resource = aws_dynamodb_table.%s.arn\n", table)
	b.WriteString("      },\n")
	b.WriteString("    ]\n")
	b.WriteString("  })\n")
	b.WriteString("}\n\n")

	fmt.Fprintf(&b, "resource \"aws_iam_role_policy_attachment\" %q {\n", policy)
	fmt.Fprintf(&b, "  role       = %q\n", p.KeycloakRole)
	fmt.Fprintf(&b, "  policy_arn = aws_iam_policy.%s.arn\n", policy)
	b.WriteString("}\n\n")

	// The JIT door SG id — point the Keycloak SPI's JIT_VPN_SG_ID env at this.
	fmt.Fprintf(&b, "output \"%s_jit_sg_id\" {\n", name)
	fmt.Fprintf(&b, "  value       = aws_security_group.%s.id\n", jitSG)
	b.WriteString("  description = \"Set the Keycloak JIT SPI env JIT_VPN_SG_ID to this.\"\n")
	b.WriteString("}\n")
	return b.String()
}
