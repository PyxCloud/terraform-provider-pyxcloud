package catalog

import (
	_ "embed"
	"fmt"
	"strings"
)

// This file is the COMPLETE wave-2 Microsoft Azure surface for the PyxCloud
// Terraform provider. It is intentionally self-contained in ONE new file so the
// concurrent wave-1 / sibling-provider PRs never conflict with it. It holds:
//
//   1. the Azure catalog snapshot loader (azure_catalog.csv -> the SAME
//      EmbeddedCatalog maps wave-1 resolves against), so every existing
//      Translate* function resolves Azure regions/SKUs/images with zero changes;
//   2. the Azure renderers for the full component set, mirroring the established
//      render.go / render_macro.go shapes (azurerm_* resources).
//
// The ONLY edits outside new files are: ProviderAzure in the provider-name map
// (catalog.go), one merge call in NewEmbedded (embedded.go), and one Azure case
// per component render-dispatch + ResourceType switch. Everything provider-
// specific lives here.

// Provider-facing name for Microsoft Azure (Terraform `azurerm` provider) and the
// catalog csp token. These mirror the wave-1 constants in catalog.go.
const (
	ProviderAzure = "azure"
	cspAzure      = "azure"
)

// azureCatalogCSV is the wave-2 Azure catalog snapshot (region + virtual_machine
// + OS image + managed_database rows), discriminated by a leading `kind` column.
// It is the Azure analogue of the wave-1 region/vm/os/mdb CSVs, kept in its own
// file so the wave-2 PR is conflict-free against the concurrently-edited wave-1
// snapshots. See the file header for the per-kind column contract and the ETL
// provenance/gap note.
//
//go:embed azure_catalog.csv
var azureCatalogCSV string

// loadAzure parses azure_catalog.csv and MERGES its rows into the EmbeddedCatalog
// indexes, using the exact same keying as the wave-1 loaders so Azure resolution
// is identical to AWS/GCP/DO (region -> csp_region, (csp,region,arch) -> SKU,
// (csp,region,os,ver,arch) -> image, (csp,region,engine) -> DB class). It is
// called once from NewEmbedded; a malformed snapshot is a hard build/parse error.
func (c *EmbeddedCatalog) loadAzure() error {
	lines := strings.Split(azureCatalogCSV, "\n")
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ",")
		kind := strings.TrimSpace(fields[0])
		if kind == "kind" {
			continue // header
		}
		switch kind {
		case "region":
			if len(fields) != 7 {
				return fmt.Errorf("azure_catalog.csv line %d: region needs 7 fields, got %d", i+1, len(fields))
			}
			row := RegionRow{
				MacroRegion:          strings.TrimSpace(fields[1]),
				Country:              strings.TrimSpace(fields[2]),
				RegionName:           strings.TrimSpace(fields[3]),
				CSPRegion:            strings.TrimSpace(fields[4]),
				CSPRegionDescription: strings.TrimSpace(fields[5]),
				CSP:                  strings.TrimSpace(fields[6]),
			}
			c.rows = append(c.rows, row)
			k := key(row.CSP, row.RegionName)
			if _, exists := c.byCSPRegion[k]; !exists {
				c.byCSPRegion[k] = row
			}
		case "vm":
			if len(fields) != 9 {
				return fmt.Errorf("azure_catalog.csv line %d: vm needs 9 fields, got %d", i+1, len(fields))
			}
			row := VMRow{
				Name:              strings.TrimSpace(fields[1]),
				Family:            strings.TrimSpace(fields[2]),
				CSP:               cspAzure,
				CSPRegion:         strings.TrimSpace(fields[3]),
				Architecture:      strings.TrimSpace(fields[4]),
				CPU:               atoiOrZero(fields[5]),
				RAM:               atoiOrZero(fields[6]),
				GPU:               strings.TrimSpace(fields[7]),
				SupportsAutoscale: strings.EqualFold(strings.TrimSpace(fields[8]), "true"),
			}
			c.vmRows = append(c.vmRows, row)
			vk := vmRegionArchKey(row.CSP, row.CSPRegion, row.Architecture)
			c.vmByRegionArch[vk] = append(c.vmByRegionArch[vk], row)
		case "os":
			if len(fields) != 6 {
				return fmt.Errorf("azure_catalog.csv line %d: os needs 6 fields, got %d", i+1, len(fields))
			}
			row := OSImageRow{
				CSP:          cspAzure,
				CSPRegion:    strings.TrimSpace(fields[1]),
				OSName:       strings.TrimSpace(fields[2]),
				OSVersion:    strings.TrimSpace(fields[3]),
				Architecture: strings.TrimSpace(fields[4]),
				Image:        strings.TrimSpace(fields[5]),
			}
			c.osByKey[osKey(row.CSP, row.CSPRegion, row.OSName, row.OSVersion, row.Architecture)] = row
		case "mdb":
			if len(fields) != 7 {
				return fmt.Errorf("azure_catalog.csv line %d: mdb needs 7 fields, got %d", i+1, len(fields))
			}
			row := MDBRow{
				Name:      strings.TrimSpace(fields[1]),
				Family:    strings.TrimSpace(fields[2]),
				CSP:       cspAzure,
				CSPRegion: strings.TrimSpace(fields[3]),
				Engine:    strings.TrimSpace(fields[4]),
				CPU:       atoiOrZero(fields[5]),
				RAM:       atoiOrZero(fields[6]),
			}
			c.mdbRows = append(c.mdbRows, row)
			mk := mdbRegionEngineKey(row.CSP, row.CSPRegion, row.Engine)
			c.mdbByRegionEng[mk] = append(c.mdbByRegionEng[mk], row)
		default:
			return fmt.Errorf("azure_catalog.csv line %d: unknown kind %q", i+1, kind)
		}
	}
	return nil
}

