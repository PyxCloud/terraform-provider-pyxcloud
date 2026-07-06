package catalog

import (
	"context"
	"strings"
	"testing"
)

func TestFullEstateDO_ProjectPlacementWiring(t *testing.T) {
	// End-to-end: a DO estate with DOProject set emits the project data source ONCE
	// and every scale-group droplet_template references it (so self-healed droplets
	// land in the environment's project). Without DOProject: neither appears.
	in := FullEstateInput("digitalocean", "Frankfurt", "x86_64", "ubuntu", "1.30")
	in.DOProject = "pyxcloud-production"
	docs, err := AssembleHCL(context.Background(), MustEmbedded(), in)
	if err != nil {
		t.Fatalf("AssembleHCL DO estate: %v", err)
	}
	all := strings.Join(docs, "\n")
	dsName := doProjectDataSourceName("pyxcloud-production")

	if got := strings.Count(all, "data \"digitalocean_project\" \""+dsName+"\""); got != 1 {
		t.Fatalf("expected exactly one digitalocean_project data source, got %d", got)
	}
	ref := "project_id         = data.digitalocean_project." + dsName + ".id"
	if !strings.Contains(all, ref) {
		t.Fatalf("scale-group droplet_templates must reference the project data source (%q)", ref)
	}
	// Every autoscale pool must carry it (else that pool's self-heals still drift).
	pools := strings.Count(all, "resource \"digitalocean_droplet_autoscale\"")
	refs := strings.Count(all, ref)
	if pools == 0 || refs != pools {
		t.Fatalf("every autoscale pool must carry project_id: %d pools, %d refs", pools, refs)
	}

	// Without DOProject: no data source, no project_id.
	in2 := FullEstateInput("digitalocean", "Frankfurt", "x86_64", "ubuntu", "1.30")
	docs2, err := AssembleHCL(context.Background(), MustEmbedded(), in2)
	if err != nil {
		t.Fatalf("AssembleHCL DO estate (no project): %v", err)
	}
	all2 := strings.Join(docs2, "\n")
	if strings.Contains(all2, "digitalocean_project") || strings.Contains(all2, "project_id") {
		t.Fatalf("no DOProject must not emit any project placement")
	}
}

func TestRenderDOProjectResources(t *testing.T) {
	// Empty project => no block.
	if RenderDOProjectResources("", []string{`resource "digitalocean_droplet" "x" {`}) != "" {
		t.Fatalf("empty project must emit nothing")
	}
	// No assignable resources => no block (autoscale + vpc + firewall are excluded).
	only := []string{
		"resource \"digitalocean_droplet_autoscale\" \"sso\" {\n}\n",
		"resource \"digitalocean_vpc\" \"net\" {\n}\n",
		"resource \"digitalocean_firewall\" \"fw\" {\n}\n",
		"resource \"digitalocean_container_registry\" \"reg\" {\n}\n",
	}
	if got := RenderDOProjectResources("pyxcloud-production", only); got != "" {
		t.Fatalf("non-assignable resources must not be bound, got:\n%s", got)
	}

	docs := []string{
		"resource \"digitalocean_database_cluster\" \"pg\" {\n}\n",
		"resource \"digitalocean_loadbalancer\" \"edge-lb\" {\n}\n",
		"resource \"digitalocean_reserved_ip\" \"mcp\" {\n}\n",
		"resource \"digitalocean_droplet_autoscale\" \"sso\" {\n}\n", // excluded
		"resource \"digitalocean_vpc\" \"net\" {\n}\n",               // excluded
		"resource \"digitalocean_spaces_bucket\" \"artifacts\" {\n}\n",
	}
	got := RenderDOProjectResources("pyxcloud-production", docs)
	dsName := doProjectDataSourceName("pyxcloud-production")
	for _, want := range []string{
		"resource \"digitalocean_project_resources\" \"" + dsName + "\"",
		"project = data.digitalocean_project." + dsName + ".id",
		"digitalocean_database_cluster.pg.urn,",
		"digitalocean_loadbalancer.edge-lb.urn,",
		"digitalocean_reserved_ip.mcp.urn,",
		"digitalocean_spaces_bucket.artifacts.urn,",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("project_resources missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"droplet_autoscale", "digitalocean_vpc"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("project_resources must not bind %q:\n%s", unwanted, got)
		}
	}
	// A header indented inside a heredoc/string body must NOT be picked up: real
	// top-level HCL resources are always at column 0, so the regex anchors there.
	tricky := []string{"resource \"x\" \"y\" {\n  user_data = <<EOT\n  resource \"digitalocean_droplet\" \"evil\" {\n  EOT\n}\n"}
	if strings.Contains(RenderDOProjectResources("p", tricky), "evil") {
		t.Fatalf("must not match a header indented inside a heredoc")
	}
}

