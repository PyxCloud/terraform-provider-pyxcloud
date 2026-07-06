package catalog

import (
	"fmt"
	"regexp"
	"sort"
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

// doProjectAssignableTypes are the DigitalOcean resource kinds that (a) belong to
// a PROJECT and (b) export a `.urn`, so they can be bound via
// digitalocean_project_resources. This is deliberately conservative: types that
// are account- or region-scoped (container_registry, vpc, firewall), sub-resources
// (spaces_bucket_policy), or that don't export a urn are OMITTED — referencing a
// missing attribute would fail the plan. droplet_autoscale is excluded on purpose:
// its MEMBERS are placed via droplet_template.project_id (phase 1), and the pool
// resource itself is not a project-assignable urn.
var doProjectAssignableTypes = map[string]bool{
	"digitalocean_droplet":            true,
	"digitalocean_kubernetes_cluster": true,
	"digitalocean_loadbalancer":       true,
	"digitalocean_database_cluster":   true,
	"digitalocean_reserved_ip":        true,
	"digitalocean_spaces_bucket":      true,
	"digitalocean_volume":             true,
	"digitalocean_domain":             true,
}

// doResourceHeaderRe matches a resource block header exactly as the renderers emit
// it — `resource "digitalocean_<type>" "<name>" {` — anchored at COLUMN 0. Top-level
// HCL resource blocks are always unindented, so this can't match a header that
// appears indented inside a heredoc/string body (e.g. a user_data script).
var doResourceHeaderRe = regexp.MustCompile(`(?m)^resource "(digitalocean_[a-z_]+)" "([A-Za-z0-9_-]+)" \{`)

// RenderDOProjectResources binds every project-assignable DigitalOcean resource in
// the assembled HCL to the environment's project, by URN, via a single
// digitalocean_project_resources block. This complements phase 1 (autoscale members
// via droplet_template.project_id): databases, load balancers, reserved IPs, spaces
// buckets, k8s clusters, volumes and domains would otherwise land in the account
// DEFAULT project. Empty projectName, or no assignable resources, => "" (no block).
func RenderDOProjectResources(projectName string, docs []string) string {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		return ""
	}
	seen := map[string]bool{}
	var refs []string
	for _, doc := range docs {
		for _, m := range doResourceHeaderRe.FindAllStringSubmatch(doc, -1) {
			typ, name := m[1], m[2]
			if !doProjectAssignableTypes[typ] {
				continue
			}
			ref := typ + "." + name + ".urn"
			if seen[ref] {
				continue
			}
			seen[ref] = true
			refs = append(refs, ref)
		}
	}
	if len(refs) == 0 {
		return ""
	}
	sort.Strings(refs) // deterministic output
	var b strings.Builder
	// Local name must be unique and stable; tie it to the project data source.
	fmt.Fprintf(&b, "resource \"digitalocean_project_resources\" %q {\n", doProjectDataSourceName(projectName))
	fmt.Fprintf(&b, "  project = data.digitalocean_project.%s.id\n", doProjectDataSourceName(projectName))
	b.WriteString("  resources = [\n")
	for _, ref := range refs {
		fmt.Fprintf(&b, "    %s,\n", ref)
	}
	b.WriteString("  ]\n}\n")
	return b.String()
}
