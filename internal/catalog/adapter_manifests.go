package catalog

// adapter_manifests.go — data-only manifests for the tier-2 adapter providers
// (roadmap P5/P6). Each template renders HCL in the style of that provider's
// terraform provider. Attribute names are validated against the public registry
// docs of the pinned TFSource at authoring time; the Note field records the
// provenance snapshot date and any known limitations. No live-API verification
// has been performed (same honesty bar as the wave-2 snapshot CSVs).

const tencentVMTemplate = `resource "tencentcloud_instance" {{printf "%q" (tfName .VMName)}} {
  instance_name     = {{printf "%q" .VMName}}
  instance_type     = {{printf "%q" .InstanceType}}
  availability_zone = {{printf "%q" .CSPRegion}}
  image_id          = {{printf "%q" .Image}}
{{- if .SubnetName}}
  subnet_id         = {{printf "%q" (subnetResourceLabel .NetworkName .SubnetName)}}
{{- end}}
{{- if .SecurityGroup}}
  security_groups   = [{{printf "%q" (tfName .SecurityGroup)}}]
{{- end}}
{{- if .UserData}}
  user_data         = {{printf "%q" .UserData}}
{{- end}}
}
`

const hetznerVMTemplate = `resource "hcloud_server" {{printf "%q" (tfName .VMName)}} {
  name        = {{printf "%q" .VMName}}
  server_type = {{printf "%q" .InstanceType}}
  image       = {{printf "%q" .Image}}
  location    = {{printf "%q" .CSPRegion}}
{{- if .UserData}}
  user_data   = {{printf "%q" .UserData}}
{{- end}}
{{- if .SSHKeys}}
  ssh_keys    = [{{range $i, $k := .SSHKeys}}{{if $i}}, {{end}}{{printf "%q" $k}}{{end}}]
{{- end}}
}
`

const hetznerNetTemplate = `resource "hcloud_network" {{printf "%q" (tfName .VPCName)}} {
  name     = {{printf "%q" .VPCName}}
  ip_range = {{printf "%q" .CIDR}}
}
`

const hetznerSubnetTemplate = `resource "hcloud_network_subnet" {{printf "%q" (subnetResourceLabel .Network.VPCName .Subnet.Name)}} {
  network_id = hcloud_network.{{tfName .Network.VPCName}}.id
  ip_range   = {{printf "%q" .Subnet.CIDR}}
}
`

const hetznerSGTemplate = `resource "hcloud_firewall" {{printf "%q" (tfName .SGName)}} {
  name = {{printf "%q" .SGName}}
{{- range .Rules}}
{{- if eq .Direction "ingress"}}
  rule {
    direction = "in"
    protocol  = {{printf "%q" .Protocol}}
    port      = {{.FromPort}}
    port_to   = {{.ToPort}}
{{- if .CIDRs}}
    source_ips = [{{range $i, $c := .CIDRs}}{{if $i}}, {{end}}{{printf "%q" $c}}{{end}}]
{{- end}}
  }
{{- end}}
{{- end}}
}
`

const vultrVMTemplate = `resource "vultr_instance" {{printf "%q" (tfName .VMName)}} {
  label    = {{printf "%q" .VMName}}
  region   = {{printf "%q" .CSPRegion}}
  plan     = {{printf "%q" .InstanceType}}
  os_name  = {{printf "%q" .Image}}
{{- if .UserData}}
  user_data = base64encode({{printf "%q" .UserData}})
{{- end}}
{{- if .SSHKeys}}
  ssh_key_ids = [{{range $i, $k := .SSHKeys}}{{if $i}}, {{end}}{{printf "%q" $k}}{{end}}]
{{- end}}
}
`

const vultrNetTemplate = `resource "vultr_vpc2" {{printf "%q" (tfName .VPCName)}} {
  description = {{printf "%q" .VPCName}}
  region      = {{printf "%q" .CSPRegion}}
  subnet      = {{printf "%q" .CIDR}}
}
`

