package provider

import (
	"context"
	"testing"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/catalog"
	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/client"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestTopologySchemaUsesPyxTypedComponentBlocks(t *testing.T) {
	t.Parallel()
	r := NewTopologyResource()
	resp := &fwresource.SchemaResponse{}
	r.Schema(context.Background(), fwresource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("topology schema diagnostics: %+v", resp.Diagnostics)
	}

	if _, ok := resp.Schema.Attributes["components"]; ok {
		t.Fatal("schema must not expose generic components block")
	}
	for _, componentType := range pyxComponentTypes {
		blockName := componentType.BlockName
		block, ok := resp.Schema.Attributes[blockName].(schema.ListNestedAttribute)
		if !ok {
			t.Fatalf("%s schema = %T, want schema.ListNestedAttribute", blockName, resp.Schema.Attributes[blockName])
		}
		attrs := block.NestedObject.Attributes
		for _, name := range []string{"path", "name", "count", "architecture", "cpu", "ram", "os_name", "min", "max", "desired", "health"} {
			if _, ok := attrs[name]; !ok {
				t.Errorf("%s missing flat component attribute %q", blockName, name)
			}
		}
		if _, ok := attrs["type"]; ok {
			t.Errorf("%s must not expose redundant type attribute", blockName)
		}
	}
}

func TestTopologyModelToTopologyUsesPyxTypedBlocks(t *testing.T) {
	t.Parallel()
	topo := modelToTopology(topologyModel{
		Name:     types.StringValue("production"),
		Provider: types.StringValue("aws"),
		Region:   types.StringValue("Dublin"),
		PyxAutoscaleVirtualMachineGroup: []componentModel{{
			Path:         types.StringValue("/0/Europe/0/Web-Net/0/app"),
			Name:         types.StringValue("app"),
			Count:        types.Int64Value(3),
			Architecture: types.StringValue("x86_64"),
			CPU:          types.StringValue("2"),
			RAM:          types.StringValue("4"),
			OSName:       types.StringValue("ubuntu"),
		}},
	})
	if len(topo.Components) != 1 {
		t.Fatalf("components = %d, want 1", len(topo.Components))
	}
	comp := topo.Components[0]
	if comp.Type != "virtual-machine-scale-group" {
		t.Errorf("type = %q", comp.Type)
	}
	if comp.Path != "/0/Europe/0/Web-Net/0/app" {
		t.Errorf("path = %q", comp.Path)
	}
	if comp.VM == nil {
		t.Fatal("expected flat VM fields to populate client VM")
	}
	if comp.VM.Architecture != "x86_64" || comp.VM.CPU != "2" || comp.VM.RAM != "4" || comp.VM.OS != "ubuntu" {
		t.Errorf("vm = %+v", comp.VM)
	}
}

func TestTopologyToModelDistributesCanonicalComponentsToPyxBlocks(t *testing.T) {
	t.Parallel()
	model := topologyToModel(client.Topology{
		Components: []client.Component{{
			Name: "db", Type: "managed-database", Path: "/0/Europe/0/DB-Net/0/app-db",
		}},
	})
	if len(model.PyxDatabase) != 1 {
		t.Fatalf("pyx_database count = %d, want 1", len(model.PyxDatabase))
	}
	if model.PyxDatabase[0].Name.ValueString() != "db" {
		t.Errorf("pyx_database name = %q", model.PyxDatabase[0].Name.ValueString())
	}
}

func TestTopologyComponentBlockHasNoLegacyNestedBlocks(t *testing.T) {
	t.Parallel()
	r := NewTopologyResource()
	resp := &fwresource.SchemaResponse{}
	r.Schema(context.Background(), fwresource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("topology schema diagnostics: %+v", resp.Diagnostics)
	}
	block := resp.Schema.Attributes["pyx_virtual_machine"].(schema.ListNestedAttribute)
	attrs := block.NestedObject.Attributes
	for _, name := range []string{"vm", "scale_group"} {
		if _, ok := attrs[name]; !ok {
			continue
		}
		t.Errorf("component schema must not expose nested %s block", name)
	}
}

