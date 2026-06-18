package catalog

import (
	"context"
	"fmt"
	"strings"
)

// AttachToExistingALBSpec represents the specification of a component
// that attaches a sibling ScaleGroup (ASG) to an existing Application Load Balancer.
type AttachToExistingALBSpec struct {
	Name            string
	Region          string
	Provider        string
	ALBListenerARN  string
	HostHeader      string
	Port            int
	Protocol        string
	HealthCheckPath string
	HealthCheckPort string
	ScaleGroup      string
	Priority        int
	Network         string // Derived network VPC name
}

// AttachToExistingALBPlan represents the resolved plan of the attachment.
type AttachToExistingALBPlan struct {
	Provider        string `json:"provider"`
	CSP             string `json:"csp"`
	RegionName      string `json:"region_name"`
	CSPRegion       string `json:"csp_region"`
	Name            string `json:"name"`
	ALBListenerARN  string `json:"alb_listener_arn"`
	HostHeader      string `json:"host_header"`
	Port            int    `json:"port"`
	Protocol        string `json:"protocol"`
	HealthCheckPath string `json:"health_check_path"`
	HealthCheckPort string `json:"health_check_port"`
	ScaleGroup      string `json:"scale_group"`
	Priority        int    `json:"priority"`
	NetworkName     string `json:"network_name"`
}

// TranslateAttachToExistingALB validates and resolves the spec into a plan.
func TranslateAttachToExistingALB(ctx context.Context, cat RegionCatalog, spec AttachToExistingALBSpec) (AttachToExistingALBPlan, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return AttachToExistingALBPlan{}, fmt.Errorf("attach-to-existing-alb: name is required")
	}
	if strings.TrimSpace(spec.ALBListenerARN) == "" {
		return AttachToExistingALBPlan{}, fmt.Errorf("attach-to-existing-alb %q: alb_listener_arn is required", spec.Name)
	}
	if strings.TrimSpace(spec.ScaleGroup) == "" {
		return AttachToExistingALBPlan{}, fmt.Errorf("attach-to-existing-alb %q: scale_group (the target scale group) is required", spec.Name)
	}
	if spec.Port <= 0 {
		return AttachToExistingALBPlan{}, fmt.Errorf("attach-to-existing-alb %q: port must be > 0", spec.Name)
	}
	if spec.Priority <= 0 {
		return AttachToExistingALBPlan{}, fmt.Errorf("attach-to-existing-alb %q: priority must be > 0", spec.Name)
	}

	csp, ok := ProviderToCSP(spec.Provider)
	if !ok {
		return AttachToExistingALBPlan{}, fmt.Errorf("attach-to-existing-alb: unknown provider %q", spec.Provider)
	}
	if strings.ToLower(spec.Provider) != ProviderAWS {
		return AttachToExistingALBPlan{}, fmt.Errorf("attach-to-existing-alb is only supported on AWS, got provider %q", spec.Provider)
	}

	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return AttachToExistingALBPlan{}, err
	}

	proto := strings.ToLower(strings.TrimSpace(spec.Protocol))
	if proto == "" {
		proto = "http"
	}
	if proto != "http" && proto != "https" {
		return AttachToExistingALBPlan{}, fmt.Errorf("attach-to-existing-alb %q: invalid protocol %q (http | https)", spec.Name, spec.Protocol)
	}

	hcPath := spec.HealthCheckPath
	if hcPath == "" {
		hcPath = "/"
	}

	return AttachToExistingALBPlan{
		Provider:        ProviderAWS,
		CSP:             csp,
		RegionName:      row.RegionName,
		CSPRegion:       row.CSPRegion,
		Name:            spec.Name,
		ALBListenerARN:  spec.ALBListenerARN,
		HostHeader:      spec.HostHeader,
		Port:            spec.Port,
		Protocol:        proto,
		HealthCheckPath: hcPath,
		HealthCheckPort: spec.HealthCheckPort,
		ScaleGroup:      spec.ScaleGroup,
		Priority:        spec.Priority,
		NetworkName:     spec.Network,
	}, nil
}

// RenderAttachToExistingALBHCL renders the AWS HCL for the target group, listener rule, and ASG attachment.
func RenderAttachToExistingALBHCL(p AttachToExistingALBPlan) (string, error) {
	tgName := tfName(p.Name) + "_tg"
	ruleName := tfName(p.Name) + "_rule"
	attachName := tfName(p.Name) + "_attach"
	var b strings.Builder

	// 1. Target Group
	fmt.Fprintf(&b, "resource \"aws_lb_target_group\" %q {\n", tgName)
	fmt.Fprintf(&b, "  name        = %q\n", tfName(p.Name)+"-tg")
	fmt.Fprintf(&b, "  port        = %d\n", p.Port)
	fmt.Fprintf(&b, "  protocol    = %q\n", strings.ToUpper(p.Protocol))
	b.WriteString("  target_type = \"instance\"\n")
	if p.NetworkName != "" {
		fmt.Fprintf(&b, "  vpc_id      = aws_vpc.%s.id\n", tfName(p.NetworkName))
	}
	b.WriteString("  health_check {\n")
	fmt.Fprintf(&b, "    protocol            = %q\n", strings.ToUpper(p.Protocol))
	fmt.Fprintf(&b, "    path                = %q\n", p.HealthCheckPath)
	b.WriteString("    interval            = 30\n")
	b.WriteString("    healthy_threshold   = 3\n")
	b.WriteString("    unhealthy_threshold = 3\n")
	if p.HealthCheckPort != "" {
		fmt.Fprintf(&b, "    port                = %q\n", p.HealthCheckPort)
	}
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", p.Name)
	b.WriteString("}\n\n")

	// 2. Listener Rule
	fmt.Fprintf(&b, "resource \"aws_lb_listener_rule\" %q {\n", ruleName)
	fmt.Fprintf(&b, "  listener_arn = %q\n", p.ALBListenerARN)
	fmt.Fprintf(&b, "  priority     = %d\n", p.Priority)
	b.WriteString("  action {\n")
	b.WriteString("    type             = \"forward\"\n")
	fmt.Fprintf(&b, "    target_group_arn = aws_lb_target_group.%s.arn\n", tgName)
	b.WriteString("  }\n")
	b.WriteString("  condition {\n")
	b.WriteString("    host_header {\n")
	fmt.Fprintf(&b, "      values = [%q]\n", p.HostHeader)
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	b.WriteString("}\n\n")

	// 3. Autoscaling Attachment
	fmt.Fprintf(&b, "resource \"aws_autoscaling_attachment\" %q {\n", attachName)
	fmt.Fprintf(&b, "  autoscaling_group_name = aws_autoscaling_group.%s.name\n", asgResourceLabel(p.ScaleGroup))
	fmt.Fprintf(&b, "  lb_target_group_arn    = aws_lb_target_group.%s.arn\n", tgName)
	b.WriteString("}\n")

	return b.String(), nil
}
