// Command pyxnet-render renders a canonical PyxCloud network or security-group
// fixture into concrete cloud-provider Terraform HCL via the catalog. It is the
// bridge used by the per-provider `terraform plan` / real apply round-trip tests
// (SPEC §6): generate the provider config from a canonical fixture, then
// plan/apply it.
//
// Usage:
//
//	pyxnet-render -fixture place.json -provider aws                 > aws_vpc.tf
//	pyxnet-render -fixture place.json -provider gcp                 > gcp_vpc.tf
//	pyxnet-render -fixture place.json -provider digitalocean        > do_vpc.tf
//	pyxnet-render -fixture sg.json -component security-group -provider aws > aws_sg.tf
//
// The fixture is the abstract, provider-neutral place; -provider selects which
// concrete provider to descend it to, and -component selects which component to
// render (default `network`, the region+VPC component).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/catalog"
)

// fixture is the canonical, provider-neutral place description. It carries both
// the network (region+VPC) and an optional security-group, so a single fixture
// can drive either component.
type fixture struct {
	Name    string   `json:"name"`
	Region  string   `json:"region"`
	CIDR    string   `json:"cidr"`
	Subnets []string `json:"subnets"`
	// SecurityGroup is the optional canonical security-group for this place.
	SecurityGroup *sgFixture `json:"security_group,omitempty"`
	// VirtualMachine is the optional canonical virtual-machine for this place.
	VirtualMachine *vmFixture `json:"virtual_machine,omitempty"`
	// ScaleGroup is the optional canonical virtual-machine-scale-group.
	ScaleGroup *sgScaleFixture `json:"scale_group,omitempty"`
	// LoadBalancer is the optional canonical load-balancer.
	LoadBalancer *lbFixture `json:"load_balancer,omitempty"`
	// ManagedDatabase is the optional canonical managed-database.
	ManagedDatabase *mdbFixture `json:"managed_database,omitempty"`
	// ObjectStorage is the optional canonical object/blob-storage.
	ObjectStorage *objectStorageFixture `json:"object_storage,omitempty"`
}

// objectStorageFixture is the canonical object/blob-storage description embedded
// in a fixture. PRIVATE BY DEFAULT: `public` omitted => false (the secure
// default). `force_destroy` is a pointer so an omitted value takes the
// production-safe default (false); the TEST fixture sets it true ONLY so a
// just-created bucket tears down cleanly — that override is test-only.
type objectStorageFixture struct {
	Name         string `json:"name"`
	Versioning   bool   `json:"versioning"`
	Public       bool   `json:"public"`
	ForceDestroy *bool  `json:"force_destroy,omitempty"`
}

// mdbFixture is the canonical managed-database description embedded in a fixture.
type mdbFixture struct {
	Name      string `json:"name"`
	Engine    string `json:"engine"`
	Version   string `json:"version"`
	CPU       int    `json:"cpu"`
	RAM       int    `json:"ram"`
	StorageGB int    `json:"storage_gb"`
	HA        bool   `json:"ha"`
	Encrypted bool   `json:"encrypted"`
	// DeletionProtection / SkipFinalSnapshot are pointers so an omitted value takes
	// the production-safe default (protection on, final snapshot taken). The TEST
	// fixture sets deletion_protection=false + skip_final_snapshot=true ONLY so the
	// round-trip teardown is clean — that override is test-only and visible here.
	DeletionProtection *bool `json:"deletion_protection,omitempty"`
	SkipFinalSnapshot  *bool `json:"skip_final_snapshot,omitempty"`
	// SecurityGroup is the canonical app SG the DB is reachable from; defaults to
	// the fixture SG.
	SecurityGroup string `json:"security_group"`
}

// lbFixture is the canonical load-balancer description embedded in a fixture.
type lbFixture struct {
	Name        string              `json:"name"`
	Listeners   []lbListenerFixture `json:"listeners"`
	HealthCheck *lbHealthFixture    `json:"health_check"`
	Stickiness  bool                `json:"stickiness"`
	TargetKind  string              `json:"target_kind"`
	TargetName  string              `json:"target_name"`
}

type lbListenerFixture struct {
	Port       int      `json:"port"`
	Protocol   string   `json:"protocol"`
	Conditions []string `json:"conditions"`
}

type lbHealthFixture struct {
	Protocol           string `json:"protocol"`
	Port               int    `json:"port"`
	Path               string `json:"path"`
	IntervalSeconds    int    `json:"interval_seconds"`
	HealthyThreshold   int    `json:"healthy_threshold"`
	UnhealthyThreshold int    `json:"unhealthy_threshold"`
}