// TestResourceTranslateNetwork exercises the resource's network translation
// wiring end-to-end through the embedded catalog.
func TestResourceTranslateNetwork(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}

	m := topologyModel{
		Name:     types.StringValue("production"),
		Provider: types.StringValue("aws"),
		Region:   types.StringValue("Dublin"),
		Network: &networkModel{
			CIDR:    types.StringValue("10.0.0.0/16"),
			Subnets: []types.String{types.StringValue("10.0.1.0/24"), types.StringValue("10.0.2.0/24")},
		},
	}

	plan, err := r.translateNetwork(context.Background(), m)
	if err != nil {
		t.Fatalf("translateNetwork: %v", err)
	}
	if plan == nil {
		t.Fatal("expected a network plan, got nil")
	}
	if plan.CSPRegion.ValueString() != "eu-west-1" {
		t.Errorf("csp_region = %q, want eu-west-1", plan.CSPRegion.ValueString())
	}
	if plan.ResourceType.ValueString() != "aws_vpc" {
		t.Errorf("resource_type = %q, want aws_vpc", plan.ResourceType.ValueString())
	}
	if len(plan.Subnets) != 2 {
		t.Fatalf("want 2 subnets, got %d", len(plan.Subnets))
	}
	if plan.Subnets[0].Zone.ValueString() != "eu-west-1a" {
		t.Errorf("subnet 0 zone = %q, want eu-west-1a", plan.Subnets[0].Zone.ValueString())
	}
}

// TestResourceTranslateNetworkNil returns no plan when no network is declared.
func TestResourceTranslateNetworkNil(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}
	plan, err := r.translateNetwork(context.Background(), topologyModel{
		Provider: types.StringValue("aws"), Region: types.StringValue("Dublin"),
	})
	if err != nil {
		t.Fatalf("translateNetwork: %v", err)
	}
	if plan != nil {
		t.Errorf("expected nil plan when no network declared, got %+v", plan)
	}
}

// TestResourceTranslateNetworkMissingCatalogRegion surfaces a hard error.
func TestResourceTranslateNetworkMissingCatalogRegion(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}
	_, err := r.translateNetwork(context.Background(), topologyModel{
		Name:     types.StringValue("x"),
		Provider: types.StringValue("digitalocean"),
		Region:   types.StringValue("Dublin"), // no DO region for Dublin
		Network:  &networkModel{CIDR: types.StringValue("10.0.0.0/16")},
	})
	if err == nil {
		t.Fatal("expected hard error for Dublin/digitalocean, got nil")
	}
}

// TestResourceTranslateSecurityGroup exercises the SG translation wiring end-to-
// end through the embedded catalog (pd-TF-SG).
func TestResourceTranslateSecurityGroup(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}

	m := topologyModel{
		Name:     types.StringValue("production"),
		Provider: types.StringValue("aws"),
		Region:   types.StringValue("Dublin"),
		Network:  &networkModel{CIDR: types.StringValue("10.0.0.0/16")},
		SecurityGroup: &securityGroupModel{
			Description: types.StringValue("web édge"), // non-ASCII -> sanitised
			Expose:      []types.Int64{types.Int64Value(80), types.Int64Value(443)},
			Rules: []securityRuleModel{{
				Direction: types.StringValue("ingress"),
				Protocol:  types.StringValue("tcp"),
				FromPort:  types.Int64Value(22),
				ToPort:    types.Int64Value(22),
				CIDRs:     []types.String{types.StringValue("10.0.0.0/16")},
				SourceSG:  types.StringValue(""),
			}},
		},
	}

	plan, err := r.translateSecurityGroup(context.Background(), m)
	if err != nil {
		t.Fatalf("translateSecurityGroup: %v", err)
	}
	if plan == nil {
		t.Fatal("expected a security-group plan, got nil")
	}
	if plan.CSPRegion.ValueString() != "eu-west-1" {
		t.Errorf("csp_region = %q, want eu-west-1", plan.CSPRegion.ValueString())
	}
	if plan.ResourceType.ValueString() != "aws_security_group" {
		t.Errorf("resource_type = %q, want aws_security_group", plan.ResourceType.ValueString())
	}
	if !catalog.IsASCII(plan.Description.ValueString()) {
		t.Errorf("description not ASCII-sanitised: %q", plan.Description.ValueString())
	}
	if plan.NetworkName.ValueString() != "production" {
		t.Errorf("network_name = %q, want production", plan.NetworkName.ValueString())
	}
	// 2 expose + 1 explicit = 3 rules.
	if len(plan.Rules) != 3 {
		t.Fatalf("want 3 rules, got %d", len(plan.Rules))
	}
}

