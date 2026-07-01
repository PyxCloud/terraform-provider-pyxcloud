package catalog

import (
	"context"
	"fmt"
	"strings"
)

// static-site is the abstract `static-site` component (pd-MIG-CUTOVER-F1-01,
// BESPOKE-GAPS GAP-1). It models a managed static frontend app — a built SPA /
// marketing bundle served from a CDN behind a custom domain — the primitive AWS
// Amplify provides and which DigitalOcean has no first-class equivalent for.
//
// It is a COMPOSITE (macro) component: rather than inventing raw resources, it
// descends to the EXISTING catalog components that already have per-provider
// renderers:
//
//   - AWS: aws_amplify_app + aws_amplify_branch — Amplify's build-and-host-a-SPA
//     primitive (the source estate runs the 3 frontends on Amplify today). The
//     origin bucket is NOT emitted on AWS (Amplify owns the hosting + CDN).
//
//   - DigitalOcean: there is no Amplify. The component descends to
//       1. a PUBLIC digitalocean_spaces_bucket with STATIC WEBSITE hosting
//          (index/error documents) — rendered by the object-storage renderer
//          (RenderObjectStorageHCL) with a Website config, and
//       2. a Cloudflare CDN front (proxied CNAME -> the Spaces website origin +
//          zone cache settings + TLS) — rendered by the cloudflare-cdn renderer
//          (RenderCloudfareCDNHCL).
//     Together: Spaces static hosting + Cloudflare CDN = the DO answer to Amplify.
//
// SECURITY NOTE: a static-site's origin bucket is PUBLIC-read by design (it
// serves a public website), which is the ONE legitimate public-object-storage
// case. Public is set explicitly on the composed object-storage spec (opt-in, as
// the object-storage security invariant requires) — it is never a silent default
// on the raw object-storage component.

// TypeStaticSite is the canonical static-site component type token.
const TypeStaticSite = "static-site"

// StaticSiteSpec is the abstract description of a managed static frontend app.
// Provider-neutral.
type StaticSiteSpec struct {
	Name     string // component name, e.g. "console" / "marketing" / "vibe"
	Region   string // abstract pyx region_name (for the Spaces bucket placement on DO)
	Provider string // aws | digitalocean | ...

	// CustomDomain is the public hostname the site is served at, e.g.
	// "app.passo.build". On DO it becomes the Cloudflare proxied CNAME host; on AWS
	// it is recorded as the Amplify branch's associated domain (informational — the
	// aws_amplify_domain_association is supplied out of band).
	CustomDomain string

	// BuildOutputDir is the built-bundle output directory (Amplify build artifact
	// dir, e.g. "dist" / "build"). Provider-neutral; on DO it is the prefix the CDN
	// origin points at (informational) and the Spaces upload root.
	BuildOutputDir string

	// IndexDocument / ErrorDocument are the static-website entry + fallback docs.
	// Defaults: index.html / index.html (SPA client-side routing fallback).
	IndexDocument string
	ErrorDocument string

	// CloudflareZoneID is the Cloudflare zone the DO CDN record lives in. Empty ->
	// var.cloudflare_zone_id (supplied out of band, exactly like the dns/cdn
	// components). Ignored on AWS (Amplify owns the CDN).
	CloudflareZoneID string
}

// StaticSitePlan is the deterministic, catalog-resolved translation of a
// StaticSiteSpec. It is a COMPOSITE plan: on DO it carries the composed
// object-storage (Spaces static website) plan and the Cloudflare CDN plan; on AWS
// it carries the Amplify app parameters.
type StaticSitePlan struct {
	Provider   string `json:"provider"`
	CSP        string `json:"csp"`
	RegionName string `json:"region_name"`
	CSPRegion  string `json:"csp_region"`

	Name           string `json:"name"`
	CustomDomain   string `json:"custom_domain,omitempty"`
	BuildOutputDir string `json:"build_output_dir,omitempty"`
	IndexDocument  string `json:"index_document"`
	ErrorDocument  string `json:"error_document"`

	// ── DigitalOcean composite (Spaces static website + Cloudflare CDN) ──
	// ObjectStorage is the composed object-storage plan (a PUBLIC Spaces bucket with
	// a Website config). Nil on AWS.
	ObjectStorage *ObjectStoragePlan `json:"object_storage,omitempty"`
	// CloudflareCDN is the composed Cloudflare CDN plan (proxied CNAME + zone
	// settings) fronting the Spaces website origin. Nil on AWS.
	CloudflareCDN *CloudflareCDNPlan `json:"cloudflare_cdn,omitempty"`

	// UsesCloudflare reports that the render pins the cloudflare/cloudflare provider
	// (true on DO). Mirrors the CDN component's flag so AssembleHCL can pin the block.
	UsesCloudflare bool `json:"uses_cloudflare"`

	ResourceType string `json:"resource_type"` // top provider resource, e.g. aws_amplify_app / digitalocean_spaces_bucket
}