func TestFullEstateDO_ProjectResourcesBinding(t *testing.T) {
	in := FullEstateInput("digitalocean", "Frankfurt", "x86_64", "ubuntu", "1.30")
	in.DOProject = "pyxcloud-production"
	docs, err := AssembleHCL(context.Background(), MustEmbedded(), in)
	if err != nil {
		t.Fatalf("AssembleHCL DO estate: %v", err)
	}
	all := strings.Join(docs, "\n")
	dsName := doProjectDataSourceName("pyxcloud-production")

	if got := strings.Count(all, "resource \"digitalocean_project_resources\" \""+dsName+"\""); got != 1 {
		t.Fatalf("expected exactly one project_resources block, got %d", got)
	}
	// The estate has a managed database + reserved IP + load balancer; each must be
	// bound by urn.
	for _, want := range []string{
		"digitalocean_database_cluster.",
		"digitalocean_reserved_ip.",
	} {
		if !strings.Contains(all, want) || !strings.Contains(all, ".urn,") {
			t.Fatalf("estate project_resources missing binding for %q", want)
		}
	}
	// Without do_project: no project_resources block at all.
	in2 := FullEstateInput("digitalocean", "Frankfurt", "x86_64", "ubuntu", "1.30")
	docs2, err := AssembleHCL(context.Background(), MustEmbedded(), in2)
	if err != nil {
		t.Fatalf("AssembleHCL DO estate (no project): %v", err)
	}
	if strings.Contains(strings.Join(docs2, "\n"), "digitalocean_project_resources") {
		t.Fatalf("no do_project must not emit a project_resources block")
	}
}

func TestRenderDOProjectDataSource(t *testing.T) {
	empty := RenderDOProjectDataSource("")
	if empty != "" {
		t.Fatalf("empty project name must emit nothing, got %q", empty)
	}
	if RenderDOProjectDataSource("   ") != "" {
		t.Fatalf("blank project name must emit nothing")
	}

	ds := RenderDOProjectDataSource("pyxcloud-production")
	dsName := doProjectDataSourceName("pyxcloud-production")
	for _, want := range []string{
		"data \"digitalocean_project\" \"" + dsName + "\"",
		"name = \"pyxcloud-production\"",
	} {
		if !strings.Contains(ds, want) {
			t.Fatalf("data source missing %q:\n%s", want, ds)
		}
	}
}

func TestRenderScaleGroupDO_ProjectPlacement(t *testing.T) {
	base := ScaleGroupPlan{
		Provider: ProviderDigitalOcean, GroupName: "backend", CSPRegion: "fra1",
		InstanceType: "s-2vcpu-4gb", Image: "ubuntu-24-04-x64", NetworkName: "net",
		Min: 2, Max: 3,
	}

	// Without a project: no project_id (account-default, legacy).
	if got := renderScaleGroupDO(base); strings.Contains(got, "project_id") {
		t.Fatalf("no DOProject must not emit project_id:\n%s", got)
	}

	// With a project: the droplet_template references the project data source, so
	// self-healed members land in that project.
	withProj := base
	withProj.DOProject = "pyxcloud-production"
	got := renderScaleGroupDO(withProj)
	wantRef := "project_id         = data.digitalocean_project." +
		doProjectDataSourceName("pyxcloud-production") + ".id"
	if !strings.Contains(got, wantRef) {
		t.Fatalf("droplet_template must reference the project data source (%q):\n%s", wantRef, got)
	}
	// The reference name must match what RenderDOProjectDataSource defines.
	ds := RenderDOProjectDataSource(withProj.DOProject)
	if !strings.Contains(ds, doProjectDataSourceName(withProj.DOProject)) {
		t.Fatalf("data source name must match the reference")
	}
}