// ── network / region+VPC ─────────────────────────────────────────────────────
//
// Azure: a resource group + azurerm_virtual_network + one azurerm_subnet per
// declared subnet. Azure does not expose AZ-per-subnet at the subnet resource
// level (zones are a VM/zonal-resource property), so subnets are plain
// address_prefixes inside the vnet — the NetworkPlan's zones (empty for Azure)
// reflect that. Every component anchors on the resource group this renders.
func renderNetworkAzure(p NetworkPlan) string {
	name := tfName(p.VPCName)
	rg := name + "_rg"
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"azurerm_resource_group\" %q {\n", rg)
	fmt.Fprintf(&b, "  name     = \"%s-rg\"\n", tfName(p.VPCName))
	fmt.Fprintf(&b, "  location = %q\n", p.CSPRegion)
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n\n")

	fmt.Fprintf(&b, "resource \"azurerm_virtual_network\" %q {\n", name)
	fmt.Fprintf(&b, "  name                = \"%s-vnet\"\n", tfName(p.VPCName))
	fmt.Fprintf(&b, "  location            = azurerm_resource_group.%s.location\n", rg)
	fmt.Fprintf(&b, "  resource_group_name = azurerm_resource_group.%s.name\n", rg)
	fmt.Fprintf(&b, "  address_space       = [%q]\n", p.CIDR)
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")

	for i, s := range p.Subnets {
		sn := fmt.Sprintf("%s_%d", name, i+1)
		fmt.Fprintf(&b, "\nresource \"azurerm_subnet\" %q {\n", sn)
		fmt.Fprintf(&b, "  name                 = %q\n", s.Name)
		fmt.Fprintf(&b, "  resource_group_name  = azurerm_resource_group.%s.name\n", rg)
		fmt.Fprintf(&b, "  virtual_network_name = azurerm_virtual_network.%s.name\n", name)
		fmt.Fprintf(&b, "  address_prefixes     = [%q]\n", s.CIDR)
		b.WriteString("}\n")
	}
	return b.String()
}

// azureRGRef returns the resource-group resource label the network renderer emits
// for a given network name, so sibling components anchor on the same RG.
func azureRGRef(networkName string) string {
	return tfName(networkName) + "_rg"
}

// ── security-group / NSG ──────────────────────────────────────────────────────
//
// Azure: azurerm_network_security_group + one azurerm_network_security_rule per
// rule. NSG rules need an explicit priority and a direction (Inbound/Outbound).
// We assign priorities deterministically from 100 upward per direction. The
// description is ASCII-guarded (re-asserted, like every other renderer).
func renderSGAzure(p SecurityGroupPlan) string {
	name := tfName(p.SGName)
	desc := asciiOnly(p.Description)
	var b strings.Builder
	rgRef := ""
	if p.NetworkName != "" {
		rgRef = azureRGRef(p.NetworkName)
	}
	fmt.Fprintf(&b, "resource \"azurerm_network_security_group\" %q {\n", name)
	fmt.Fprintf(&b, "  name                = %q\n", p.SGName)
	fmt.Fprintf(&b, "  location            = %q\n", p.CSPRegion)
	if rgRef != "" {
		fmt.Fprintf(&b, "  resource_group_name = azurerm_resource_group.%s.name\n", rgRef)
	}
	if desc != "" {
		// NSG carries no group-level description field; surface it as a tag.
		fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\", description = %q }\n", desc)
	} else {
		fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	}
	b.WriteString("}\n")

	inPri, outPri := 100, 100
	for i, r := range p.Rules {
		rn := fmt.Sprintf("%s_%s_%d", name, r.Direction, i)
		dir := "Inbound"
		pri := &inPri
		if r.Direction == DirEgress {
			dir = "Outbound"
			pri = &outPri
		}
		fmt.Fprintf(&b, "\nresource \"azurerm_network_security_rule\" %q {\n", rn)
		fmt.Fprintf(&b, "  name                        = \"%s-%s-%d\"\n", tfName(p.SGName), r.Direction, i)
		fmt.Fprintf(&b, "  priority                    = %d\n", *pri)
		fmt.Fprintf(&b, "  direction                   = %q\n", dir)
		b.WriteString("  access                      = \"Allow\"\n")
		fmt.Fprintf(&b, "  protocol                    = %q\n", azureProto(r.Protocol))
		fmt.Fprintf(&b, "  source_port_range           = \"*\"\n")
		if r.Protocol == ProtoTCP || r.Protocol == ProtoUDP {
			fmt.Fprintf(&b, "  destination_port_range      = %q\n", portRangeString(r.FromPort, r.ToPort))
		} else {
			fmt.Fprintf(&b, "  destination_port_range      = \"*\"\n")
		}
		// Source/destination prefixes: ingress scopes the source, egress the dest.
		prefixes := r.CIDRs
		if len(prefixes) == 0 {
			prefixes = []string{"*"}
		}
		if r.Direction == DirEgress {
			fmt.Fprintf(&b, "  source_address_prefix       = \"*\"\n")
			fmt.Fprintf(&b, "  destination_address_prefixes = %s\n", hclCIDRList(prefixes))
		} else {
			fmt.Fprintf(&b, "  source_address_prefixes     = %s\n", hclCIDRList(prefixes))
			fmt.Fprintf(&b, "  destination_address_prefix  = \"*\"\n")
		}
		if rgRef != "" {
			fmt.Fprintf(&b, "  resource_group_name         = azurerm_resource_group.%s.name\n", rgRef)
		}
		fmt.Fprintf(&b, "  network_security_group_name = azurerm_network_security_group.%s.name\n", name)
		b.WriteString("}\n")
		*pri += 10
	}
	return b.String()
}

// azureProto maps a canonical protocol to the Azure NSG protocol token ("*" = all).
func azureProto(proto string) string {
	switch proto {
	case ProtoAll:
		return "*"
	case ProtoTCP:
		return "Tcp"
	case ProtoUDP:
		return "Udp"
	case ProtoICMP:
		return "Icmp"
	}
	return "*"
}