// sgScaleFixture is the canonical virtual-machine-scale-group description.
type sgScaleFixture struct {
	Name         string `json:"name"`
	Architecture string `json:"architecture"`
	CPU          int    `json:"cpu"`
	RAM          int    `json:"ram"`
	OS           string `json:"os"`
	OSVersion    string `json:"os_version"`
	Min          int    `json:"min"`
	Max          int    `json:"max"`
	Desired      int    `json:"desired"`
	Health       string `json:"health"`
	// SecurityGroup is the canonical SG name to attach; defaults to the fixture SG.
	SecurityGroup string `json:"security_group"`
}

// vmFixture is the canonical virtual-machine description embedded in a fixture.
type vmFixture struct {
	Name         string `json:"name"`
	Architecture string `json:"architecture"`
	CPU          int    `json:"cpu"`
	RAM          int    `json:"ram"`
	OS           string `json:"os"`
	OSVersion    string `json:"os_version"`
	Count        int    `json:"count"`
	// Subnet / SecurityGroup are the canonical names of the sibling components
	// this VM wires into; default to the first subnet and the fixture's SG.
	Subnet        string `json:"subnet"`
	SecurityGroup string `json:"security_group"`
}

// sgFixture is the canonical security-group description embedded in a fixture.
type sgFixture struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Expose      []int         `json:"expose"`
	Rules       []ruleFixture `json:"rules"`
}

type ruleFixture struct {
	Direction string   `json:"direction"`
	Protocol  string   `json:"protocol"`
	FromPort  int      `json:"from_port"`
	ToPort    int      `json:"to_port"`
	CIDRs     []string `json:"cidrs"`
	SourceSG  string   `json:"source_sg"`
}

func main() {
	fixturePath := flag.String("fixture", "", "path to canonical fixture JSON")
	provider := flag.String("provider", "", "target provider: aws | gcp | digitalocean")
	component := flag.String("component", "network", "component to render: network | security-group | virtual-machine | scale-group | load-balancer | managed-database | object-storage")
	flag.Parse()

	if *fixturePath == "" || *provider == "" {
		fmt.Fprintln(os.Stderr, "usage: pyxnet-render -fixture f.json -provider aws|gcp|digitalocean [-component network|security-group]")
		os.Exit(2)
	}

	raw, err := os.ReadFile(*fixturePath)
	if err != nil {
		fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		fatal(fmt.Errorf("parse fixture: %w", err))
	}

	cat := catalog.MustEmbedded()
	switch *component {
	case "network":
		renderNetwork(cat, f, *provider)
	case "security-group", "sg":
		renderSecurityGroup(cat, f, *provider)
	case "virtual-machine", "vm":
		renderVM(cat, f, *provider)
	case "scale-group", "virtual-machine-scale-group", "asg":
		renderScaleGroup(cat, f, *provider)
	case "load-balancer", "lb":
		renderLoadBalancer(cat, f, *provider)
	case "managed-database", "mdb", "database", "db":
		renderManagedDatabase(cat, f, *provider)
	case "object-storage", "blob-storage", "storage", "s3":
		renderObjectStorage(cat, f, *provider)
	default:
		fatal(fmt.Errorf("unknown component %q (network | security-group | virtual-machine | scale-group | load-balancer | managed-database | object-storage)", *component))
	}
}

func renderObjectStorage(cat catalog.RegionCatalog, f fixture, provider string) {
	if f.ObjectStorage == nil {
		fatal(fmt.Errorf("fixture has no object_storage block"))
	}
	os := f.ObjectStorage
	name := os.Name
	if name == "" {
		name = f.Name
	}
	plan, err := catalog.TranslateObjectStorage(context.Background(), cat, catalog.ObjectStorageSpec{
		Name:         name,
		Region:       f.Region,
		Provider:     provider,
		Versioning:   os.Versioning,
		Public:       os.Public,
		ForceDestroy: os.ForceDestroy,
	})
	if err != nil {
		fatal(err)
	}
	hcl, err := catalog.RenderObjectStorageHCL(plan)
	if err != nil {
		fatal(err)
	}
	fmt.Print(hcl)
}

