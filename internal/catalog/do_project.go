package catalog

import (
	"fmt"
	"strings"
)

// DigitalOcean project placement (per-environment).
//
// DO resources with no explicit project land in the account-DEFAULT project. With
// two projects (pyxcloud-production, pyxcloud-staging) that means every resource
// defaulted to staging, and — worse — a self-healed droplet_autoscale member
// silently re-landed in the default project, bleeding prod droplets into staging.
//
// The fix: the environment carries a DO PROJECT NAME (from the account binding),
// and the render (a) emits one digitalocean_project data source that looks the
// project up by name and (b) references it from resources (droplet_template
// project_id today; other resource kinds via digitalocean_project_resources next)
// so placement is decided by the pipeline/IaC, deterministically, per env.

// doProjectDataSourceName derives the Terraform local name for the
// digitalocean_project data source from the project NAME (local names can't carry
// dots/dashes/spaces). The data-source definition and every reference must agree,
// so both go through this helper.
func doProjectDataSourceName(projectName string) string {
	return "pyx_" + tfName(projectName)
}

// RenderDOProjectDataSource emits the digitalocean_project data source that looks
// up the environment's DO project BY NAME (e.g. "pyxcloud-production"). Emit it
// ONCE per estate; resources reference data.digitalocean_project.<name>.id to be
// placed there. Empty projectName => "" (no data source; account-default, legacy).
func RenderDOProjectDataSource(projectName string) string {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "data \"digitalocean_project\" %q {\n", doProjectDataSourceName(projectName))
	fmt.Fprintf(&b, "  name = %q\n", projectName)
	b.WriteString("}\n")
	return b.String()
}
