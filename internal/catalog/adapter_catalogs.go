package catalog

// adapter_catalogs.go — embedded tier-2 snapshots for the adapter providers
// (roadmap P5/P6). Same CSV shape as the wave-2 snapshot files (region/vm/os/mdb)
// and parsed by foldAdapterCatalog into the SAME shared indexes, so adapter
// providers resolve through the identical Translate* path as wave-1/wave-2.
//
// Provenance (snapshot 2026-10-05): rows authored from public provider docs and
// pricing pages of each cloud (tencentcloud.com, docs.hetzner.cloud, vultr.com,
// scaleway.com, rackspace.com). Not live-API verified. Image/OS ids are real
// registry tokens where public (hcloud image names, vultr os_id, scaleway image
// labels, tencentcloud public image ids); mark any changed id as a snapshot
// refresh, not a code change.

const tencentAdapterCSV = `# Tencent Cloud adapter snapshot 2026-10-05 (ap-guangzhou / eu-frankfurt / ap-singapore)
region,Asia Pacific,China,Guangzhou,ap-guangzhou,Guangzhou,tencent
region,Europe,Germany,Frankfurt,eu-frankfurt,Frankfurt,tencent
region,Asia Pacific,Singapore,Singapore,ap-singapore,Singapore,tencent
vm,S5.MEDIUM4,S5,ap-guangzhou,x86_64,2,4,0,true
vm,S5.LARGE8,S5,ap-guangzhou,x86_64,4,8,0,true
vm,S5.MEDIUM4,S5,eu-frankfurt,x86_64,2,4,0,true
vm,S5.LARGE8,S5,ap-singapore,x86_64,4,8,0,true
os,ap-guangzhou,ubuntu,22.04,x86_64,img-22trbe82
os,ap-guangzhou,ubuntu,24.04,x86_64,img-487padv6
os,eu-frankfurt,ubuntu,22.04,x86_64,img-22trbe82
os,ap-singapore,ubuntu,22.04,x86_64,img-22trbe82
mdb,PG-SMALL2,pg-s4,ap-guangzhou,postgres,2,4
mdb,PG-SMALL2,pg-s4,ap-singapore,postgres,2,4
`

const hetznerAdapterCSV = `# Hetzner Cloud adapter snapshot 2026-10-05 (fsn1/nbg1/hel1/ash) — no managed DB
region,Europe,Germany,Falkenstein,fsn1,Falkenstein DC Park 1,hetzner
region,Europe,Germany,Nuremberg,nbg1,Nuremberg,hetzner
region,Europe,Finland,Helsinki,hel1,Helsinki,hetzner
region,North America,USA,Ashburn,ash,Ashburn VA,hetzner
vm,cx22,cx,fsn1,x86_64,2,4,0,true
vm,cx32,cx,fsn1,x86_64,4,8,0,true
vm,cx42,cx,nbg1,x86_64,8,16,0,true
vm,cax41,cax,hel1,arm64,8,16,0,true
vm,cx22,cx,ash,x86_64,2,4,0,true
os,fsn1,ubuntu,22.04,x86_64,ubuntu-22.04
os,fsn1,ubuntu,24.04,x86_64,ubuntu-24.04
os,nbg1,ubuntu,22.04,x86_64,ubuntu-22.04
os,hel1,ubuntu,22.04,x86_64,ubuntu-22.04
os,ash,ubuntu,22.04,x86_64,ubuntu-22.04
`

const vultrAdapterCSV = `# Vultr adapter snapshot 2026-10-05 (ewr/ams/fra) — os_id numeric mapping recorded in image column
region,North America,USA,New Jersey,ewr,New Jersey,vultr
region,Europe,Netherlands,Amsterdam,ams,Amsterdam,vultr
region,Europe,Germany,Frankfurt,fra,Frankfurt,vultr
vm,vc2-2c-4gb,vc2,ewr,x86_64,2,4,0,false
vm,vc2-4c-8gb,vc2,ewr,x86_64,4,8,0,false
vm,vc2-2c-4gb,vc2,ams,x86_64,2,4,0,false
vm,vc2-4c-8gb,vc2,fra,x86_64,4,8,0,false
os,ewr,ubuntu,22.04,x86_64,Ubuntu 22.04 x64
os,ewr,ubuntu,24.04,x86_64,Ubuntu 24.04 x64
os,ams,ubuntu,22.04,x86_64,Ubuntu 22.04 x64
os,fra,ubuntu,22.04,x86_64,Ubuntu 22.04 x64
mdb,vc2-2c-2gb,dbaas,ewr,postgres,2,2
mdb,vc2-2c-2gb,dbaas,ams,postgres,2,2
`