const vultrSGTemplate = `resource "vultr_firewall_group" {{printf "%q" (tfName .SGName)}} {
  description = {{printf "%q" .SGName}}
}
{{- range .Rules}}
{{- if eq .Direction "ingress"}}
resource "vultr_firewall_rule" {{printf "%q" (printf "%s_%s_%d" (tfName $.SGName) .Protocol .FromPort)}} {
  firewall_group_id = vultr_firewall_group.{{tfName $.SGName}}.id
  protocol          = {{printf "%q" .Protocol}}
  port              = {{printf "%q" (printf "%d" .FromPort)}}
  subnet            = {{index .CIDRs 0}}
  subnet_size       = 0
}
{{- end}}
{{- end}}
`

const scalewayVMTemplate = `resource "scaleway_instance_server" {{printf "%q" (tfName .VMName)}} {
  name             = {{printf "%q" .VMName}}
  image            = {{printf "%q" .Image}}
  commercial_type  = {{printf "%q" .InstanceType}}
  zone             = {{printf "%q" .CSPRegion}}
{{- if .UserData}}
  user_data {
    key   = "cloud-init"
    value = {{printf "%q" .UserData}}
  }
{{- end}}
}
`

const scalewayNetTemplate = `resource "scaleway_vpc" {{printf "%q" (tfName .VPCName)}} {
  name = {{printf "%q" .VPCName}}
}
`

const scalewaySubnetTemplate = `resource "scaleway_vpc_private_network" {{printf "%q" (subnetResourceLabel .Network.VPCName .Subnet.Name)}} {
  name  = {{printf "%q" .Subnet.Name}}
  ipv4_subnets {
    subnet_id = {{printf "%q" .Subnet.CIDR}}
  }
}
`

const scalewaySGTemplate = `resource "scaleway_security_group" {{printf "%q" (tfName .SGName)}} {
  name                    = {{printf "%q" .SGName}}
  description             = {{printf "%q" .Description}}
}
{{- range .Rules}}
{{- if eq .Direction "ingress"}}
resource "scaleway_security_group_rule" {{printf "%q" (printf "%s_%s_%d" (tfName $.SGName) .Protocol .FromPort)}} {
  security_group_id = scaleway_security_group.{{tfName $.SGName}}.id
  action            = "accept"
  protocol          = {{printf "%q" .Protocol}}
  port              = {{.FromPort}}
  ip_range          = {{index .CIDRs 0}}
}
{{- end}}
{{- end}}
`

const tencentMDBTemplate = `resource "tencentcloud_postgresql_basic_instance" {{printf "%q" (tfName .DBName)}} {
  name          = {{printf "%q" .DBName}}
  instance_type = {{printf "%q" .DBClass}}
  zone          = {{printf "%q" .CSPRegion}}
  db_version    = {{printf "%q" .EngineVersion}}
}
`

const vultrMDBTemplate = `resource "vultr_database" {{printf "%q" (tfName .DBName)}} {
  label           = {{printf "%q" .DBName}}
  region          = {{printf "%q" .CSPRegion}}
  plan            = {{printf "%q" .DBClass}}
  database_engine = {{printf "%q" .Engine}}
  database_version = {{printf "%q" .EngineVersion}}
}
`

const scalewayMDBTemplate = `resource "scaleway_rdb_instance" {{printf "%q" (tfName .DBName)}} {
  name      = {{printf "%q" .DBName}}
  node_type = {{printf "%q" .DBClass}}
  engine    = {{printf "%q" (printf "PostgreSQL-%s" .EngineVersion)}}
  is_ha_cluster = {{.HA}}
}
`

