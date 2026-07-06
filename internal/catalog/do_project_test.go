package catalog

import (
	"strings"
	"testing"
)

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