// TestResourceTranslateSecurityGroupNil returns no plan when none declared.
func TestResourceTranslateSecurityGroupNil(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}
	plan, err := r.translateSecurityGroup(context.Background(), topologyModel{
		Provider: types.StringValue("aws"), Region: types.StringValue("Dublin"),
	})
	if err != nil {
		t.Fatalf("translateSecurityGroup: %v", err)
	}
	if plan != nil {
		t.Errorf("expected nil plan when no security_group declared, got %+v", plan)
	}
}

// TestResourceTranslateVM exercises the VM translation wiring end-to-end through
// the embedded catalog (pd-TF-EC2-VM): instance type from the virtual_machine
// catalog, image from the OS catalog, placement wired to the sibling subnet/SG.
func TestResourceTranslateVM(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}

	m := topologyModel{
		Name:     types.StringValue("production"),
		Provider: types.StringValue("aws"),
		Region:   types.StringValue("Dublin"),
		Network: &networkModel{
			CIDR:    types.StringValue("10.0.0.0/16"),
			Subnets: []types.String{types.StringValue("10.0.1.0/24")},
		},
		SecurityGroup: &securityGroupModel{
			Name:   types.StringValue("production-web"),
			Expose: []types.Int64{types.Int64Value(80)},
		},
		VirtualMachine: &virtualMachineModel{
			Architecture: types.StringValue("x86_64"),
			CPU:          types.Int64Value(2),
			RAM:          types.Int64Value(4),
			OS:           types.StringValue("ubuntu"),
			Count:        types.Int64Value(2),
		},
	}

	plan, err := r.translateVM(context.Background(), m)
	if err != nil {
		t.Fatalf("translateVM: %v", err)
	}
	if plan == nil {
		t.Fatal("expected a VM plan, got nil")
	}
	if plan.CSPRegion.ValueString() != "eu-west-1" {
		t.Errorf("csp_region = %q, want eu-west-1", plan.CSPRegion.ValueString())
	}
	if plan.InstanceType.ValueString() != "t3.medium" {
		t.Errorf("instance_type = %q, want t3.medium", plan.InstanceType.ValueString())
	}
	if plan.ResourceType.ValueString() != "aws_instance" {
		t.Errorf("resource_type = %q, want aws_instance", plan.ResourceType.ValueString())
	}
	if plan.SubnetName.ValueString() != "production-subnet-1" {
		t.Errorf("subnet_name = %q, want production-subnet-1", plan.SubnetName.ValueString())
	}
	if plan.SecurityGroup.ValueString() != "production-web" {
		t.Errorf("security_group = %q, want production-web", plan.SecurityGroup.ValueString())
	}
	if len(plan.Instances) != 2 {
		t.Fatalf("want 2 instances for count=2, got %d", len(plan.Instances))
	}
}

// TestResourceTranslateVMNil returns no plan when none declared.
func TestResourceTranslateVMNil(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}
	plan, err := r.translateVM(context.Background(), topologyModel{
		Provider: types.StringValue("aws"), Region: types.StringValue("Dublin"),
	})
	if err != nil {
		t.Fatalf("translateVM: %v", err)
	}
	if plan != nil {
		t.Errorf("expected nil plan when no virtual_machine declared, got %+v", plan)
	}
}

// TestResourceTranslateVMSKUNoMatch surfaces a hard plan-time error.
func TestResourceTranslateVMSKUNoMatch(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}
	_, err := r.translateVM(context.Background(), topologyModel{
		Name:     types.StringValue("x"),
		Provider: types.StringValue("aws"),
		Region:   types.StringValue("Dublin"),
		VirtualMachine: &virtualMachineModel{
			CPU: types.Int64Value(999), RAM: types.Int64Value(9999),
		},
	})
	if err == nil {
		t.Fatal("expected hard SKU no-match error, got nil")
	}
}