// ── virtual-machine ──────────────────────────────────────────────────────────
//
// Azure: one azurerm_network_interface + one azurerm_linux_virtual_machine per
// instance. The size comes from the catalog (VMPlan.InstanceType). The image is
// the catalog URN (publisher:offer:sku:version), split into source_image_reference.
func renderVMAzure(p VMPlan) string {
	var b strings.Builder
	rg := ""
	if p.NetworkName != "" {
		rg = azureRGRef(p.NetworkName)
	}
	subnetLabel := subnetResourceLabel(p.NetworkName, p.SubnetName)
	pub, offer, sku, ver := splitAzureImageURN(p.Image)
	for _, inst := range p.Instances {
		rn := tfName(inst.Name)
		nicName := rn + "_nic"

		fmt.Fprintf(&b, "resource \"azurerm_network_interface\" %q {\n", nicName)
		fmt.Fprintf(&b, "  name                = \"%s-nic\"\n", tfName(inst.Name))
		fmt.Fprintf(&b, "  location            = %q\n", p.CSPRegion)
		if rg != "" {
			fmt.Fprintf(&b, "  resource_group_name = azurerm_resource_group.%s.name\n", rg)
		}
		b.WriteString("  ip_configuration {\n")
		b.WriteString("    name                          = \"internal\"\n")
		if p.SubnetName != "" {
			fmt.Fprintf(&b, "    subnet_id                     = azurerm_subnet.%s.id\n", subnetLabel)
		}
		b.WriteString("    private_ip_address_allocation = \"Dynamic\"\n")
		b.WriteString("  }\n")
		fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
		b.WriteString("}\n\n")

		fmt.Fprintf(&b, "resource \"azurerm_linux_virtual_machine\" %q {\n", rn)
		fmt.Fprintf(&b, "  name                  = %q\n", tfName(inst.Name))
		fmt.Fprintf(&b, "  location              = %q\n", p.CSPRegion)
		if rg != "" {
			fmt.Fprintf(&b, "  resource_group_name   = azurerm_resource_group.%s.name\n", rg)
		}
		fmt.Fprintf(&b, "  size                  = %q\n", p.InstanceType)
		fmt.Fprintf(&b, "  network_interface_ids = [azurerm_network_interface.%s.id]\n", nicName)
		b.WriteString("  admin_username        = \"pyxadmin\"\n")
		// SSH key is provided out-of-band (CI / Vault) via a variable, never inline.
		b.WriteString("  admin_ssh_key {\n")
		b.WriteString("    username   = \"pyxadmin\"\n")
		b.WriteString("    public_key = var.ssh_public_key\n")
		b.WriteString("  }\n")
		b.WriteString("  os_disk {\n")
		b.WriteString("    caching              = \"ReadWrite\"\n")
		b.WriteString("    storage_account_type = \"Premium_LRS\"\n")
		b.WriteString("  }\n")
		b.WriteString("  source_image_reference {\n")
		fmt.Fprintf(&b, "    publisher = %q\n", pub)
		fmt.Fprintf(&b, "    offer     = %q\n", offer)
		fmt.Fprintf(&b, "    sku       = %q\n", sku)
		fmt.Fprintf(&b, "    version   = %q\n", ver)
		b.WriteString("  }\n")
		fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
		b.WriteString("}\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// splitAzureImageURN splits a marketplace URN "publisher:offer:sku:version" into
// its four parts. A malformed/short URN degrades gracefully (the catalog guards
// the format upstream); missing parts fall back to "latest"/"" so the render is
// always well-formed HCL.
func splitAzureImageURN(urn string) (publisher, offer, sku, version string) {
	parts := strings.Split(urn, ":")
	get := func(i string, idx int) string {
		if idx < len(parts) {
			return parts[idx]
		}
		return i
	}
	return get("", 0), get("", 1), get("", 2), get("latest", 3)
}

// ── scale-group / VM Scale Set ───────────────────────────────────────────────
//
// Azure: azurerm_linux_virtual_machine_scale_set with an autoscale setting
// (azurerm_monitor_autoscale_setting) for min/max/default capacity — the Azure
// analogue of the AWS ASG / GCP MIG. health_probe is wired when health=elb (the
// scale set integrates with a load-balancer probe).
func renderScaleGroupAzure(p ScaleGroupPlan) string {
	name := tfName(p.GroupName)
	vmssName := name + "_vmss"
	asName := name + "_as"
	var b strings.Builder
	rg := ""
	if p.NetworkName != "" {
		rg = azureRGRef(p.NetworkName)
	}
	subnetLabel := ""
	if len(p.SubnetNames) > 0 {
		subnetLabel = subnetResourceLabel(p.NetworkName, p.SubnetNames[0])
	}
	pub, offer, sku, ver := splitAzureImageURN(p.Image)

	fmt.Fprintf(&b, "resource \"azurerm_linux_virtual_machine_scale_set\" %q {\n", vmssName)
	fmt.Fprintf(&b, "  name                = \"%s-vmss\"\n", tfName(p.GroupName))
	fmt.Fprintf(&b, "  location            = %q\n", p.CSPRegion)
	if rg != "" {
		fmt.Fprintf(&b, "  resource_group_name = azurerm_resource_group.%s.name\n", rg)
	}
	fmt.Fprintf(&b, "  sku                 = %q\n", p.InstanceType)
	fmt.Fprintf(&b, "  instances           = %d\n", p.Desired)
	b.WriteString("  admin_username      = \"pyxadmin\"\n")
	b.WriteString("  upgrade_mode        = \"Rolling\"\n") // rolling refresh, the production pattern
	b.WriteString("  admin_ssh_key {\n")
	b.WriteString("    username   = \"pyxadmin\"\n")
	b.WriteString("    public_key = var.ssh_public_key\n")
	b.WriteString("  }\n")
	b.WriteString("  source_image_reference {\n")
	fmt.Fprintf(&b, "    publisher = %q\n", pub)
	fmt.Fprintf(&b, "    offer     = %q\n", offer)
	fmt.Fprintf(&b, "    sku       = %q\n", sku)
	fmt.Fprintf(&b, "    version   = %q\n", ver)
	b.WriteString("  }\n")
	b.WriteString("  os_disk {\n")
	b.WriteString("    caching              = \"ReadWrite\"\n")
	b.WriteString("    storage_account_type = \"Premium_LRS\"\n")
	b.WriteString("  }\n")
	b.WriteString("  network_interface {\n")
	fmt.Fprintf(&b, "    name    = \"%s-nic\"\n", tfName(p.GroupName))
	b.WriteString("    primary = true\n")
	b.WriteString("    ip_configuration {\n")
	b.WriteString("      name      = \"internal\"\n")
	b.WriteString("      primary   = true\n")
	if subnetLabel != "" {
		fmt.Fprintf(&b, "      subnet_id = azurerm_subnet.%s.id\n", subnetLabel)
	}
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	if p.Health == HealthELB {
		b.WriteString("  health_probe_id = var.lb_health_probe_id\n")
	}
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n\n")

	// Autoscale setting: min/max/default capacity, the Azure analogue of the ASG
	// bounds. Scales on average CPU (the standard general-purpose metric).
	fmt.Fprintf(&b, "resource \"azurerm_monitor_autoscale_setting\" %q {\n", asName)
	fmt.Fprintf(&b, "  name                = \"%s-autoscale\"\n", tfName(p.GroupName))
	fmt.Fprintf(&b, "  location            = %q\n", p.CSPRegion)
	if rg != "" {
		fmt.Fprintf(&b, "  resource_group_name = azurerm_resource_group.%s.name\n", rg)
	}
	fmt.Fprintf(&b, "  target_resource_id  = azurerm_linux_virtual_machine_scale_set.%s.id\n", vmssName)
	b.WriteString("  profile {\n")
	b.WriteString("    name = \"pyxcloud-default\"\n")
	b.WriteString("    capacity {\n")
	fmt.Fprintf(&b, "      minimum = %d\n", p.Min)
	fmt.Fprintf(&b, "      maximum = %d\n", p.Max)
	fmt.Fprintf(&b, "      default = %d\n", p.Desired)
	b.WriteString("    }\n")
	b.WriteString("    rule {\n")
	b.WriteString("      metric_trigger {\n")
	b.WriteString("        metric_name        = \"Percentage CPU\"\n")
	fmt.Fprintf(&b, "        metric_resource_id = azurerm_linux_virtual_machine_scale_set.%s.id\n", vmssName)
	b.WriteString("        time_grain         = \"PT1M\"\n")
	b.WriteString("        statistic          = \"Average\"\n")
	b.WriteString("        time_window        = \"PT5M\"\n")
	b.WriteString("        time_aggregation   = \"Average\"\n")
	b.WriteString("        operator           = \"GreaterThan\"\n")
	b.WriteString("        threshold          = 70\n")
	b.WriteString("      }\n")
	b.WriteString("      scale_action {\n")
	b.WriteString("        direction = \"Increase\"\n")
	b.WriteString("        type      = \"ChangeCount\"\n")
	b.WriteString("        value     = \"1\"\n")
	b.WriteString("        cooldown  = \"PT5M\"\n")
	b.WriteString("      }\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	b.WriteString("}\n")
	return b.String()
}

// ── load-balancer ────────────────────────────────────────────────────────────
//
// Azure: azurerm_public_ip + azurerm_lb (Standard, public) + one
// azurerm_lb_probe (health check) + an azurerm_lb_backend_address_pool +
// one azurerm_lb_rule per listener. Standard SKU is the production default
// (zone-redundant). Azure LB is L4; HTTP path health checks degrade to a TCP
// probe on the health-check port (the L4 LB has no HTTP path semantics).
func renderLBAzure(p LoadBalancerPlan) string {
	name := tfName(p.LBName)
	lbName := name + "_lb"
	pipName := name + "_pip"
	poolName := name + "_pool"
	probeName := name + "_probe"
	var b strings.Builder
	rg := ""
	if p.NetworkName != "" {
		rg = azureRGRef(p.NetworkName)
	}
	hc := p.HealthCheck

	fmt.Fprintf(&b, "resource \"azurerm_public_ip\" %q {\n", pipName)
	fmt.Fprintf(&b, "  name                = \"%s-pip\"\n", tfName(p.LBName))
	fmt.Fprintf(&b, "  location            = %q\n", p.CSPRegion)
	if rg != "" {
		fmt.Fprintf(&b, "  resource_group_name = azurerm_resource_group.%s.name\n", rg)
	}
	b.WriteString("  allocation_method   = \"Static\"\n")
	b.WriteString("  sku                 = \"Standard\"\n")
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n\n")

	fmt.Fprintf(&b, "resource \"azurerm_lb\" %q {\n", lbName)
	fmt.Fprintf(&b, "  name                = %q\n", tfName(p.LBName))
	fmt.Fprintf(&b, "  location            = %q\n", p.CSPRegion)
	if rg != "" {
		fmt.Fprintf(&b, "  resource_group_name = azurerm_resource_group.%s.name\n", rg)
	}
	b.WriteString("  sku                 = \"Standard\"\n")
	b.WriteString("  frontend_ip_configuration {\n")
	b.WriteString("    name                 = \"pyxcloud-frontend\"\n")
	fmt.Fprintf(&b, "    public_ip_address_id = azurerm_public_ip.%s.id\n", pipName)
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n\n")

	fmt.Fprintf(&b, "resource \"azurerm_lb_backend_address_pool\" %q {\n", poolName)
	fmt.Fprintf(&b, "  name            = \"%s-pool\"\n", tfName(p.LBName))
	fmt.Fprintf(&b, "  loadbalancer_id = azurerm_lb.%s.id\n", lbName)
	b.WriteString("}\n\n")

	fmt.Fprintf(&b, "resource \"azurerm_lb_probe\" %q {\n", probeName)
	fmt.Fprintf(&b, "  name                = \"%s-probe\"\n", tfName(p.LBName))
	fmt.Fprintf(&b, "  loadbalancer_id     = azurerm_lb.%s.id\n", lbName)
	fmt.Fprintf(&b, "  protocol            = %q\n", azureLBProbeProto(hc.Protocol))
	fmt.Fprintf(&b, "  port                = %d\n", hc.Port)
	if hc.Protocol == LBProtoHTTP || hc.Protocol == LBProtoHTTPS {
		fmt.Fprintf(&b, "  request_path        = %q\n", hc.Path)
	}
	fmt.Fprintf(&b, "  interval_in_seconds = %d\n", hc.IntervalSeconds)
	fmt.Fprintf(&b, "  number_of_probes    = %d\n", hc.UnhealthyThreshold)
	b.WriteString("}\n\n")

	for _, l := range p.Listeners {
		rn := fmt.Sprintf("%s_rule_%d", name, l.Port)
		fmt.Fprintf(&b, "resource \"azurerm_lb_rule\" %q {\n", rn)
		fmt.Fprintf(&b, "  name                           = \"%s-rule-%d\"\n", tfName(p.LBName), l.Port)
		fmt.Fprintf(&b, "  loadbalancer_id                = azurerm_lb.%s.id\n", lbName)
		b.WriteString("  protocol                       = \"Tcp\"\n")
		fmt.Fprintf(&b, "  frontend_port                  = %d\n", l.Port)
		fmt.Fprintf(&b, "  backend_port                   = %d\n", l.Port)
		b.WriteString("  frontend_ip_configuration_name = \"pyxcloud-frontend\"\n")
		fmt.Fprintf(&b, "  backend_address_pool_ids       = [azurerm_lb_backend_address_pool.%s.id]\n", poolName)
		fmt.Fprintf(&b, "  probe_id                       = azurerm_lb_probe.%s.id\n", probeName)
		if p.Stickiness {
			// Source-IP affinity is the Azure LB analogue of LB cookie stickiness.
			b.WriteString("  load_distribution              = \"SourceIP\"\n")
		}
		b.WriteString("}\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// azureLBProbeProto maps a canonical LB protocol to an Azure lb_probe protocol
// (Http / Https / Tcp).
func azureLBProbeProto(proto string) string {
	switch proto {
	case LBProtoHTTPS:
		return "Https"
	case LBProtoHTTP:
		return "Http"
	}
	return "Tcp"
}

// ── managed-database ─────────────────────────────────────────────────────────
//
// Azure: azurerm_postgresql_flexible_server (or azurerm_mysql_flexible_server)
// with the SKU from the catalog, geo/zone-redundant HA when ha=true, and
// production-safe defaults. The data-safety force-replace guard runs at PLAN time
// (CheckManagedDatabaseDataSafety, provider-agnostic) exactly as for wave-1 — this
// renderer always emits the production-safe shape.
func renderMDBAzure(p ManagedDatabasePlan) string {
	name := tfName(p.DBName)
	var b strings.Builder
	rg := ""
	if p.NetworkName != "" {
		rg = azureRGRef(p.NetworkName)
	}
	resource := "azurerm_postgresql_flexible_server"
	verAttr := mdbAzurePostgresVersion(p.EngineVersion)
	if p.Engine == DBEngineMySQL {
		resource = "azurerm_mysql_flexible_server"
		verAttr = mdbAzureMySQLVersion(p.EngineVersion)
	}

	fmt.Fprintf(&b, "resource %q %q {\n", resource, name)
	fmt.Fprintf(&b, "  name                   = %q\n", name)
	fmt.Fprintf(&b, "  location               = %q\n", p.CSPRegion)
	if rg != "" {
		fmt.Fprintf(&b, "  resource_group_name    = azurerm_resource_group.%s.name\n", rg)
	}
	fmt.Fprintf(&b, "  version                = %q\n", verAttr)
	fmt.Fprintf(&b, "  sku_name               = %q\n", azureFlexSKU(p.Family, p.DBClass))
	fmt.Fprintf(&b, "  storage_mb             = %d\n", azureFlexStorageMB(p.StorageGB))
	// Credentials are managed out-of-band (Key Vault / Vault); password via variable.
	b.WriteString("  administrator_login    = \"pyxadmin\"\n")
	b.WriteString("  administrator_password = var.db_password\n")
	b.WriteString("  backup_retention_days  = 7\n")
	// HA: zone-redundant standby (the Azure Flexible Server HA mode).
	if p.HA {
		b.WriteString("  high_availability {\n")
		b.WriteString("    mode = \"ZoneRedundant\"\n")
		b.WriteString("  }\n")
	}
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	// Production-safe default: deletion protection via lifecycle prevent_destroy
	// (Flexible Server has no in-place deletion-protection flag; the final-snapshot
	// intent is met by the always-on automated backups above).
	if p.DeletionProtection {
		b.WriteString("  lifecycle {\n")
		b.WriteString("    prevent_destroy = true\n")
		b.WriteString("  }\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// azureFlexStorageMB snaps a requested storage size (GiB) to the smallest valid
// Azure Flexible Server storage_mb tier (Azure only accepts a fixed ladder, not
// arbitrary sizes). A request larger than the top tier clamps to the maximum.
func azureFlexStorageMB(storageGB int) int {
	tiers := []int{32768, 65536, 131072, 262144, 524288, 1048576, 2097152, 4193280, 4194304, 8388608, 16777216, 33553408}
	want := storageGB * 1024
	for _, t := range tiers {
		if t >= want {
			return t
		}
	}
	return tiers[len(tiers)-1]
}

// azureFlexSKU maps the catalog (family, class) to the Azure Flexible Server
// sku_name form "<Tier>_<Size>" (e.g. B_Standard_B1ms, GP_Standard_D2ds_v5).
func azureFlexSKU(family, class string) string {
	tier := "GP"
	switch strings.ToLower(family) {
	case "burstable":
		tier = "B"
	case "memoryoptimized":
		tier = "MO"
	case "generalpurpose":
		tier = "GP"
	}
	return tier + "_" + class
}

// mdbAzurePostgresVersion maps the resolved engine version to an Azure PostgreSQL
// Flexible Server major-version token (Azure pins majors: "16", "15", ...).
func mdbAzurePostgresVersion(version string) string {
	v := strings.TrimSpace(version)
	if v == "" {
		return "16"
	}
	// Azure takes the major only.
	return strings.SplitN(v, ".", 2)[0]
}

// mdbAzureMySQLVersion maps the resolved engine version to an Azure MySQL
// Flexible Server version token ("8.0" / "5.7").
func mdbAzureMySQLVersion(version string) string {
	v := strings.TrimSpace(version)
	if v == "" {
		return "8.0"
	}
	return v
}

// ── object-storage ───────────────────────────────────────────────────────────
//
// Azure: azurerm_storage_account + azurerm_storage_container. PRIVATE BY DEFAULT
// (SPEC §5.7): the storage account sets public_network_access / blob public access
// off unless explicitly public, and the container access_type is "private" unless
// public ("blob" = anonymous read of blobs). Versioning is a blob property on the
// account. The account name reuses the globally-unique-safe derived bucket name
// (sans hyphens — Azure storage account names are 3-24 lowercase alphanumerics).
func renderObjectStorageAzure(p ObjectStoragePlan) string {
	label := tfName(p.LogicalName)
	acctName := label + "_sa"
	containerName := label + "_container"
	var b strings.Builder

	accessType := "private"
	if p.Public {
		accessType = "blob"
	}
	allowBlobPublic := p.Public

	fmt.Fprintf(&b, "resource \"azurerm_storage_account\" %q {\n", acctName)
	fmt.Fprintf(&b, "  name                          = %q\n", azureStorageAccountName(p.BucketName))
	fmt.Fprintf(&b, "  location                      = %q\n", p.CSPRegion)
	b.WriteString("  resource_group_name           = var.resource_group_name\n")
	b.WriteString("  account_tier                  = \"Standard\"\n")
	b.WriteString("  account_replication_type      = \"LRS\"\n")
	// PRIVATE BY DEFAULT: anonymous blob access is disabled unless explicitly public.
	fmt.Fprintf(&b, "  allow_nested_items_to_be_public = %t\n", allowBlobPublic)
	b.WriteString("  blob_properties {\n")
	b.WriteString("    versioning_enabled = " + boolStr(p.Versioning) + "\n")
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n\n")

	fmt.Fprintf(&b, "resource \"azurerm_storage_container\" %q {\n", containerName)
	fmt.Fprintf(&b, "  name                  = %q\n", p.LogicalName)
	fmt.Fprintf(&b, "  storage_account_name  = azurerm_storage_account.%s.name\n", acctName)
	fmt.Fprintf(&b, "  container_access_type = %q\n", accessType)
	b.WriteString("}\n")
	return b.String()
}

// azureStorageAccountName reduces a derived bucket name to a valid Azure storage
// account name: 3-24 chars, lowercase letters and digits only (no hyphens).
func azureStorageAccountName(bucket string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(bucket) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
		}
	}
	out := sb.String()
	if out == "" {
		out = "pyxstorage"
	}
	if len(out) > 24 {
		out = out[:24]
	}
	for len(out) < 3 {
		out += "0"
	}
	return out
}

// ── cache ────────────────────────────────────────────────────────────────────
//
// Azure: azurerm_redis_cache. Capacity/family/sku_name are derived from the
// requested memory; non-SSL port is disabled (TLS only) — the secure default.
func renderCacheAzure(p CachePlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	skuName, family, capacity := azureRedisSizing(p.MemoryGB)
	fmt.Fprintf(&b, "resource \"azurerm_redis_cache\" %q {\n", name)
	fmt.Fprintf(&b, "  name                = %q\n", p.Name)
	fmt.Fprintf(&b, "  location            = %q\n", p.CSPRegion)
	b.WriteString("  resource_group_name = var.resource_group_name\n")
	fmt.Fprintf(&b, "  capacity            = %d\n", capacity)
	fmt.Fprintf(&b, "  family              = %q\n", family)
	fmt.Fprintf(&b, "  sku_name            = %q\n", skuName)
	// Secure default: TLS only, no public non-SSL port.
	b.WriteString("  non_ssl_port_enabled = false\n")
	b.WriteString("  minimum_tls_version  = \"1.2\"\n")
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

// azureRedisSizing maps requested memory to an Azure Redis (sku_name, family,
// capacity) triple. Basic/Standard use the C family; larger uses Premium P family.
func azureRedisSizing(memGB int) (sku, family string, capacity int) {
	switch {
	case memGB <= 1:
		return "Standard", "C", 1
	case memGB <= 6:
		return "Standard", "C", 2
	case memGB <= 13:
		return "Standard", "C", 3
	case memGB <= 26:
		return "Premium", "P", 1
	default:
		return "Premium", "P", 2
	}
}

// ── messaging: managed-queue + event-streaming ───────────────────────────────
//
// Azure: a managed-queue maps to a Service Bus namespace + queue
// (azurerm_servicebus_namespace + azurerm_servicebus_queue); event-streaming maps
// to an Event Hubs namespace + event hub (azurerm_eventhub_namespace +
// azurerm_eventhub). Both are encrypted at rest by Microsoft-managed keys and
// private to the namespace by default.
func renderMessagingAzure(p MessagingPlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	if p.Kind == KindStream {
		nsName := name + "_ehns"
		fmt.Fprintf(&b, "resource \"azurerm_eventhub_namespace\" %q {\n", nsName)
		fmt.Fprintf(&b, "  name                = \"%s-ehns\"\n", p.Name)
		fmt.Fprintf(&b, "  location            = %q\n", p.CSPRegion)
		b.WriteString("  resource_group_name = var.resource_group_name\n")
		b.WriteString("  sku                 = \"Standard\"\n")
		fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
		b.WriteString("}\n\n")

		partitions := p.Shards
		if partitions <= 0 {
			partitions = 2 // Event Hubs requires >= 1 partition; default a small fixed count.
		}
		retention := p.RetentionHours / 24
		if retention < 1 {
			retention = 1 // Event Hubs message_retention is in DAYS (>= 1).
		}
		fmt.Fprintf(&b, "resource \"azurerm_eventhub\" %q {\n", name)
		fmt.Fprintf(&b, "  name                = %q\n", p.Name)
		fmt.Fprintf(&b, "  namespace_name      = azurerm_eventhub_namespace.%s.name\n", nsName)
		b.WriteString("  resource_group_name = var.resource_group_name\n")
		fmt.Fprintf(&b, "  partition_count     = %d\n", partitions)
		fmt.Fprintf(&b, "  message_retention   = %d\n", retention)
		b.WriteString("}\n")
		return b.String()
	}

	// managed-queue -> Service Bus namespace + queue.
	nsName := name + "_sbns"
	fmt.Fprintf(&b, "resource \"azurerm_servicebus_namespace\" %q {\n", nsName)
	fmt.Fprintf(&b, "  name                = \"%s-sbns\"\n", p.Name)
	fmt.Fprintf(&b, "  location            = %q\n", p.CSPRegion)
	b.WriteString("  resource_group_name = var.resource_group_name\n")
	b.WriteString("  sku                 = \"Standard\"\n")
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n\n")

	fmt.Fprintf(&b, "resource \"azurerm_servicebus_queue\" %q {\n", name)
	fmt.Fprintf(&b, "  name         = %q\n", p.Name)
	fmt.Fprintf(&b, "  namespace_id = azurerm_servicebus_namespace.%s.id\n", nsName)
	if p.FIFO {
		// Service Bus sessions give FIFO/ordered, exactly-once-ish delivery.
		b.WriteString("  requires_session = true\n")
	}
	if p.VisibilityTimeoutSeconds > 0 {
		// lock_duration is the Service Bus analogue of SQS visibility timeout
		// (ISO-8601 duration; capped at 5 minutes by Service Bus).
		secs := p.VisibilityTimeoutSeconds
		if secs > 300 {
			secs = 300
		}
		fmt.Fprintf(&b, "  lock_duration = \"PT%dS\"\n", secs)
	}
	if p.MaxReceiveCount > 0 {
		b.WriteString("  dead_lettering_on_message_expiration = true\n")
		fmt.Fprintf(&b, "  max_delivery_count                   = %d\n", p.MaxReceiveCount)
	}
	b.WriteString("}\n")
	return b.String()
}

// ── dns-zone ─────────────────────────────────────────────────────────────────
//
// Azure: azurerm_dns_zone (public) or azurerm_private_dns_zone (private). A
// private zone links to the place's vnet (azurerm_private_dns_zone_virtual_network_link).
func renderDNSZoneAzure(p DNSZonePlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	if p.Private {
		fmt.Fprintf(&b, "resource \"azurerm_private_dns_zone\" %q {\n", name)
		fmt.Fprintf(&b, "  name                = %q\n", p.Domain)
		b.WriteString("  resource_group_name = var.resource_group_name\n")
		fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
		b.WriteString("}\n")
		if p.NetworkName != "" {
			linkName := name + "_link"
			fmt.Fprintf(&b, "\nresource \"azurerm_private_dns_zone_virtual_network_link\" %q {\n", linkName)
			fmt.Fprintf(&b, "  name                  = \"%s-link\"\n", p.Name)
			b.WriteString("  resource_group_name   = var.resource_group_name\n")
			fmt.Fprintf(&b, "  private_dns_zone_name = azurerm_private_dns_zone.%s.name\n", name)
			fmt.Fprintf(&b, "  virtual_network_id    = azurerm_virtual_network.%s.id\n", tfName(p.NetworkName))
			b.WriteString("}\n")
		}
		return b.String()
	}
	fmt.Fprintf(&b, "resource \"azurerm_dns_zone\" %q {\n", name)
	fmt.Fprintf(&b, "  name                = %q\n", p.Domain)
	b.WriteString("  resource_group_name = var.resource_group_name\n")
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

// ── cdn-service ──────────────────────────────────────────────────────────────
//
// Azure: an Azure Front Door (Standard) profile + endpoint
// (azurerm_cdn_frontdoor_profile + azurerm_cdn_frontdoor_endpoint). Front Door is
// the modern Azure CDN; it serves over HTTPS by default.
func renderCDNAzure(p CDNPlan) string {
	name := tfName(p.Name)
	profileName := name + "_fd"
	endpointName := name + "_fde"
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"azurerm_cdn_frontdoor_profile\" %q {\n", profileName)
	fmt.Fprintf(&b, "  name                = \"%s-fd\"\n", p.Name)
	b.WriteString("  resource_group_name = var.resource_group_name\n")
	b.WriteString("  sku_name            = \"Standard_AzureFrontDoor\"\n")
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n\n")

	fmt.Fprintf(&b, "resource \"azurerm_cdn_frontdoor_endpoint\" %q {\n", endpointName)
	fmt.Fprintf(&b, "  name                     = \"%s-endpoint\"\n", p.Name)
	fmt.Fprintf(&b, "  cdn_frontdoor_profile_id = azurerm_cdn_frontdoor_profile.%s.id\n", profileName)
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

// ── waf-service ──────────────────────────────────────────────────────────────
//
// Azure: azurerm_cdn_frontdoor_firewall_policy — the Front Door WAF policy, with
// the Microsoft managed default rule set, default action Allow, managed rules Block.
func renderWAFAzure(p WAFPlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"azurerm_cdn_frontdoor_firewall_policy\" %q {\n", name)
	fmt.Fprintf(&b, "  name                = %q\n", azureWAFPolicyName(p.Name))
	b.WriteString("  resource_group_name = var.resource_group_name\n")
	b.WriteString("  sku_name            = \"Standard_AzureFrontDoor\"\n")
	b.WriteString("  enabled             = true\n")
	b.WriteString("  mode                = \"Prevention\"\n") // block, not just detect
	b.WriteString("  managed_rule {\n")
	b.WriteString("    type    = \"Microsoft_DefaultRuleSet\"\n")
	b.WriteString("    version = \"2.1\"\n")
	b.WriteString("    action  = \"Block\"\n")
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

// azureWAFPolicyName reduces a name to the Front Door WAF policy charset (letters
// and digits only, must start with a letter, <= 128 chars).
func azureWAFPolicyName(name string) string {
	var sb strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
		}
	}
	out := sb.String()
	if out == "" || (out[0] >= '0' && out[0] <= '9') {
		out = "pyx" + out
	}
	if len(out) > 128 {
		out = out[:128]
	}
	return out
}

// ── managed-kubernetes ───────────────────────────────────────────────────────
//
// Azure: azurerm_kubernetes_cluster (AKS) with a default node pool that
// autoscales (min/max count). The node VM size comes from the catalog (the SAME
// ResolveSKU path). System-assigned identity; the cluster is private-ish by the
// AKS secure defaults.
func renderKubernetesAzure(p K8sPlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	rg := ""
	if p.NetworkName != "" {
		rg = azureRGRef(p.NetworkName)
	}
	fmt.Fprintf(&b, "resource \"azurerm_kubernetes_cluster\" %q {\n", name)
	fmt.Fprintf(&b, "  name                = %q\n", p.Name)
	fmt.Fprintf(&b, "  location            = %q\n", p.CSPRegion)
	if rg != "" {
		fmt.Fprintf(&b, "  resource_group_name = azurerm_resource_group.%s.name\n", rg)
	} else {
		b.WriteString("  resource_group_name = var.resource_group_name\n")
	}
	fmt.Fprintf(&b, "  dns_prefix          = %q\n", p.Name)
	if v := strings.TrimSpace(p.Version); v != "" {
		fmt.Fprintf(&b, "  kubernetes_version  = %q\n", v)
	}
	b.WriteString("  default_node_pool {\n")
	b.WriteString("    name                = \"default\"\n")
	fmt.Fprintf(&b, "    vm_size             = %q\n", p.NodeType)
	b.WriteString("    auto_scaling_enabled = true\n")
	fmt.Fprintf(&b, "    min_count           = %d\n", p.MinNodes)
	fmt.Fprintf(&b, "    max_count           = %d\n", p.MaxNodes)
	if len(p.SubnetNames) > 0 {
		fmt.Fprintf(&b, "    vnet_subnet_id      = azurerm_subnet.%s.id\n", subnetResourceLabel(p.NetworkName, p.SubnetNames[0]))
	}
	b.WriteString("  }\n")
	b.WriteString("  identity {\n")
	b.WriteString("    type = \"SystemAssigned\"\n")
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

// ── secrets-manager ──────────────────────────────────────────────────────────
//
// Azure: azurerm_key_vault — the Key Vault is the secret CONTAINER; the secret
// VALUE is written out-of-band (CI / Vault), never declared in state. soft_delete
// + purge protection are the production-safe defaults (a force-destroy test
// override disables purge protection so a just-created vault can be reclaimed).
func renderSecretsAzure(p SecretsPlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"azurerm_key_vault\" %q {\n", name)
	fmt.Fprintf(&b, "  name                       = %q\n", azureKeyVaultName(p.Name))
	fmt.Fprintf(&b, "  location                   = %q\n", p.CSPRegion)
	b.WriteString("  resource_group_name        = var.resource_group_name\n")
	b.WriteString("  tenant_id                  = var.azure_tenant_id\n")
	b.WriteString("  sku_name                   = \"standard\"\n")
	b.WriteString("  soft_delete_retention_days = 7\n")
	// Production-safe default: purge protection on, unless the test override clears it.
	fmt.Fprintf(&b, "  purge_protection_enabled   = %t\n", !p.ForceDestroy)
	if desc := asciiOnly(p.Description); desc != "" {
		fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\", description = %q }\n", desc)
	} else {
		fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// azureKeyVaultName reduces a name to the Key Vault charset (3-24 chars,
// alphanumerics and hyphens, must start with a letter).
func azureKeyVaultName(name string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			sb.WriteRune(r)
		}
	}
	out := strings.Trim(sb.String(), "-")
	if out == "" || (out[0] >= '0' && out[0] <= '9') {
		out = "kv-" + out
	}
	if len(out) > 24 {
		out = strings.Trim(out[:24], "-")
	}
	for len(out) < 3 {
		out += "0"
	}
	return out
}

// ── serverless-function ──────────────────────────────────────────────────────
//
// Azure: azurerm_service_plan (Consumption Y1) + azurerm_linux_function_app. The
// function is PRIVATE by default (no public route emitted by the macro component);
// the runtime stack comes from the canonical runtime. A storage account backs the
// function app (Azure requires one); we reference it via a variable.
func renderServerlessAzure(p ServerlessPlan) string {
	name := tfName(p.Name)
	planName := name + "_plan"
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"azurerm_service_plan\" %q {\n", planName)
	fmt.Fprintf(&b, "  name                = \"%s-plan\"\n", p.Name)
	fmt.Fprintf(&b, "  location            = %q\n", p.CSPRegion)
	b.WriteString("  resource_group_name = var.resource_group_name\n")
	b.WriteString("  os_type             = \"Linux\"\n")
	b.WriteString("  sku_name            = \"Y1\"\n") // Consumption (serverless) plan
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n\n")

	fmt.Fprintf(&b, "resource \"azurerm_linux_function_app\" %q {\n", name)
	fmt.Fprintf(&b, "  name                       = %q\n", p.Name)
	fmt.Fprintf(&b, "  location                   = %q\n", p.CSPRegion)
	b.WriteString("  resource_group_name        = var.resource_group_name\n")
	fmt.Fprintf(&b, "  service_plan_id            = azurerm_service_plan.%s.id\n", planName)
	b.WriteString("  storage_account_name       = var.function_storage_account_name\n")
	b.WriteString("  storage_account_access_key = var.function_storage_access_key\n")
	b.WriteString("  site_config {\n")
	b.WriteString("    application_stack {\n")
	b.WriteString(azureFunctionStack(p.Runtime, p.ConcreteRuntime))
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return b.String()
}

// azureFunctionStack renders the application_stack block body for a Function App
// from the canonical runtime + the provider-derived concrete runtime version.
func azureFunctionStack(runtime, concrete string) string {
	switch runtime {
	case RuntimeNode:
		// concrete is "nodejs20.x"-ish for AWS; derive the bare major for Azure.
		ver := strings.TrimSuffix(strings.TrimPrefix(concrete, "nodejs"), ".x")
		if ver == "" {
			ver = "20"
		}
		return fmt.Sprintf("      node_version = %q\n", ver)
	case RuntimePython:
		ver := strings.TrimPrefix(concrete, "python")
		if ver == "" {
			ver = "3.12"
		}
		return fmt.Sprintf("      python_version = %q\n", ver)
	default:
		// Go and others run as a custom handler on Azure Functions.
		return "      use_dotnet_isolated_runtime = false\n"
	}
}