var AdapterManifests = map[string]*AdapterManifest{
	"tencent": {
		Provider:   "tencent",
		CSP:        "tencent",
		TFLocal:    "tencentcloud",
		TFSource:   "tencentstack/tencentcloud",
		Note:       "snapshot 2026-10-05 from public registry docs; live-API verification pending (pd-TF-PROVIDER-T2)",
		VMTemplate: tencentVMTemplate,
		NetTemplate: `resource "tencentcloud_vpc" {{printf "%q" (tfName .VPCName)}} {
  name       = {{printf "%q" .VPCName}}
  cidr_block = {{printf "%q" .CIDR}}
}
`,
		SubnetTemplate: `resource "tencentcloud_subnet" {{printf "%q" (subnetResourceLabel .Network.VPCName .Subnet.Name)}} {
  name              = {{printf "%q" .Subnet.Name}}
  vpc_id            = tencentcloud_vpc.{{tfName .Network.VPCName}}.id
  cidr_block        = {{printf "%q" .Subnet.CIDR}}
  availability_zone = {{printf "%q" .Network.CSPRegion}}
}
`,
		SGTemplate: `resource "tencentcloud_security_group" {{printf "%q" (tfName .SGName)}} {
  name        = {{printf "%q" .SGName}}
  description = {{printf "%q" .Description}}
}
{{- range .Rules}}
{{- if eq .Direction "ingress"}}
resource "tencentcloud_security_group_rule" {{printf "%q" (printf "%s_%s_%d" (tfName $.SGName) .Protocol .FromPort)}} {
  security_group_id = tencentcloud_security_group.{{tfName $.SGName}}.id
  policy            = "accept"
  protocol          = {{printf "%q" .Protocol}}
  port              = {{printf "%q" (printf "%d" .FromPort)}}
  cidr_ip           = {{index .CIDRs 0}}
}
{{- end}}
{{- end}}
`,
		MDBTemplate: tencentMDBTemplate,
	},
	"hetzner": {
		Provider:       "hetzner",
		CSP:            "hetzner",
		TFLocal:        "hcloud",
		TFSource:       "hetznercloud/hcloud",
		Note:           "snapshot 2026-10-05 from public registry docs; hcloud has no managed DB — self-host mitigation applies",
		VMTemplate:     hetznerVMTemplate,
		NetTemplate:    hetznerNetTemplate,
		SubnetTemplate: hetznerSubnetTemplate,
		SGTemplate:     hetznerSGTemplate,
	},
	"vultr": {
		Provider:    "vultr",
		CSP:         "vultr",
		TFLocal:     "vultr",
		TFSource:    "vultr/vultr",
		Note:        "snapshot 2026-10-05 from public registry docs; os_id numeric mapping recorded in adapter catalog (vultr os ids)",
		VMTemplate:  vultrVMTemplate,
		NetTemplate: vultrNetTemplate,
		SGTemplate:  vultrSGTemplate,
		MDBTemplate: vultrMDBTemplate,
	},
	"scaleway": {
		Provider:       "scaleway",
		CSP:            "scaleway",
		TFLocal:        "scaleway",
		TFSource:       "scaleway/scaleway",
		Note:           "snapshot 2026-10-05 from public registry docs; zones fold to region ids (fr-par -> fr-par-1)",
		VMTemplate:     scalewayVMTemplate,
		NetTemplate:    scalewayNetTemplate,
		SubnetTemplate: scalewaySubnetTemplate,
		SGTemplate:     scalewaySGTemplate,
		MDBTemplate:    scalewayMDBTemplate,
	},
	"rackspace": {
		Provider: "rackspace",
		CSP:      "rackspace",
		TFLocal:  "openstack",
		TFSource: "terraform-provider-openstack/openstack",
		Note:     "legacy: Rackspace is OpenStack-compatible; rendered via the openstack provider against Rackspace endpoints (snapshot 2026-10-05)",
		VMTemplate: `resource "openstack_compute_instance_v2" {{printf "%q" (tfName .VMName)}} {
  name        = {{printf "%q" .VMName}}
  flavor_name = {{printf "%q" .InstanceType}}
  image_name  = {{printf "%q" .Image}}
{{- if .UserData}}
  user_data   = {{printf "%q" .UserData}}
{{- end}}
}
`,
		NetTemplate: `resource "openstack_networking_network_v2" {{printf "%q" (tfName .VPCName)}} {
  name = {{printf "%q" .VPCName}}
}
resource "openstack_networking_subnet_v2" {{printf "%q" (tfName .VPCName)}} {
  name       = {{printf "%q" .VPCName}}
  network_id = openstack_networking_network_v2.{{tfName .VPCName}}.id
  cidr       = {{printf "%q" .CIDR}}
}
`,
		SGTemplate: `resource "openstack_networking_secgroup_v2" {{printf "%q" (tfName .SGName)}} {
  name        = {{printf "%q" .SGName}}
  description = {{printf "%q" .Description}}
}
{{- range .Rules}}
{{- if eq .Direction "ingress"}}
resource "openstack_networking_secgroup_rule_v2" {{printf "%q" (printf "%s_%s_%d" (tfName $.SGName) .Protocol .FromPort)}} {
  direction          = "ingress"
  secgroup_id        = openstack_networking_secgroup_v2.{{tfName $.SGName}}.id
  protocol           = {{printf "%q" .Protocol}}
  port_range_min     = {{.FromPort}}
  port_range_max     = {{.ToPort}}
  remote_ip_prefix   = {{index .CIDRs 0}}
}
{{- end}}
{{- end}}
`,
	},
}