// TestResourceTranslateScaleGroup exercises the scale-group translation wiring
// end-to-end through the embedded catalog (pd-TF-ASG): instance type from the
// virtual_machine catalog (reused VM SKU), multi-AZ spread across the network's
// subnets, min/max/desired + health wired into the plan.
func TestResourceTranslateScaleGroup(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}

	m := topologyModel{
		Name:     types.StringValue("production"),
		Provider: types.StringValue("aws"),
		Region:   types.StringValue("Dublin"),
		Network: &networkModel{
			CIDR: types.StringValue("10.0.0.0/16"),
			Subnets: []types.String{
				types.StringValue("10.0.1.0/24"),
				types.StringValue("10.0.2.0/24"),
				types.StringValue("10.0.3.0/24"),
			},
		},
		SecurityGroup: &securityGroupModel{
			Name:   types.StringValue("production-web"),
			Expose: []types.Int64{types.Int64Value(80)},
		},
		ScaleGroup: &scaleGroupModel{
			Architecture: types.StringValue("x86_64"),
			CPU:          types.Int64Value(2),
			RAM:          types.Int64Value(4),
			OS:           types.StringValue("ubuntu"),
			Min:          types.Int64Value(2),
			Max:          types.Int64Value(6),
			Desired:      types.Int64Value(3),
			Health:       types.StringValue("elb"),
		},
	}

	plan, err := r.translateScaleGroup(context.Background(), m)
	if err != nil {
		t.Fatalf("translateScaleGroup: %v", err)
	}
	if plan == nil {
		t.Fatal("expected a scale-group plan, got nil")
	}
	if plan.CSPRegion.ValueString() != "eu-west-1" {
		t.Errorf("csp_region = %q, want eu-west-1", plan.CSPRegion.ValueString())
	}
	if plan.InstanceType.ValueString() != "t3.medium" {
		t.Errorf("instance_type = %q, want t3.medium (reused VM SKU)", plan.InstanceType.ValueString())
	}
	if plan.ResourceType.ValueString() != "aws_autoscaling_group" {
		t.Errorf("resource_type = %q, want aws_autoscaling_group", plan.ResourceType.ValueString())
	}
	if plan.Min.ValueInt64() != 2 || plan.Max.ValueInt64() != 6 || plan.Desired.ValueInt64() != 3 {
		t.Errorf("bounds = %d/%d/%d, want 2/6/3", plan.Min.ValueInt64(), plan.Max.ValueInt64(), plan.Desired.ValueInt64())
	}
	if plan.Health.ValueString() != "elb" {
		t.Errorf("health = %q, want elb", plan.Health.ValueString())
	}
	if len(plan.SubnetNames) != 3 || len(plan.Zones) != 3 {
		t.Errorf("want 3-way multi-AZ spread, got %d subnets / %d zones", len(plan.SubnetNames), len(plan.Zones))
	}
	if plan.SecurityGroup.ValueString() != "production-web" {
		t.Errorf("security_group = %q, want production-web", plan.SecurityGroup.ValueString())
	}
}

// TestResourceTranslateScaleGroupNil returns no plan when none declared.
func TestResourceTranslateScaleGroupNil(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}
	plan, err := r.translateScaleGroup(context.Background(), topologyModel{
		Provider: types.StringValue("aws"), Region: types.StringValue("Dublin"),
	})
	if err != nil {
		t.Fatalf("translateScaleGroup: %v", err)
	}
	if plan != nil {
		t.Errorf("expected nil plan when no scale_group declared, got %+v", plan)
	}
}

// TestResourceTranslateScaleGroupDO asserts a DigitalOcean scale-group maps to
// DO's native droplet-autoscale primitive (the AWS->DO migration keystone)
// rather than a DOKS cluster: digitalocean_droplet_autoscale is a lift-and-shift
// of the AWS ASG (VM+systemd) carrying per-instance user_data (the durable
// services need it), matching the live estate, with self-heal bounds preserved.
func TestResourceTranslateScaleGroupDO(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}
	plan, err := r.translateScaleGroup(context.Background(), topologyModel{
		Name:     types.StringValue("x"),
		Provider: types.StringValue("digitalocean"),
		Region:   types.StringValue("Frankfurt"),
		ScaleGroup: &scaleGroupModel{
			CPU: types.Int64Value(2), RAM: types.Int64Value(4),
			Min: types.Int64Value(1), Max: types.Int64Value(3),
		},
	})
	if err != nil {
		t.Fatalf("DO scale-group should map to droplet_autoscale, got error: %v", err)
	}
	if plan == nil {
		t.Fatal("expected a droplet_autoscale plan for DO scale-group, got nil")
	}
	if plan.ResourceType.ValueString() != "digitalocean_droplet_autoscale" {
		t.Errorf("resource_type = %q, want digitalocean_droplet_autoscale", plan.ResourceType.ValueString())
	}
}

