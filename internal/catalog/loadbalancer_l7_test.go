package catalog

import (
	"context"
	"strings"
	"testing"
)

// l7Spec is a load-balancer spec with two layer-7 routing rules on the :443
// listener: an admin route gated to a VPN CIDR (high priority) and a public API
// route. Priorities are deliberately out of order to assert sorting.
func l7Spec(provider string) LoadBalancerSpec {
	return LoadBalancerSpec{
		Name: "web-lb", Region: "Frankfurt", Provider: provider,
		Network: "net", Subnets: []string{"a", "b"},
		TargetKind: "scale-group", TargetName: "web",
		Listeners: []LBListenerSpec{
			{Port: 443, Protocol: "https", Rules: []LBRoutingRule{
				{Priority: 200, HostHeaders: []string{"api.example.com"}, PathPatterns: []string{"/v1/*"}},
				{Priority: 100, HostHeaders: []string{"admin.example.com"}, AdminVPNCIDRs: []string{"10.8.0.0/24"}, TargetName: "admin"},
			}},
		},
	}
}

// TestTranslateLoadBalancerL7RulesSorted asserts routing rules are resolved and
// sorted by ascending priority for a deterministic plan.
func TestTranslateLoadBalancerL7RulesSorted(t *testing.T) {
	t.Parallel()
	plan, err := TranslateLoadBalancer(context.Background(), MustEmbedded(), l7Spec("aws"))
	if err != nil {
		t.Fatal(err)
	}
	rules := plan.Listeners[0].Rules
	if len(rules) != 2 {
		t.Fatalf("want 2 resolved rules, got %d", len(rules))
	}
	if rules[0].Priority != 100 || rules[1].Priority != 200 {
		t.Errorf("rules not sorted by priority: %d, %d", rules[0].Priority, rules[1].Priority)
	}
	if rules[0].TargetName != "admin" || len(rules[0].AdminVPNCIDRs) != 1 {
		t.Errorf("admin rule not resolved: %+v", rules[0])
	}
}

// TestRenderLoadBalancerAWSL7Rules asserts the ALB listener-rule HCL: per-rule
// aws_lb_listener_rule with priority, host/path conditions, and the admin-VPN
// source_ip gate.
func TestRenderLoadBalancerAWSL7Rules(t *testing.T) {
	t.Parallel()
	plan, err := TranslateLoadBalancer(context.Background(), MustEmbedded(), l7Spec("aws"))
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderLoadBalancerHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "aws_lb_listener_rule" "web-lb_listener_443_rule_100"`,
		`resource "aws_lb_listener_rule" "web-lb_listener_443_rule_200"`,
		`priority     = 100`,
		`priority     = 200`,
		`host_header {`,
		`values = ["admin.example.com"]`,
		`path_pattern {`,
		`values = ["/v1/*"]`,
		// Admin-VPN gate:
		`source_ip {`,
		`values = ["10.8.0.0/24"]`,
		// admin rule forwards to its own target group.
		`target_group_arn = aws_lb_target_group.admin_tg.arn`,
		// GAP-4 resolved: that per-host target group is now SYNTHESISED (not just
		// referenced) — a distinct aws_lb_target_group + ASG attachment per TargetName.
		`resource "aws_lb_target_group" "admin_tg"`,
		`resource "aws_autoscaling_attachment" "admin_attach"`,
		`autoscaling_group_name = aws_autoscaling_group.admin_asg.name`,
		`lb_target_group_arn    = aws_lb_target_group.admin_tg.arn`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("aws L7 HCL missing %q:\n%s", want, hcl)
		}
	}
	if !IsASCII(hcl) {
		t.Errorf("rendered HCL not ASCII")
	}
}

// TestRenderLoadBalancerDOL7Ingress asserts that on DigitalOcean the L7 rules
// render a DOKS Ingress (the forwarding_rule cannot express host/path/source-IP)
// and that the admin-VPN gate is preserved as the ingress-nginx source-range
// whitelist (documented constraint).
func TestRenderLoadBalancerDOL7Ingress(t *testing.T) {
	t.Parallel()
	plan, err := TranslateLoadBalancer(context.Background(), MustEmbedded(), l7Spec("digitalocean"))
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderLoadBalancerHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "kubernetes_manifest" "web-lb_ingress"`,
		`kind       = "Ingress"`,
		`host = "admin.example.com"`,
		`host = "api.example.com"`,
		`path     = "/v1"`,
		// Admin-VPN gate preserved as the ingress source-range whitelist.
		`"nginx.ingress.kubernetes.io/whitelist-source-range" = "10.8.0.0/24"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("do L7 ingress HCL missing %q:\n%s", want, hcl)
		}
	}
	// A DO LB WITHOUT rules must NOT emit an ingress (back-compat).
	noRules, _ := TranslateLoadBalancer(context.Background(), MustEmbedded(), LoadBalancerSpec{
		Name: "plain", Region: "Frankfurt", Provider: "digitalocean",
		Listeners: []LBListenerSpec{{Port: 80, Protocol: "http"}},
	})
	plainHCL, _ := RenderLoadBalancerHCL(noRules)
	if strings.Contains(plainHCL, "kubernetes_manifest") {
		t.Errorf("plain DO LB must not emit an ingress:\n%s", plainHCL)
	}
}

// TestLoadBalancerL7RuleValidation asserts the hard plan-time errors: a rule with
// no host/path match, an out-of-range/duplicate priority, and the AWS combined
// condition-value quota that counts the admin-VPN CIDRs.
func TestLoadBalancerL7RuleValidation(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	base := func(rules []LBRoutingRule) LoadBalancerSpec {
		return LoadBalancerSpec{
			Name: "lb", Region: "Frankfurt", Provider: "aws",
			Listeners: []LBListenerSpec{{Port: 443, Protocol: "https", Rules: rules}},
		}
	}
	cases := []struct {
		name  string
		rules []LBRoutingRule
		want  string
	}{
		{"no-condition", []LBRoutingRule{{Priority: 10}}, "at least one host_header or path_pattern"},
		{"bad-priority", []LBRoutingRule{{Priority: 0, HostHeaders: []string{"a.com"}}}, "out of range"},
		{"dup-priority", []LBRoutingRule{
			{Priority: 5, HostHeaders: []string{"a.com"}},
			{Priority: 5, HostHeaders: []string{"b.com"}},
		}, "duplicate routing-rule priority"},
		{"quota-with-gate", []LBRoutingRule{{
			Priority:      9,
			HostHeaders:   []string{"a.com", "b.com", "c.com"},
			PathPatterns:  []string{"/x"},
			AdminVPNCIDRs: []string{"10.0.0.0/8", "192.168.0.0/16"},
		}}, "exceeding the AWS ALB limit"},
	}
	for _, c := range cases {
		if _, err := TranslateLoadBalancer(context.Background(), cat, base(c.rules)); err == nil ||
			!strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want error containing %q, got %v", c.name, c.want, err)
		}
	}
}
