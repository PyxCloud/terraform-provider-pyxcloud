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