// TestResourceTranslateLoadBalancer exercises the LB translation wiring (pd-TF-LB)
// end-to-end: listeners + health check + stickiness resolved, multi-AZ spread
// across the network's subnets, the security-group attached, and — crucially —
// the target defaulting to the sibling scale-group when target_name is omitted.
func TestResourceTranslateLoadBalancer(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}

	m := topologyModel{
		Name:     types.StringValue("production"),
		Provider: types.StringValue("aws"),
		Region:   types.StringValue("Dublin"),
		Network: &networkModel{
			CIDR: types.StringValue("10.0.0.0/16"),
			Subnets: []types.String{
				types.StringValue("10.0.1.0/24"),
				types.StringValue("10.0.2.0/24"),
			},
		},
		SecurityGroup: &securityGroupModel{
			Name:   types.StringValue("production-web"),
			Expose: []types.Int64{types.Int64Value(80)},
		},
		ScaleGroup: &scaleGroupModel{
			Name: types.StringValue("web"),
			CPU:  types.Int64Value(2), RAM: types.Int64Value(4),
			Min: types.Int64Value(2), Max: types.Int64Value(6),
		},
		LoadBalancer: &loadBalancerModel{
			Listeners: []lbListenerModel{
				{Port: types.Int64Value(443), Protocol: types.StringValue("https")},
				{Port: types.Int64Value(80), Protocol: types.StringValue("http")},
			},
			Stickiness: types.BoolValue(true),
		},
	}

	plan, err := r.translateLoadBalancer(context.Background(), m)
	if err != nil {
		t.Fatalf("translateLoadBalancer: %v", err)
	}
	if plan == nil {
		t.Fatal("expected a load-balancer plan, got nil")
	}
	if plan.CSPRegion.ValueString() != "eu-west-1" {
		t.Errorf("csp_region = %q, want eu-west-1", plan.CSPRegion.ValueString())
	}
	if plan.ResourceType.ValueString() != "aws_lb" {
		t.Errorf("resource_type = %q, want aws_lb", plan.ResourceType.ValueString())
	}
	// Target defaulted to the sibling scale-group.
	if plan.TargetKind.ValueString() != "scale-group" || plan.TargetName.ValueString() != "web" {
		t.Errorf("target = %q/%q, want scale-group/web", plan.TargetKind.ValueString(), plan.TargetName.ValueString())
	}
	if len(plan.Listeners) != 2 || plan.Listeners[0].Port.ValueInt64() != 80 || plan.Listeners[1].Port.ValueInt64() != 443 {
		t.Errorf("listeners not sorted ascending: %+v", plan.Listeners)
	}
	if !plan.Stickiness.ValueBool() {
		t.Error("stickiness should be true")
	}
	if len(plan.SubnetNames) != 2 || len(plan.Zones) != 2 {
		t.Errorf("want 2-way multi-AZ spread, got %d subnets / %d zones", len(plan.SubnetNames), len(plan.Zones))
	}
	if plan.SecurityGroup.ValueString() != "production-web" {
		t.Errorf("security_group = %q, want production-web", plan.SecurityGroup.ValueString())
	}
	if plan.HealthCheck == nil || plan.HealthCheck.Port.ValueInt64() != 80 {
		t.Errorf("health-check should default to first listener port 80, got %+v", plan.HealthCheck)
	}
}

// TestResourceTranslateLoadBalancerNil returns no plan when none declared.
func TestResourceTranslateLoadBalancerNil(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}
	plan, err := r.translateLoadBalancer(context.Background(), topologyModel{
		Provider: types.StringValue("aws"), Region: types.StringValue("Dublin"),
	})
	if err != nil {
		t.Fatalf("translateLoadBalancer: %v", err)
	}
	if plan != nil {
		t.Errorf("expected nil plan when no load_balancer declared, got %+v", plan)
	}
}

