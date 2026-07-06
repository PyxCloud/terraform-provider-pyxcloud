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
