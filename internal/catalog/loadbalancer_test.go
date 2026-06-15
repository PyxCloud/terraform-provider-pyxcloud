package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestTranslateLoadBalancerAWS asserts the resolved structured plan for AWS:
// catalog-resolved csp_region, multi-AZ zones from the region catalog, listeners
// sorted by port, the scale-group target wiring, and the aws_lb resource type.
func TestTranslateLoadBalancerAWS(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	// Dublin -> eu-west-1; listeners deliberately out of order to assert sorting.
	plan, err := TranslateLoadBalancer(context.Background(), cat, LoadBalancerSpec{
		Name:     "web-lb",
		Region:   "Dublin",
		Provider: "aws",
		Listeners: []LBListenerSpec{
			{Port: 443, Protocol: "https"},
			{Port: 80, Protocol: "http"},
		},
		Stickiness:    true,
		TargetKind:    "scale-group",
		TargetName:    "web",
		Network:       "production",
		Subnets:       []string{"production-subnet-1", "production-subnet-2", "production-subnet-3"},
		SecurityGroup: "production-web",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSPRegion != "eu-west-1" {
		t.Errorf("csp_region = %q, want eu-west-1", plan.CSPRegion)
	}
	if plan.ResourceType != "aws_lb" {
		t.Errorf("resource_type = %q, want aws_lb", plan.ResourceType)
	}
	if len(plan.Listeners) != 2 || plan.Listeners[0].Port != 80 || plan.Listeners[1].Port != 443 {
		t.Fatalf("listeners not sorted ascending by port: %+v", plan.Listeners)
	}
	if plan.Listeners[0].Protocol != LBProtoHTTP || plan.Listeners[1].Protocol != LBProtoHTTPS {
		t.Errorf("listener protocols wrong: %+v", plan.Listeners)
	}
	if !plan.Stickiness {
		t.Error("stickiness should be true")
	}
	if plan.TargetKind != LBTargetScaleGroup || plan.TargetName != "web" {
		t.Errorf("target = %q/%q, want scale-group/web", plan.TargetKind, plan.TargetName)
	}
	// Multi-AZ: three subnets -> three distinct zones.
	wantZones := []string{"eu-west-1a", "eu-west-1b", "eu-west-1c"}
	if len(plan.Zones) != 3 {
		t.Fatalf("want 3 zones for 3 subnets, got %v", plan.Zones)
	}
	for i, z := range wantZones {
		if plan.Zones[i] != z {
			t.Errorf("zone[%d] = %q, want %q", i, plan.Zones[i], z)
		}
	}
	// Health check defaults from the first listener (port 80, http) when unset.
	if plan.HealthCheck.Port != 80 || plan.HealthCheck.Protocol != LBProtoHTTP || plan.HealthCheck.Path != "/" {
		t.Errorf("health-check defaults wrong: %+v", plan.HealthCheck)
	}
	if plan.HealthCheck.IntervalSeconds != 30 || plan.HealthCheck.HealthyThreshold != 3 || plan.HealthCheck.UnhealthyThreshold != 3 {
		t.Errorf("health-check threshold defaults wrong: %+v", plan.HealthCheck)
	}
}

func TestTranslateLoadBalancerGCP(t *testing.T) {
	t.Parallel()
	plan, err := TranslateLoadBalancer(context.Background(), MustEmbedded(), LoadBalancerSpec{
		Name: "api-lb", Region: "Frankfurt", Provider: "gcp",
		Listeners:  []LBListenerSpec{{Port: 80, Protocol: "http"}},
		TargetKind: "scale-group", TargetName: "api",
		Network: "production", Subnets: []string{"production-subnet-1", "production-subnet-2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSPRegion != "europe-west3" {
		t.Errorf("csp_region = %q, want europe-west3", plan.CSPRegion)
	}
	if plan.ResourceType != "google_compute_forwarding_rule" {
		t.Errorf("resource_type = %q, want google_compute_forwarding_rule", plan.ResourceType)
	}
	wantZones := []string{"europe-west3-a", "europe-west3-b"}
	if len(plan.Zones) != 2 {
		t.Fatalf("want 2 zones, got %v", plan.Zones)
	}
	for i, z := range wantZones {
		if plan.Zones[i] != z {
			t.Errorf("zone[%d] = %q, want %q", i, plan.Zones[i], z)
		}
	}
}

func TestTranslateLoadBalancerDO(t *testing.T) {
	t.Parallel()
	plan, err := TranslateLoadBalancer(context.Background(), MustEmbedded(), LoadBalancerSpec{
		Name: "web-lb", Region: "Frankfurt", Provider: "digitalocean",
		Listeners:  []LBListenerSpec{{Port: 80, Protocol: "http"}, {Port: 443, Protocol: "https"}},
		Stickiness: true,
		TargetKind: "vm", TargetName: "web",
		Network: "production", Subnets: []string{"production-subnet-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "digitalocean_loadbalancer" {
		t.Errorf("resource_type = %q, want digitalocean_loadbalancer", plan.ResourceType)
	}
	// DO is region-scoped: no zones.
	if len(plan.Zones) != 0 {
		t.Errorf("DO should have no zones, got %v", plan.Zones)
	}
	if plan.CSPRegion != "fra1" {
		t.Errorf("csp_region = %q, want fra1", plan.CSPRegion)
	}
}

// TestLoadBalancerTargetKindDefaultsAndAliases asserts target-kind defaulting and
// the alias map (asg/scalegroup -> scale-group, virtual-machine/instance -> vm).
func TestLoadBalancerTargetKindDefaultsAndAliases(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	cases := []struct{ in, want string }{
		{"", LBTargetScaleGroup},
		{"scale-group", LBTargetScaleGroup},
		{"asg", LBTargetScaleGroup},
		{"scalegroup", LBTargetScaleGroup},
		{"vm", LBTargetVM},
		{"virtual-machine", LBTargetVM},
		{"instance", LBTargetVM},
	}
	for _, c := range cases {
		plan, err := TranslateLoadBalancer(context.Background(), cat, LoadBalancerSpec{
			Region: "Dublin", Provider: "aws",
			Listeners:  []LBListenerSpec{{Port: 80}},
			TargetKind: c.in, TargetName: "web",
		})
		if err != nil {
			t.Fatalf("target kind %q: %v", c.in, err)
		}
		if plan.TargetKind != c.want {
			t.Errorf("target kind %q -> %q, want %q", c.in, plan.TargetKind, c.want)
		}
	}
}

// TestLoadBalancerProtocolDefaultsAndAliases asserts listener protocol defaulting.
func TestLoadBalancerProtocolDefaultsAndAliases(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	cases := []struct {
		in   string
		want string
	}{
		{"", LBProtoHTTP},
		{"http", LBProtoHTTP},
		{"https", LBProtoHTTPS},
		{"ssl", LBProtoHTTPS},
		{"tcp", LBProtoTCP},
		{"l4", LBProtoTCP},
	}
	for _, c := range cases {
		plan, err := TranslateLoadBalancer(context.Background(), cat, LoadBalancerSpec{
			Region: "Dublin", Provider: "aws", TargetName: "web",
			Listeners: []LBListenerSpec{{Port: 8080, Protocol: c.in}},
		})
		if err != nil {
			t.Fatalf("proto %q: %v", c.in, err)
		}
		if plan.Listeners[0].Protocol != c.want {
			t.Errorf("proto %q -> %q, want %q", c.in, plan.Listeners[0].Protocol, c.want)
		}
	}
}

// TestLoadBalancerExplicitHealthCheck asserts an explicit health check overrides
// the listener defaults.
func TestLoadBalancerExplicitHealthCheck(t *testing.T) {
	t.Parallel()
	plan, err := TranslateLoadBalancer(context.Background(), MustEmbedded(), LoadBalancerSpec{
		Region: "Dublin", Provider: "aws", TargetName: "web",
		Listeners: []LBListenerSpec{{Port: 443, Protocol: "https"}},
		HealthCheck: LBHealthCheckSpec{
			Protocol: "http", Port: 8080, Path: "/healthz",
			IntervalSeconds: 15, HealthyThreshold: 5, UnhealthyThreshold: 2,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	hc := plan.HealthCheck
	if hc.Protocol != LBProtoHTTP || hc.Port != 8080 || hc.Path != "/healthz" {
		t.Errorf("explicit health check not honoured: %+v", hc)
	}
	if hc.IntervalSeconds != 15 || hc.HealthyThreshold != 5 || hc.UnhealthyThreshold != 2 {
		t.Errorf("explicit health-check thresholds not honoured: %+v", hc)
	}
}

// TestLoadBalancerTCPHealthCheckNoPath asserts a tcp health check carries no path.
func TestLoadBalancerTCPHealthCheckNoPath(t *testing.T) {
	t.Parallel()
	plan, err := TranslateLoadBalancer(context.Background(), MustEmbedded(), LoadBalancerSpec{
		Region: "Dublin", Provider: "aws", TargetName: "web",
		Listeners: []LBListenerSpec{{Port: 5432, Protocol: "tcp"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.HealthCheck.Protocol != LBProtoTCP || plan.HealthCheck.Path != "" {
		t.Errorf("tcp health check should carry no path: %+v", plan.HealthCheck)
	}
}

// TestLoadBalancerAWSConditionLimit asserts the AWS ALB <=5-condition-value quota
// is a hard plan-time error (never a silent truncation).
func TestLoadBalancerAWSConditionLimit(t *testing.T) {
	t.Parallel()
	_, err := TranslateLoadBalancer(context.Background(), MustEmbedded(), LoadBalancerSpec{
		Region: "Dublin", Provider: "aws", TargetName: "web",
		Listeners: []LBListenerSpec{
			{Port: 80, Protocol: "http", Conditions: []string{"/a", "/b", "/c", "/d", "/e", "/f"}},
		},
	})
	if err == nil {
		t.Fatal("expected condition-limit error for 6 conditions, got nil")
	}
	if !strings.Contains(err.Error(), "AWS ALB") || !strings.Contains(err.Error(), "5") {
		t.Errorf("error should mention the AWS ALB 5-value limit, got %v", err)
	}
	// Exactly 5 is fine.
	if _, err := TranslateLoadBalancer(context.Background(), MustEmbedded(), LoadBalancerSpec{
		Region: "Dublin", Provider: "aws", TargetName: "web",
		Listeners: []LBListenerSpec{
			{Port: 80, Protocol: "http", Conditions: []string{"/a", "/b", "/c", "/d", "/e"}},
		},
	}); err != nil {
		t.Errorf("5 conditions should be allowed, got %v", err)
	}
}

// TestLoadBalancerRegionNotFound asserts an unresolvable region is a hard error.
func TestLoadBalancerRegionNotFound(t *testing.T) {
	t.Parallel()
	_, err := TranslateLoadBalancer(context.Background(), MustEmbedded(), LoadBalancerSpec{
		Region: "Atlantis", Provider: "aws", TargetName: "web",
		Listeners: []LBListenerSpec{{Port: 80}},
	})
	if err == nil {
		t.Fatal("expected region-not-found error, got nil")
	}
	var nf ErrRegionNotFound
	if !errors.As(err, &nf) {
		t.Fatalf("expected ErrRegionNotFound, got %T: %v", err, err)
	}
}

func TestLoadBalancerValidation(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	cases := []struct {
		name string
		spec LoadBalancerSpec
	}{
		{"missing region", LoadBalancerSpec{Provider: "aws", Listeners: []LBListenerSpec{{Port: 80}}}},
		{"missing provider", LoadBalancerSpec{Region: "Dublin", Listeners: []LBListenerSpec{{Port: 80}}}},
		{"unknown provider", LoadBalancerSpec{Region: "Dublin", Provider: "vultr", Listeners: []LBListenerSpec{{Port: 80}}}},
		{"no listeners", LoadBalancerSpec{Region: "Dublin", Provider: "aws"}},
		{"bad listener port", LoadBalancerSpec{Region: "Dublin", Provider: "aws", Listeners: []LBListenerSpec{{Port: 0}}}},
		{"bad listener proto", LoadBalancerSpec{Region: "Dublin", Provider: "aws", Listeners: []LBListenerSpec{{Port: 80, Protocol: "grpc"}}}},
		{"bad target kind", LoadBalancerSpec{Region: "Dublin", Provider: "aws", Listeners: []LBListenerSpec{{Port: 80}}, TargetKind: "lambda"}},
		{"bad health port", LoadBalancerSpec{Region: "Dublin", Provider: "aws", Listeners: []LBListenerSpec{{Port: 80}}, HealthCheck: LBHealthCheckSpec{Port: 99999}}},
		{"bad health proto", LoadBalancerSpec{Region: "Dublin", Provider: "aws", Listeners: []LBListenerSpec{{Port: 80}}, HealthCheck: LBHealthCheckSpec{Protocol: "icmp"}}},
	}
	for _, c := range cases {
		if _, err := TranslateLoadBalancer(context.Background(), cat, c.spec); err == nil {
			t.Errorf("%s: expected validation error, got nil", c.name)
		}
	}
}

// TestRenderLoadBalancerAWS asserts the per-provider shaping: aws_lb +
// target_group + a listener per port, lb_cookie stickiness, multi-subnet wiring,
// the ASG target-group attachment, and ASCII output.
func TestRenderLoadBalancerAWS(t *testing.T) {
	t.Parallel()
	plan, err := TranslateLoadBalancer(context.Background(), MustEmbedded(), LoadBalancerSpec{
		Name: "web-lb", Region: "Dublin", Provider: "aws",
		Listeners:  []LBListenerSpec{{Port: 80, Protocol: "http"}, {Port: 443, Protocol: "https"}},
		Stickiness: true,
		TargetKind: "scale-group", TargetName: "web",
		Network:       "production",
		Subnets:       []string{"production-subnet-1", "production-subnet-2"},
		SecurityGroup: "production-web",
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderLoadBalancerHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "aws_internet_gateway" "web-lb_igw"`,
		`resource "aws_route_table" "web-lb_rt"`,
		`resource "aws_route_table_association" "web-lb_rta_1"`,
		`resource "aws_route_table_association" "web-lb_rta_2"`,
		`cidr_block = "0.0.0.0/0"`,
		`resource "aws_lb" "web-lb_lb"`,
		`load_balancer_type = "application"`,
		`internal           = false`,
		`depends_on = [aws_internet_gateway.web-lb_igw]`,
		`security_groups    = [aws_security_group.production-web.id]`,
		`subnets            = [aws_subnet.production_1.id, aws_subnet.production_2.id]`,
		`resource "aws_lb_target_group" "web-lb_tg"`,
		`target_type = "instance"`,
		`vpc_id      = aws_vpc.production.id`,
		`resource "aws_lb_listener" "web-lb_listener_80"`,
		`resource "aws_lb_listener" "web-lb_listener_443"`,
		`protocol          = "HTTP"`,
		`protocol          = "HTTPS"`,
		`target_group_arn = aws_lb_target_group.web-lb_tg.arn`,
		`type            = "lb_cookie"`,
		`resource "aws_autoscaling_attachment" "web-lb_attach"`,
		`autoscaling_group_name = aws_autoscaling_group.web_asg.name`,
		`lb_target_group_arn    = aws_lb_target_group.web-lb_tg.arn`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("aws LB HCL missing %q:\n%s", want, hcl)
		}
	}
	if !IsASCII(hcl) {
		t.Errorf("rendered HCL is not ASCII:\n%s", hcl)
	}
}

// TestRenderLoadBalancerAWSNoStickiness asserts no stickiness block when off.
func TestRenderLoadBalancerAWSNoStickiness(t *testing.T) {
	t.Parallel()
	plan, err := TranslateLoadBalancer(context.Background(), MustEmbedded(), LoadBalancerSpec{
		Name: "web-lb", Region: "Dublin", Provider: "aws",
		Listeners:  []LBListenerSpec{{Port: 80}},
		TargetName: "web", Network: "production", Subnets: []string{"production-subnet-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, _ := RenderLoadBalancerHCL(plan)
	if strings.Contains(hcl, "stickiness {") {
		t.Errorf("no stickiness expected:\n%s", hcl)
	}
}

// TestRenderLoadBalancerAWSVMTarget asserts a fixed-VM target wires a
// target-group attachment to the first instance (not an ASG attachment).
func TestRenderLoadBalancerAWSVMTarget(t *testing.T) {
	t.Parallel()
	plan, err := TranslateLoadBalancer(context.Background(), MustEmbedded(), LoadBalancerSpec{
		Name: "web-lb", Region: "Dublin", Provider: "aws",
		Listeners:  []LBListenerSpec{{Port: 80}},
		TargetKind: "vm", TargetName: "web",
		Network: "production", Subnets: []string{"production-subnet-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, _ := RenderLoadBalancerHCL(plan)
	if !strings.Contains(hcl, `resource "aws_lb_target_group_attachment" "web-lb_attach"`) {
		t.Errorf("vm target should emit a target_group_attachment:\n%s", hcl)
	}
	if !strings.Contains(hcl, `target_id        = aws_instance.web-1.id`) {
		t.Errorf("vm target should attach the first instance:\n%s", hcl)
	}
	if strings.Contains(hcl, "aws_autoscaling_attachment") {
		t.Errorf("vm target should NOT emit an ASG attachment:\n%s", hcl)
	}
}

func TestRenderLoadBalancerGCP(t *testing.T) {
	t.Parallel()
	plan, err := TranslateLoadBalancer(context.Background(), MustEmbedded(), LoadBalancerSpec{
		Name: "api-lb", Region: "Frankfurt", Provider: "gcp",
		Listeners:  []LBListenerSpec{{Port: 80, Protocol: "http"}},
		Stickiness: true,
		TargetKind: "scale-group", TargetName: "api",
		Network: "production", Subnets: []string{"production-subnet-1", "production-subnet-2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderLoadBalancerHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "google_compute_health_check" "api-lb_hc"`,
		`resource "google_compute_region_backend_service" "api-lb_be"`,
		`resource "google_compute_forwarding_rule" "api-lb_fr_80"`,
		`region                = "europe-west3"`,
		`health_checks         = [google_compute_health_check.api-lb_hc.id]`,
		`session_affinity      = "GENERATED_COOKIE"`,
		`group = google_compute_region_instance_group_manager.api_mig.instance_group`,
		`backend_service       = google_compute_region_backend_service.api-lb_be.id`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("gcp LB HCL missing %q:\n%s", want, hcl)
		}
	}
}

func TestRenderLoadBalancerDO(t *testing.T) {
	t.Parallel()
	plan, err := TranslateLoadBalancer(context.Background(), MustEmbedded(), LoadBalancerSpec{
		Name: "web-lb", Region: "Frankfurt", Provider: "digitalocean",
		Listeners:  []LBListenerSpec{{Port: 80, Protocol: "http"}, {Port: 443, Protocol: "https"}},
		Stickiness: true,
		TargetName: "web",
		Network:    "production", Subnets: []string{"production-subnet-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderLoadBalancerHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "digitalocean_loadbalancer" "web-lb"`,
		`region = "fra1"`,
		`vpc_uuid = digitalocean_vpc.production.id`,
		`entry_protocol  = "http"`,
		`entry_port      = 80`,
		`entry_protocol  = "https"`,
		`entry_port      = 443`,
		`healthcheck {`,
		`sticky_sessions {`,
		`droplet_tag = "pyxcloud"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("do LB HCL missing %q:\n%s", want, hcl)
		}
	}
}

// TestRenderLoadBalancerUnsupportedProvider asserts the renderer rejects an
// unknown provider (defence in depth for a hand-built plan).
func TestRenderLoadBalancerUnsupportedProvider(t *testing.T) {
	t.Parallel()
	if _, err := RenderLoadBalancerHCL(LoadBalancerPlan{Provider: "vultr"}); err == nil {
		t.Fatal("expected render error for unsupported provider, got nil")
	}
}