// TestResourceTranslateManagedDatabase exercises the resource's managed-database
// translation wiring end-to-end through the embedded catalog: catalog-resolved
// DB class, production-safe defaults, multi-AZ spread, and SG/network wiring.
func TestResourceTranslateManagedDatabase(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}

	m := topologyModel{
		Name:     types.StringValue("production"),
		Provider: types.StringValue("aws"),
		Region:   types.StringValue("Frankfurt"), // AWS -> eu-central-1
		Network: &networkModel{
			CIDR: types.StringValue("10.0.0.0/16"),
			Subnets: []types.String{
				types.StringValue("10.0.1.0/24"),
				types.StringValue("10.0.2.0/24"),
			},
		},
		SecurityGroup: &securityGroupModel{
			Name:   types.StringValue("production-db"),
			Expose: []types.Int64{types.Int64Value(5432)},
		},
		ManagedDatabase: &managedDatabaseModel{
			Engine:    types.StringValue("postgres"),
			CPU:       types.Int64Value(2),
			RAM:       types.Int64Value(4),
			StorageGB: types.Int64Value(50),
			HA:        types.BoolValue(true),
			Encrypted: types.BoolValue(true),
		},
	}

	plan, err := r.translateManagedDatabase(context.Background(), m)
	if err != nil {
		t.Fatalf("translateManagedDatabase: %v", err)
	}
	if plan == nil {
		t.Fatal("expected a managed-database plan, got nil")
	}
	if plan.CSPRegion.ValueString() != "eu-central-1" {
		t.Errorf("csp_region = %q, want eu-central-1", plan.CSPRegion.ValueString())
	}
	if plan.DBClass.ValueString() != "db.t3.medium" {
		t.Errorf("db_class = %q, want db.t3.medium", plan.DBClass.ValueString())
	}
	if plan.ResourceType.ValueString() != "aws_db_instance" {
		t.Errorf("resource_type = %q, want aws_db_instance", plan.ResourceType.ValueString())
	}
	// Production-safe defaults when the flags are unset (null).
	if !plan.DeletionProtection.ValueBool() {
		t.Error("deletion_protection should default to true")
	}
	if plan.SkipFinalSnapshot.ValueBool() {
		t.Error("skip_final_snapshot should default to false")
	}
	if len(plan.SubnetNames) != 2 || len(plan.Zones) != 2 {
		t.Errorf("want 2-way multi-AZ spread, got %d subnets / %d zones", len(plan.SubnetNames), len(plan.Zones))
	}
	if plan.SecurityGroup.ValueString() != "production-db" {
		t.Errorf("security_group = %q, want production-db", plan.SecurityGroup.ValueString())
	}
}

// TestResourceTranslateManagedDatabaseTestOverride asserts the test-only override
// flips the production-safe defaults through the resource wiring.
func TestResourceTranslateManagedDatabaseTestOverride(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}
	m := topologyModel{
		Name: types.StringValue("production"), Provider: types.StringValue("aws"),
		Region: types.StringValue("Frankfurt"),
		ManagedDatabase: &managedDatabaseModel{
			Engine: types.StringValue("postgres"), CPU: types.Int64Value(2), RAM: types.Int64Value(4),
			DeletionProtection: types.BoolValue(false),
			SkipFinalSnapshot:  types.BoolValue(true),
		},
	}
	plan, err := r.translateManagedDatabase(context.Background(), m)
	if err != nil {
		t.Fatalf("translateManagedDatabase: %v", err)
	}
	if plan.DeletionProtection.ValueBool() {
		t.Error("test override should disable deletion_protection")
	}
	if !plan.SkipFinalSnapshot.ValueBool() {
		t.Error("test override should enable skip_final_snapshot")
	}
}

// TestResourceTranslateManagedDatabaseNil returns no plan when none declared.
func TestResourceTranslateManagedDatabaseNil(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}
	plan, err := r.translateManagedDatabase(context.Background(), topologyModel{
		Provider: types.StringValue("aws"), Region: types.StringValue("Frankfurt"),
	})
	if err != nil {
		t.Fatalf("translateManagedDatabase: %v", err)
	}
	if plan != nil {
		t.Errorf("expected nil plan when no managed_database declared, got %+v", plan)
	}
}

