package catalog

import (
	"context"
	"fmt"
	"strings"
)

// Image resolution is the abstract OS-image -> concrete provider image kind
// (board task pd-MIG-AMI-DO-IMAGE, epic EPIC-AWS-TO-DO-MIGRATION). The catalog
// already resolves a concrete image string per (csp, csp_region, os, version,
// arch) via VMCatalog.ResolveImage — an AWS AMI id, a GCP image family, or a DO
// image SLUG. What was missing is a structured, provider-aware *reference* so the
// renderer knows HOW to emit it:
//
//   - AWS: an AMI is REGION-SPECIFIC and rotates. Pinning a literal `ami-xxxx`
//     in HCL is brittle and breaks the moment the topology moves region. The
//     robust, plan-valid form is an SSM PUBLIC PARAMETER lookup
//     (data.aws_ssm_parameter over /aws/service/...), resolved by AWS at plan
//     time to the current AMI for that region — the "AMI-via-SSM" path.
//   - DigitalOcean: an image is a STABLE, region-portable SLUG (e.g.
//     "ubuntu-24-04-x64") consumed verbatim by digitalocean_droplet.image. No
//     SSM/data lookup needed — the slug IS the reference.
//   - GCP: an image family/path consumed verbatim by the boot disk.
//
// This file turns the flat catalog string into an ImageRef carrying that kind,
// so the migration AWS(AMI/SSM) -> DO(slug) is an explicit, tested resolution
// step rather than an implicit "stuff the string into `ami =`".

// ImageKind classifies how a resolved image reference must be rendered.
type ImageKind string

const (
	// ImageKindAMILiteral is a concrete AWS AMI id (ami-...). Emitted verbatim as
	// `ami = "ami-..."`. Used when the catalog pins an explicit AMI for a region.
	ImageKindAMILiteral ImageKind = "ami-literal"
	// ImageKindSSMParameter is an AWS SSM public-parameter PATH (/aws/service/...).
	// Rendered as a data.aws_ssm_parameter lookup so AWS resolves the current,
	// region-correct AMI at plan time — the robust "AMI-via-SSM" form.
	ImageKindSSMParameter ImageKind = "ssm-parameter"
	// ImageKindDOSlug is a DigitalOcean image slug (e.g. ubuntu-24-04-x64),
	// consumed verbatim by digitalocean_droplet.image.
	ImageKindDOSlug ImageKind = "do-slug"
	// ImageKindGCPImage is a GCP image family / full path, consumed verbatim.
	ImageKindGCPImage ImageKind = "gcp-image"
	// ImageKindGeneric is any other provider's image string, emitted verbatim.
	ImageKindGeneric ImageKind = "generic"
)

// ImageRef is the structured, provider-aware resolution of an abstract OS image.
// It is what TranslateImage produces from the flat VMCatalog.ResolveImage string,
// and what the renderer consumes to emit the correct HCL form.
type ImageRef struct {
	Provider  string    `json:"provider"`   // aws | gcp | digitalocean
	CSP       string    `json:"csp"`        // catalog token: aws | gcp | do
	CSPRegion string    `json:"csp_region"` // concrete provider region
	OSName    string    `json:"os_name"`    // ubuntu | debian
	OSVersion string    `json:"os_version"` // resolved version
	Arch      string    `json:"arch"`       // x86_64 | arm64
	Kind      ImageKind `json:"kind"`       // how Ref must be rendered

	// Ref is the concrete reference value:
	//   - ami-literal:   the AMI id ("ami-0123...")
	//   - ssm-parameter: the SSM parameter PATH ("/aws/service/...")
	//   - do-slug:       the DO image slug ("ubuntu-24-04-x64")
	//   - gcp-image:     the GCP image family/path
	Ref string `json:"ref"`
}

// ssmParameterPrefix is the AWS Systems Manager public-parameter namespace. A
// catalog "image" value that starts with this is an SSM path, not a literal AMI.
const ssmParameterPrefix = "/aws/service/"