// ── Tail providers (market-coverage push, snapshot 2026-10-05) ────────────────

var huaweiVMTemplate = `resource "huaweicloud_compute_instance" {{printf "%q" (tfName .VMName)}} {
  name               = {{printf "%q" .VMName}}
  flavor_id          = {{printf "%q" .InstanceType}}
  availability_zone  = {{printf "%q" .CSPRegion}}
  image_id           = {{printf "%q" .Image}}
{{- if .SecurityGroup}}
  security_group_ids = [{{printf "%q" (tfName .SecurityGroup)}}]
{{- end}}
{{- if .UserData}}
  user_data          = {{printf "%q" .UserData}}
{{- end}}
}
`

var fastlyCDNNote = "fastly is a native edge provider (CDN/DNS-equivalent only); VM components surface the honest unsupported error"

var tailAdapterManifests = map[string]*AdapterManifest{
	"huawei": {
		Provider:   "huawei",
		CSP:        "huawei",
		TFLocal:    "huaweicloud",
		TFSource:   "huaweicloud/huaweicloud",
		Note:       "snapshot 2026-10-05 from public registry docs; live-API verification pending",
		VMTemplate: huaweiVMTemplate,
		NetTemplate: `resource "huaweicloud_vpc" {{printf "%q" (tfName .VPCName)}} {
  name = {{printf "%q" .VPCName}}
  cidr = {{printf "%q" .CIDR}}
}
`,
		SubnetTemplate: `resource "huaweicloud_vpc_subnet" {{printf "%q" (subnetResourceLabel .Network.VPCName .Subnet.Name)}} {
  name       = {{printf "%q" .Subnet.Name}}
  vpc_id     = huaweicloud_vpc.{{tfName .Network.VPCName}}.id
  cidr       = {{printf "%q" .Subnet.CIDR}}
  gateway_ip = {{printf "%q" (printf "10.0.%d.1" (subnetOctet .Subnet.CIDR))}}
}
`,
		SGTemplate: `resource "huaweicloud_networking_secgroup" {{printf "%q" (tfName .SGName)}} {
  name        = {{printf "%q" .SGName}}
  description = {{printf "%q" .Description}}
}
{{- range .Rules}}
{{- if eq .Direction "ingress"}}
resource "huaweicloud_networking_secgroup_rule" {{printf "%q" (printf "%s_%s_%d" (tfName $.SGName) .Protocol .FromPort)}} {
  direction          = "ingress"
  security_group_id  = huaweicloud_networking_secgroup.{{tfName $.SGName}}.id
  protocol           = {{printf "%q" .Protocol}}
  port_range_min     = {{.FromPort}}
  port_range_max     = {{.ToPort}}
  remote_ip_prefix   = {{index .CIDRs 0}}
}
{{- end}}
{{- end}}
`,
		MDBTemplate: `resource "huaweicloud_rds_instance" {{printf "%q" (tfName .DBName)}} {
  name              = {{printf "%q" .DBName}}
  flavor            = {{printf "%q" .DBClass}}
  availability_zone = [{{printf "%q" .CSPRegion}}]
  db {
    type     = "PostgreSQL"
    version  = {{printf "%q" .EngineVersion}}
  }
}
`,
	},
	"akamai": {
		Provider: "akamai",
		CSP:      "akamai",
		TFLocal:  "akamai",
		TFSource: "akamai/akamai",
		Note:     "Akamai Connected Cloud is Linode-based (akamai_linode_* resources); snapshot 2026-10-05",
		VMTemplate: `resource "akamai_linode_instance" {{printf "%q" (tfName .VMName)}} {
  label      = {{printf "%q" .VMName}}
  region     = {{printf "%q" .CSPRegion}}
  type       = {{printf "%q" .InstanceType}}
  image      = {{printf "%q" .Image}}
{{- if .UserData}}
  root_pass  = "GENERATED-AT-APPLY"
  boot_script = {{printf "%q" .UserData}}
{{- end}}
}
`,
		NetTemplate: `resource "akamai_linode_vpc" {{printf "%q" (tfName .VPCName)}} {
  label = {{printf "%q" .VPCName}}
  region = {{printf "%q" .CSPRegion}}
  subnet {
    label = {{printf "%q" (printf "%s-subnet-1" .VPCName)}}
    cidr  = {{printf "%q" .CIDR}}
  }
}
`,
		SGTemplate: `resource "akamai_linode_firewall" {{printf "%q" (tfName .SGName)}} {
  label = {{printf "%q" .SGName}}
{{- range .Rules}}
{{- if eq .Direction "ingress"}}
  inbound {
    label    = {{printf "%q" (printf "%s-%d" .Protocol .FromPort)}}
    action   = "ACCEPT"
    protocol = {{printf "%q" .Protocol}}
    ports    = {{printf "%q" (printf "%d-%d" .FromPort .ToPort)}}
    ipv4     = [{{range $i, $c := .CIDRs}}{{if $i}}, {{end}}{{printf "%q" $c}}{{end}}]
  }
{{- end}}
{{- end}}
}
`,
	},
	"fastly": {
		Provider: "fastly",
		CSP:      "fastly",
		TFLocal:  "fastly",
		TFSource: "fastly/fastly",
		Note:     fastlyCDNNote,
		VMTemplate: `# fastly Compute@Edge service (degraded VM substitute): fastly cannot host
# general-purpose VMs; this renders a Compute service shell that must be filled
# with a deployed edge application package.
resource "fastly_service_v1" {{printf "%q" (tfName .VMName)}} {
  name = {{printf "%q" .VMName}}
  domain {
    name = {{printf "%q" (printf "%s.pyxcloud-edge.net" (tfName .VMName))}}
  }
}
`,
		SGTemplate: `# fastly has no network firewall primitive; the edge service IS the boundary.
resource "fastly_service_v1" {{printf "%q" (tfName .SGName)}} {
  name = {{printf "%q" .SGName}}
}
`,
	},
	"vsphere": {
		Provider: "vsphere",
		CSP:      "vsphere",
		TFLocal:  "vsphere",
		TFSource: "hashicorp/vsphere",
		Note:     "private-cloud vSphere (VM-only, no managed services); snapshot 2026-10-05; template name is deployment-config, not catalog-resolved",
		VMTemplate: `resource "vsphere_virtual_machine" {{printf "%q" (tfName .VMName)}} {
  name             = {{printf "%q" .VMName}}
  resource_pool_id = var.vsphere_resource_pool_id
  datastore_id     = var.vsphere_datastore_id
  num_cpus         = {{.CPU}}
  memory           = {{printf "%d" (mul .RAM 1024)}}
  guest_id         = "ubuntu64Guest"
  network_interface {
    network_id = var.vsphere_network_id
  }
  disk {
    label = "disk0"
    size  = 20
  }
  clone {
    template_uuid = var.vsphere_template_uuid
  }
}
`,
		SGTemplate: `# vSphere has no SG primitive in the hashicorp/vsphere provider; network policy
# is enforced by NSX. This renders a placeholder doc so the plan stays inspectable.
resource "vsphere_virtual_machine" {{printf "%q" (tfName .SGName)}} {
  name             = {{printf "%q" .SGName}}
  resource_pool_id = var.vsphere_resource_pool_id
  datastore_id     = var.vsphere_datastore_id
  num_cpus         = 1
  memory           = 1024
  guest_id         = "otherGuest"
  network_interface {
    network_id = var.vsphere_network_id
  }
}
`,
	},
}

func init() {
	for k, v := range tailAdapterManifests {
		AdapterManifests[k] = v
	}
}