// TestResourceDataSafetyGuardWiring asserts the resource-level data-safety guard
// helper (dbPlanModelToCatalog + CheckManagedDatabaseDataSafety) blocks a
// force-replacing encryption flip between a prior state plan and a new plan — the
// exact diff ModifyPlan/Update perform on an UPDATE.
func TestResourceDataSafetyGuardWiring(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}
	base := topologyModel{
		Name: types.StringValue("production"), Provider: types.StringValue("aws"),
		Region: types.StringValue("Frankfurt"),
		ManagedDatabase: &managedDatabaseModel{
			Engine: types.StringValue("postgres"), CPU: types.Int64Value(2), RAM: types.Int64Value(4),
			Encrypted: types.BoolValue(false),
		},
	}
	priorPlan, err := r.translateManagedDatabase(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}

	// New plan: encryption enabled on the EXISTING DB -> force-replace -> blocked.
	next := base
	next.ManagedDatabase = &managedDatabaseModel{
		Engine: types.StringValue("postgres"), CPU: types.Int64Value(2), RAM: types.Int64Value(4),
		Encrypted: types.BoolValue(true),
	}
	nextPlan, err := r.translateManagedDatabase(context.Background(), next)
	if err != nil {
		t.Fatal(err)
	}

	derr := catalog.CheckManagedDatabaseDataSafety(
		dbPlanModelToCatalog(priorPlan),
		dbPlanModelToCatalog(nextPlan),
	)
	if derr == nil {
		t.Fatal("expected the data-safety guard to block the encryption flip, got nil")
	}
	// Fresh create (nil prior) must be allowed.
	if err := catalog.CheckManagedDatabaseDataSafety(nil, dbPlanModelToCatalog(nextPlan)); err != nil {
		t.Errorf("fresh create should be safe, got %v", err)
	}
}

// TestResourceTranslateObjectStorage exercises the resource's object-storage
// translation wiring end-to-end: catalog-resolved location, globally-unique-safe
// bucket name, private-by-default, and the production-safe force_destroy default.
func TestResourceTranslateObjectStorage(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}

	m := topologyModel{
		Name:     types.StringValue("production"),
		Provider: types.StringValue("aws"),
		Region:   types.StringValue("Frankfurt"), // AWS -> eu-central-1
		ObjectStorage: &objectStorageModel{
			Name:       types.StringValue("app-assets"),
			Versioning: types.BoolValue(true),
		},
	}

	plan, err := r.translateObjectStorage(context.Background(), m)
	if err != nil {
		t.Fatalf("translateObjectStorage: %v", err)
	}
	if plan == nil {
		t.Fatal("expected an object-storage plan, got nil")
	}
	if plan.CSPRegion.ValueString() != "eu-central-1" {
		t.Errorf("csp_region = %q, want eu-central-1", plan.CSPRegion.ValueString())
	}
	if plan.ResourceType.ValueString() != "aws_s3_bucket" {
		t.Errorf("resource_type = %q, want aws_s3_bucket", plan.ResourceType.ValueString())
	}
	// PRIVATE BY DEFAULT when public is unset.
	if plan.Public.ValueBool() {
		t.Error("public should default to false (private-by-default)")
	}
	if !plan.Versioning.ValueBool() {
		t.Error("versioning should be carried")
	}
	// Production-safe default: force_destroy false unless overridden.
	if plan.ForceDestroy.ValueBool() {
		t.Error("force_destroy should default to false")
	}
}

// TestResourceTranslateObjectStorageForceDestroyOverride asserts the test-only
// override flips force_destroy through the resource wiring.
func TestResourceTranslateObjectStorageForceDestroyOverride(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}
	m := topologyModel{
		Name: types.StringValue("production"), Provider: types.StringValue("aws"),
		Region: types.StringValue("Frankfurt"),
		ObjectStorage: &objectStorageModel{
			Name:         types.StringValue("app-assets"),
			ForceDestroy: types.BoolValue(true),
		},
	}
	plan, err := r.translateObjectStorage(context.Background(), m)
	if err != nil {
		t.Fatalf("translateObjectStorage: %v", err)
	}
	if !plan.ForceDestroy.ValueBool() {
		t.Error("test override should enable force_destroy")
	}
}

// TestResourceTranslateObjectStorageNil returns no plan when none declared.
func TestResourceTranslateObjectStorageNil(t *testing.T) {
	t.Parallel()
	r := &topologyResource{catalog: catalog.MustEmbedded()}
	plan, err := r.translateObjectStorage(context.Background(), topologyModel{
		Provider: types.StringValue("aws"), Region: types.StringValue("Frankfurt"),
	})
	if err != nil {
		t.Fatalf("translateObjectStorage: %v", err)
	}
	if plan != nil {
		t.Errorf("expected nil plan when no object_storage declared, got %+v", plan)
	}
}