const (
	defaultStaticIndexDoc = "index.html"
	// SPA fallback: serving index.html for 404s lets client-side routers own the path.
	defaultStaticErrorDoc = "index.html"
)

// TranslateStaticSite resolves a StaticSiteSpec into a concrete StaticSitePlan.
// On DigitalOcean it composes the object-storage (public Spaces static website)
// and cloudflare-cdn plans via their existing Translate* functions — no raw
// resources are invented. On AWS it resolves the Amplify app parameters.
func TranslateStaticSite(ctx context.Context, cat RegionCatalog, spec StaticSiteSpec) (StaticSitePlan, error) {
	if err := validateStaticSiteSpec(spec); err != nil {
		return StaticSitePlan{}, err
	}
	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return StaticSitePlan{}, err
	}
	provider := lc(spec.Provider)

	index := strings.TrimSpace(spec.IndexDocument)
	if index == "" {
		index = defaultStaticIndexDoc
	}
	errDoc := strings.TrimSpace(spec.ErrorDocument)
	if errDoc == "" {
		errDoc = defaultStaticErrorDoc
	}

	plan := StaticSitePlan{
		Provider:       provider,
		CSP:            row.CSP,
		RegionName:     row.RegionName,
		CSPRegion:      row.CSPRegion,
		Name:           canonicalName(spec.Name, "pyxcloud-site"),
		CustomDomain:   strings.TrimSpace(spec.CustomDomain),
		BuildOutputDir: strings.TrimSpace(spec.BuildOutputDir),
		IndexDocument:  index,
		ErrorDocument:  errDoc,
	}

	switch provider {
	case ProviderAWS:
		// Amplify: the source estate's managed build-and-host primitive.
		plan.ResourceType = "aws_amplify_app"
		return plan, nil

	case ProviderDigitalOcean:
		// Compose the object-storage (public Spaces static website) plan via the
		// existing translator — reuse, not reinvention. Public is opt-in HERE (a
		// static site serves a public website) with a Website config carrying the
		// index/error docs.
		osPlan, err := TranslateObjectStorage(ctx, cat, ObjectStorageSpec{
			Name:     plan.Name,
			Region:   spec.Region,
			Provider: spec.Provider,
			Public:   true, // static website is public-read (the one legitimate public case)
			Website:  &WebsiteConfig{IndexDocument: index, ErrorDocument: errDoc},
		})
		if err != nil {
			return StaticSitePlan{}, fmt.Errorf("static-site: composing object-storage origin: %w", err)
		}
		plan.ObjectStorage = &osPlan

		// The Cloudflare CDN fronts the Spaces website endpoint. The origin host is
		// the Spaces static-website endpoint derived deterministically from the
		// bucket name + region (<bucket>.<region>.digitaloceanspaces.com). Compose via
		// the existing cloudflare-cdn translator.
		origin := spacesWebsiteEndpoint(osPlan.BucketName, osPlan.CSPRegion)
		cdnPlan, err := TranslateCloudfareCDN(ctx, CloudflareCDNSpec{
			Name:       plan.Name,
			ZoneID:     spec.CloudflareZoneID,
			Host:       cdnHostFromDomain(spec.CustomDomain, plan.Name),
			OriginHost: origin,
		})
		if err != nil {
			return StaticSitePlan{}, fmt.Errorf("static-site: composing cloudflare CDN front: %w", err)
		}
		plan.CloudflareCDN = &cdnPlan
		plan.UsesCloudflare = true
		plan.ResourceType = "digitalocean_spaces_bucket"
		return plan, nil

	default:
		return StaticSitePlan{}, ErrComponentUnsupported{
			Component: TypeStaticSite, Provider: provider, CSP: row.CSP, CSPRegion: row.CSPRegion,
			Alternative: "static-site is supported on aws (Amplify) and digitalocean (Spaces static " +
				"website + Cloudflare CDN); host the frontend on one of those, or serve the built " +
				"bundle from an object-storage bucket fronted by a cdn-service component",
		}
	}
}

// spacesWebsiteEndpoint returns the deterministic DO Spaces static-website
// endpoint hostname for a bucket in a region. DO Spaces serves static sites at
// <bucket>.<region>.digitaloceanspaces.com (the CDN edge endpoint uses the same
// origin host, proxied through Cloudflare).
func spacesWebsiteEndpoint(bucket, cspRegion string) string {
	return bucket + "." + cspRegion + ".digitaloceanspaces.com"
}

// cdnHostFromDomain derives the Cloudflare record host (subdomain) from the
// custom domain. If a full domain is given (e.g. "app.passo.build") its leftmost
// label is the host; otherwise the component name is used.
func cdnHostFromDomain(domain, name string) string {
	d := strings.TrimSpace(domain)
	if d == "" {
		return name
	}
	if i := strings.IndexByte(d, '.'); i > 0 {
		return d[:i]
	}
	return d
}