func renderManagedDatabase(cat catalog.MDBCatalog, f fixture, provider string) {
	if f.ManagedDatabase == nil {
		fatal(fmt.Errorf("fixture has no managed_database block"))
	}
	db := f.ManagedDatabase
	name := db.Name
	if name == "" {
		name = f.Name
	}
	// Spread the DB subnet group across all the network's subnets (multi-AZ).
	subnets := make([]string, 0, len(f.Subnets))
	for i := range f.Subnets {
		subnets = append(subnets, fmt.Sprintf("%s-subnet-%d", f.Name, i+1))
	}
	secGroup := db.SecurityGroup
	if secGroup == "" && f.SecurityGroup != nil {
		secGroup = f.SecurityGroup.Name
	}
	plan, err := catalog.TranslateManagedDatabase(context.Background(), cat, catalog.ManagedDatabaseSpec{
		Name:               name,
		Region:             f.Region,
		Provider:           provider,
		Engine:             db.Engine,
		Version:            db.Version,
		CPU:                db.CPU,
		RAM:                db.RAM,
		StorageGB:          db.StorageGB,
		HA:                 db.HA,
		Encrypted:          db.Encrypted,
		DeletionProtection: db.DeletionProtection,
		SkipFinalSnapshot:  db.SkipFinalSnapshot,
		Network:            f.Name,
		Subnets:            subnets,
		SecurityGroup:      secGroup,
	})
	if err != nil {
		fatal(err)
	}
	hcl, err := catalog.RenderManagedDatabaseHCL(plan)
	if err != nil {
		fatal(err)
	}
	fmt.Print(hcl)
}

func renderLoadBalancer(cat catalog.RegionCatalog, f fixture, provider string) {
	if f.LoadBalancer == nil {
		fatal(fmt.Errorf("fixture has no load_balancer block"))
	}
	lb := f.LoadBalancer
	name := lb.Name
	if name == "" {
		name = f.Name
	}
	// Spread the LB across all the network's subnets (multi-AZ, internet-facing).
	subnets := make([]string, 0, len(f.Subnets))
	for i := range f.Subnets {
		subnets = append(subnets, fmt.Sprintf("%s-subnet-%d", f.Name, i+1))
	}
	secGroup := ""
	if f.SecurityGroup != nil {
		secGroup = f.SecurityGroup.Name
	}
	// Default the target to the fixture's scale-group, else virtual-machine.
	targetKind := lb.TargetKind
	targetName := lb.TargetName
	if targetName == "" {
		if f.ScaleGroup != nil {
			targetName = f.ScaleGroup.Name
			if targetKind == "" {
				targetKind = catalog.LBTargetScaleGroup
			}
		} else if f.VirtualMachine != nil {
			targetName = f.VirtualMachine.Name
			if targetKind == "" {
				targetKind = catalog.LBTargetVM
			}
		}
	}
	listeners := make([]catalog.LBListenerSpec, 0, len(lb.Listeners))
	for _, l := range lb.Listeners {
		listeners = append(listeners, catalog.LBListenerSpec{
			Port:       l.Port,
			Protocol:   l.Protocol,
			Conditions: l.Conditions,
		})
	}
	var hc catalog.LBHealthCheckSpec
	if lb.HealthCheck != nil {
		hc = catalog.LBHealthCheckSpec{
			Protocol:           lb.HealthCheck.Protocol,
			Port:               lb.HealthCheck.Port,
			Path:               lb.HealthCheck.Path,
			IntervalSeconds:    lb.HealthCheck.IntervalSeconds,
			HealthyThreshold:   lb.HealthCheck.HealthyThreshold,
			UnhealthyThreshold: lb.HealthCheck.UnhealthyThreshold,
		}
	}
	plan, err := catalog.TranslateLoadBalancer(context.Background(), cat, catalog.LoadBalancerSpec{
		Name:          name,
		Region:        f.Region,
		Provider:      provider,
		Listeners:     listeners,
		HealthCheck:   hc,
		Stickiness:    lb.Stickiness,
		TargetKind:    targetKind,
		TargetName:    targetName,
		Network:       f.Name,
		Subnets:       subnets,
		SecurityGroup: secGroup,
	})
	if err != nil {
		fatal(err)
	}
	hcl, err := catalog.RenderLoadBalancerHCL(plan)
	if err != nil {
		fatal(err)
	}
	fmt.Print(hcl)
}