// TranslateImage resolves an abstract OS image for one provider into a structured
// ImageRef using the catalog. Deterministic and catalog-driven: it calls the same
// VMCatalog.ResolveImage the VM translation uses, then classifies the result into
// the provider-correct ImageKind. Any missing catalog data surfaces as the
// catalog's hard ErrOSImageNotFound (never a silent fallback), per SPEC §4.
//
// This is the explicit AMI-via-SSM <-> DO-slug resolution boundary the migration
// needs: the SAME abstract (os, version, arch) yields a data.aws_ssm_parameter
// lookup on AWS and a bare digitalocean_droplet.image slug on DO.
func TranslateImage(ctx context.Context, cat VMCatalog, spec VMSpec) (ImageRef, error) {
	if strings.TrimSpace(spec.Provider) == "" {
		return ImageRef{}, fmt.Errorf("image: provider is required (aws | gcp | digitalocean)")
	}
	if _, ok := ProviderToCSP(spec.Provider); !ok {
		return ImageRef{}, fmt.Errorf("image: unknown provider %q (aws | gcp | digitalocean)", spec.Provider)
	}

	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return ImageRef{}, err
	}

	arch := strings.ToLower(strings.TrimSpace(spec.Architecture))
	if arch == "" {
		arch = ArchX8664
	}
	osName := strings.ToLower(strings.TrimSpace(spec.OS))
	if osName == "" {
		osName = OSUbuntu
	}
	osVersion := strings.TrimSpace(spec.OSVersion)
	if osVersion == "" {
		osVersion = defaultOSVersions[osName]
	}

	img, err := cat.ResolveImage(ctx, row.CSP, row.CSPRegion, osName, osVersion, arch)
	if err != nil {
		return ImageRef{}, err
	}

	ref := ImageRef{
		Provider:  strings.ToLower(strings.TrimSpace(spec.Provider)),
		CSP:       row.CSP,
		CSPRegion: row.CSPRegion,
		OSName:    osName,
		OSVersion: osVersion,
		Arch:      arch,
		Ref:       img.Image,
	}
	ref.Kind = classifyImageKind(ref.Provider, img.Image)
	return ref, nil
}

// classifyImageKind decides how a resolved image string must be rendered, based
// on the provider and the shape of the value.
func classifyImageKind(provider, image string) ImageKind {
	image = strings.TrimSpace(image)
	switch provider {
	case ProviderAWS:
		// An SSM public-parameter path is the robust, region-portable AMI source.
		if strings.HasPrefix(image, ssmParameterPrefix) {
			return ImageKindSSMParameter
		}
		// A literal "ami-..." is a region-pinned id (still valid, less portable).
		return ImageKindAMILiteral
	case ProviderDigitalOcean:
		return ImageKindDOSlug
	case ProviderGCP:
		return ImageKindGCPImage
	default:
		return ImageKindGeneric
	}
}

// RenderImageRefHCL renders the image reference into the HCL fragment(s) for a
// provider, and returns the EXPRESSION a compute resource must assign to its
// image attribute.
//
//   - AWS ssm-parameter: emits a `data "aws_ssm_parameter" "<label>_ami"` block
//     and returns the expression `data.aws_ssm_parameter.<label>_ami.value` —
//     AWS resolves it to the current region AMI at plan time (AMI-via-SSM).
//   - AWS ami-literal:    no data block; returns the quoted AMI id.
//   - DigitalOcean:       no data block; returns the quoted DO slug verbatim.
//   - GCP/generic:        no data block; returns the quoted image verbatim.
//
// `label` is a tf-safe resource label (the caller passes tfName(name)). The
// boolean reports whether the returned dataBlock is non-empty (needs emitting).
func RenderImageRefHCL(ref ImageRef, label string) (dataBlock string, imageExpr string) {
	switch ref.Kind {
	case ImageKindSSMParameter:
		var b strings.Builder
		fmt.Fprintf(&b, "data \"aws_ssm_parameter\" %q {\n", label+"_ami")
		fmt.Fprintf(&b, "  name = %q\n", ref.Ref)
		b.WriteString("}\n")
		return b.String(), fmt.Sprintf("data.aws_ssm_parameter.%s_ami.value", label)
	case ImageKindAMILiteral, ImageKindDOSlug, ImageKindGCPImage, ImageKindGeneric:
		return "", fmt.Sprintf("%q", ref.Ref)
	default:
		return "", fmt.Sprintf("%q", ref.Ref)
	}
}