func validateStaticSiteSpec(spec StaticSiteSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("static-site: region (abstract pyx region_name) is required")
	}
	if _, ok := ProviderToCSP(spec.Provider); !ok {
		return fmt.Errorf("static-site: unknown provider %q", spec.Provider)
	}
	return nil
}

// CanonicalStaticSiteType reports whether t names the static-site component
// (accepts a few natural aliases).
func CanonicalStaticSiteType(t string) (string, bool) {
	switch lc(t) {
	case TypeStaticSite, "static-website", "static-hosting", "frontend-app", "spa":
		return TypeStaticSite, true
	}
	return "", false
}

// RenderStaticSiteHCL renders a StaticSitePlan into concrete provider HCL. On AWS
// it emits the Amplify app + branch; on DO it emits the composed object-storage
// (public Spaces static website) + Cloudflare CDN documents by delegating to the
// existing renderers (RenderObjectStorageHCL + RenderCloudfareCDNHCL).
func RenderStaticSiteHCL(p StaticSitePlan) (string, error) {
	switch p.Provider {
	case ProviderAWS:
		return renderStaticSiteAmplify(p), nil
	case ProviderDigitalOcean:
		return renderStaticSiteDO(p)
	default:
		return "", fmt.Errorf("static-site: render unsupported for provider %q", p.Provider)
	}
}

func renderStaticSiteAmplify(p StaticSitePlan) string {
	label := tfName(p.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "# pyxcloud static-site: AWS Amplify managed static hosting (%s)\n", p.Name)
	fmt.Fprintf(&b, "resource \"aws_amplify_app\" %q {\n", label)
	fmt.Fprintf(&b, "  name = %q\n", p.Name)
	// The source repo/build settings are supplied out of band (Amplify connects a
	// git provider); the abstract topology carries the build output dir so the
	// Amplify buildSpec artifacts baseDirectory is explicit and idempotent.
	outDir := p.BuildOutputDir
	if outDir == "" {
		outDir = "dist"
	}
	b.WriteString("  build_spec = <<-BUILDSPEC\n")
	b.WriteString("    version: 1\n")
	b.WriteString("    frontend:\n")
	b.WriteString("      phases:\n")
	b.WriteString("        build:\n")
	b.WriteString("          commands: []\n")
	b.WriteString("      artifacts:\n")
	fmt.Fprintf(&b, "        baseDirectory: %s\n", outDir)
	b.WriteString("        files:\n")
	b.WriteString("          - '**/*'\n")
	b.WriteString("  BUILDSPEC\n")
	// SPA rewrite: serve the index document for client-side-routed 404s.
	b.WriteString("  custom_rule {\n")
	b.WriteString("    source = \"</^[^.]+$|\\\\.(?!(css|gif|ico|jpg|js|png|txt|svg|woff|woff2|ttf|map|json)$)([^.]+$)/>\"\n")
	fmt.Fprintf(&b, "    target = %q\n", "/"+p.IndexDocument)
	b.WriteString("    status = \"200\"\n")
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", p.Name)
	b.WriteString("}\n\n")

	fmt.Fprintf(&b, "resource \"aws_amplify_branch\" %q {\n", label)
	fmt.Fprintf(&b, "  app_id      = aws_amplify_app.%s.id\n", label)
	b.WriteString("  branch_name = \"main\"\n")
	b.WriteString("  stage       = \"PRODUCTION\"\n")
	b.WriteString("}\n")
	if p.CustomDomain != "" {
		fmt.Fprintf(&b, "\n# NOTE: custom domain %q is associated via aws_amplify_domain_association "+
			"(supplied out of band — the zone/cert wiring is not part of the abstract topology).\n", p.CustomDomain)
	}
	return b.String()
}

func renderStaticSiteDO(p StaticSitePlan) (string, error) {
	if p.ObjectStorage == nil || p.CloudflareCDN == nil {
		return "", fmt.Errorf("static-site: DO plan missing composed object-storage / cloudflare-cdn plan")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# pyxcloud static-site: DigitalOcean Spaces static website + Cloudflare CDN (%s)\n", p.Name)

	// 1. Spaces static-website origin — via the existing object-storage renderer.
	osHCL, err := RenderObjectStorageHCL(*p.ObjectStorage)
	if err != nil {
		return "", fmt.Errorf("static-site: rendering Spaces origin: %w", err)
	}
	b.WriteString(osHCL)
	if !strings.HasSuffix(osHCL, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// 2. Cloudflare CDN front (proxied CNAME + zone cache/TLS settings) — via the
	//    existing cloudflare-cdn renderer.
	cdnHCL, err := RenderCloudfareCDNHCL(*p.CloudflareCDN)
	if err != nil {
		return "", fmt.Errorf("static-site: rendering Cloudflare CDN front: %w", err)
	}
	b.WriteString(cdnHCL)
	return b.String(), nil
}