const scalewayAdapterCSV = `# Scaleway adapter snapshot 2026-10-05 (fr-par/nl-ams/pl-waw) — region ids fold to zone fr-par-1
region,Europe,France,Paris,fr-par-1,Paris Zone 1,scaleway
region,Europe,Netherlands,Amsterdam,nl-ams-1,Amsterdam Zone 1,scaleway
region,Europe,Poland,Warsaw,pl-waw-1,Warsaw Zone 1,scaleway
vm,DEV1-S,DEV1,fr-par-1,x86_64,2,4,0,false
vm,DEV1-M,DEV1,fr-par-1,x86_64,3,8,0,false
vm,DEV1-S,DEV1,nl-ams-1,x86_64,2,4,0,false
vm,GP1-XS,GP1,pl-waw-1,arm64,4,16,0,false
os,fr-par-1,ubuntu,22.04,x86_64,ubuntu_jammy
os,fr-par-1,ubuntu,24.04,x86_64,ubuntu_noble
os,nl-ams-1,ubuntu,22.04,x86_64,ubuntu_jammy
os,pl-waw-1,ubuntu,22.04,x86_64,ubuntu_jammy
mdb,DB-DEV-S,DB-DEV,fr-par-1,postgres,2,4
mdb,DB-DEV-S,DB-DEV,nl-ams-1,postgres,2,4
`

const rackspaceAdapterCSV = `# Rackspace adapter snapshot 2026-10-05 (LON/ORD/DFW/SYD) — OpenStack-compatible endpoints; legacy performance flavors
region,Europe,UK,London,LON,London (ORD/DFW/SYD also available),rackspace
region,North America,USA,Chicago,ORD,Chicago,rackspace
region,North America,USA,Dallas,DFW,Dallas-Fort Worth,rackspace
region,Oceania,Australia,Sydney,SYD,Sydney,rackspace
vm,performance1-4,performance,LON,x86_64,2,4,0,false
vm,performance1-8,performance,LON,x86_64,4,8,0,false
vm,performance2-15,performance,ORD,x86_64,15,128,0,false
os,LON,ubuntu,22.04,x86_64,Ubuntu 22.04 LTS (Focal Fossa)
os,ORD,ubuntu,22.04,x86_64,Ubuntu 22.04 LTS (Focal Fossa)
os,DFW,ubuntu,22.04,x86_64,Ubuntu 22.04 LTS (Focal Fossa)
`

// ── Tail provider snapshots (market-coverage push, 2026-10-05) ────────────────

const huaweiAdapterCSV = `# Huawei Cloud adapter snapshot 2026-10-05 (cn-north-4 / eu-west) — pseudo-flavors from public docs
region,Asia Pacific,China,Beijing,cn-north-4,Beijing-4,huawei
region,Europe,Ireland,Dublin,eu-west-0,Dublin,huawei
vm,s6.small.1,s6,cn-north-4,x86_64,1,2,0,true
vm,s6.large.2,s6,cn-north-4,x86_64,2,8,0,true
vm,s6.large.2,s6,eu-west-0,x86_64,2,8,0,true
os,cn-north-4,ubuntu,22.04,x86_64,ubuntu-22.04-lts
os,eu-west-0,ubuntu,22.04,x86_64,ubuntu-22.04-lts
mdb,rds.pg.c2.large,rds.pg,cn-north-4,postgres,2,8
`

const akamaiAdapterCSV = `# Akamai Connected Cloud snapshot 2026-10-05 (Linode-compatible regions/SKUs)
region,North America,USA,Chicago,us-ord,Chicago,akamai
region,Europe,UK,London,eu-lon,London,akamai
region,Asia Pacific,Japan,Osaka,ap-osaka,Osaka,akamai
vm,g6-standard-2,g6,us-ord,x86_64,2,8,0,true
vm,g6-standard-4,g6,eu-lon,x86_64,4,8,0,true
vm,g6-standard-2,g6,ap-osaka,x86_64,2,8,0,true
os,us-ord,ubuntu,22.04,x86_64,linode/ubuntu22.04
os,eu-lon,ubuntu,22.04,x86_64,linode/ubuntu22.04
os,ap-osaka,ubuntu,22.04,x86_64,linode/ubuntu22.04
`

const fastlyAdapterCSV = `# Fastly snapshot 2026-10-05 — global anycast edge, no VMs
region,Global,Global,Global,global,Fastly global anycast edge network,fastly
`

const vsphereAdapterCSV = `# vSphere snapshot 2026-10-05 — on-prem/private cloud; one pseudo-region per datacenter policy
region,Private,On-prem,Datacenter,datacenter,Default vSphere datacenter,vsphere
`

var tailAdapterCatalogs = map[string]string{
	"huawei":  huaweiAdapterCSV,
	"akamai":  akamaiAdapterCSV,
	"fastly":  fastlyAdapterCSV,
	"vsphere": vsphereAdapterCSV,
}