func renderScaleGroup(cat catalog.VMCatalog, f fixture, provider string) {
	if f.ScaleGroup == nil {
		fatal(fmt.Errorf("fixture has no scale_group block"))
	}
	sg := f.ScaleGroup
	name := sg.Name
	if name == "" {
		name = f.Name
	}
	// Spread the group across all the network's subnets (multi-AZ).
	subnets := make([]string, 0, len(f.Subnets))
	for i := range f.Subnets {
		subnets = append(subnets, fmt.Sprintf("%s-subnet-%d", f.Name, i+1))
	}
	secGroup := sg.SecurityGroup
	if secGroup == "" && f.SecurityGroup != nil {
		secGroup = f.SecurityGroup.Name
	}
	plan, err := catalog.TranslateScaleGroup(context.Background(), cat, catalog.ScaleGroupSpec{
		Name:          name,
		Region:        f.Region,
		Provider:      provider,
		Architecture:  sg.Architecture,
		CPU:           sg.CPU,
		RAM:           sg.RAM,
		OS:            sg.OS,
		OSVersion:     sg.OSVersion,
		Min:           sg.Min,
		Max:           sg.Max,
		Desired:       sg.Desired,
		Health:        sg.Health,
		Network:       f.Name,
		Subnets:       subnets,
		SecurityGroup: secGroup,
	})
	if err != nil {
		fatal(err)
	}
	hcl, err := catalog.RenderScaleGroupHCL(plan)
	if err != nil {
		fatal(err)
	}
	fmt.Print(hcl)
}

func renderVM(cat catalog.VMCatalog, f fixture, provider string) {
	if f.VirtualMachine == nil {
		fatal(fmt.Errorf("fixture has no virtual_machine block"))
	}
	vm := f.VirtualMachine
	name := vm.Name
	if name == "" {
		name = f.Name
	}
	// Default the subnet to the first network subnet (production-subnet-1) and
	// the SG to the fixture's security-group, so a VM in a VPC+SG wires up.
	subnet := vm.Subnet
	if subnet == "" && len(f.Subnets) > 0 {
		subnet = fmt.Sprintf("%s-subnet-1", f.Name)
	}
	sg := vm.SecurityGroup
	if sg == "" && f.SecurityGroup != nil {
		sg = f.SecurityGroup.Name
	}
	plan, err := catalog.TranslateVM(context.Background(), cat, catalog.VMSpec{
		Name:          name,
		Region:        f.Region,
		Provider:      provider,
		Architecture:  vm.Architecture,
		CPU:           vm.CPU,
		RAM:           vm.RAM,
		OS:            vm.OS,
		OSVersion:     vm.OSVersion,
		Count:         vm.Count,
		Network:       f.Name,
		Subnet:        subnet,
		SecurityGroup: sg,
	})
	if err != nil {
		fatal(err)
	}
	hcl, err := catalog.RenderVMHCL(plan)
	if err != nil {
		fatal(err)
	}
	fmt.Print(hcl)
}

func renderNetwork(cat catalog.RegionCatalog, f fixture, provider string) {
	plan, err := catalog.TranslateNetwork(context.Background(), cat, catalog.NetworkSpec{
		Name:     f.Name,
		Region:   f.Region,
		Provider: provider,
		CIDR:     f.CIDR,
		Subnets:  f.Subnets,
	})
	if err != nil {
		fatal(err)
	}
	hcl, err := catalog.RenderHCL(plan)
	if err != nil {
		fatal(err)
	}
	fmt.Print(hcl)
}

func renderSecurityGroup(cat catalog.RegionCatalog, f fixture, provider string) {
	if f.SecurityGroup == nil {
		fatal(fmt.Errorf("fixture has no security_group block"))
	}
	sg := f.SecurityGroup
	rules := make([]catalog.SecurityRule, 0, len(sg.Rules))
	for _, r := range sg.Rules {
		rules = append(rules, catalog.SecurityRule{
			Direction: r.Direction,
			Protocol:  r.Protocol,
			FromPort:  r.FromPort,
			ToPort:    r.ToPort,
			CIDRs:     r.CIDRs,
			SourceSG:  r.SourceSG,
		})
	}
	name := sg.Name
	if name == "" {
		name = f.Name
	}
	plan, err := catalog.TranslateSecurityGroup(context.Background(), cat, catalog.SecurityGroupSpec{
		Name:        name,
		Network:     f.Name,
		Region:      f.Region,
		Provider:    provider,
		Description: sg.Description,
		Expose:      sg.Expose,
		Rules:       rules,
	})
	if err != nil {
		fatal(err)
	}
	hcl, err := catalog.RenderSGHCL(plan)
	if err != nil {
		fatal(err)
	}
	fmt.Print(hcl)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "pyxnet-render:", err)
	os.Exit(1)
}
