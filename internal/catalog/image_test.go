package catalog

import (
	"context"
	"strings"
	"testing"
)

// TestTranslateImageDOSlug asserts the abstract OS image resolves to a DO image
// SLUG (consumed verbatim by digitalocean_droplet.image) — the migration target
// for the AWS AMI/SSM path.
func TestTranslateImageDOSlug(t *testing.T) {
	t.Parallel()
	ref, err := TranslateImage(context.Background(), MustEmbedded(), VMSpec{
		Region: "Frankfurt", Provider: "digitalocean", OS: "ubuntu", OSVersion: "24.04", Architecture: "x86_64",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref.Kind != ImageKindDOSlug {
		t.Errorf("kind = %q, want do-slug", ref.Kind)
	}
	if ref.CSPRegion != "fra1" {
		t.Errorf("csp_region = %q, want fra1", ref.CSPRegion)
	}
	if !strings.HasPrefix(ref.Ref, "ubuntu-24-04") {
		t.Errorf("DO image ref = %q, want an ubuntu-24-04 slug", ref.Ref)
	}
	// The DO slug is rendered verbatim, with NO data block.
	block, expr := RenderImageRefHCL(ref, "web")
	if block != "" {
		t.Errorf("DO slug must not emit a data block, got:\n%s", block)
	}
	if expr != `"`+ref.Ref+`"` {
		t.Errorf("DO image expr = %q, want the quoted slug", expr)
	}
}

// TestTranslateImageAWSLiteral asserts the AWS path resolves to a literal AMI id
// when the catalog pins one (the embedded snapshot uses ami-... ids).
func TestTranslateImageAWSLiteral(t *testing.T) {
	t.Parallel()
	// AWS OS images in the embedded snapshot live in eu-west-1 (Dublin) /
	// us-east-1; DO slugs live in fra1 (Frankfurt). Use the region each provider
	// actually has an image for.
	ref, err := TranslateImage(context.Background(), MustEmbedded(), VMSpec{
		Region: "Dublin", Provider: "aws", OS: "ubuntu", OSVersion: "24.04", Architecture: "x86_64",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref.Kind != ImageKindAMILiteral {
		t.Errorf("kind = %q, want ami-literal", ref.Kind)
	}
	if !strings.HasPrefix(ref.Ref, "ami-") {
		t.Errorf("AWS image ref = %q, want an ami- id", ref.Ref)
	}
	block, expr := RenderImageRefHCL(ref, "web")
	if block != "" {
		t.Errorf("literal AMI must not emit a data block, got:\n%s", block)
	}
	if expr != `"`+ref.Ref+`"` {
		t.Errorf("AWS image expr = %q, want the quoted AMI id", expr)
	}
}

// TestClassifyAndRenderSSMParameter asserts the AMI-via-SSM path: an AWS image
// value under /aws/service/ is classified as an SSM parameter and rendered as a
// data.aws_ssm_parameter lookup whose .value is the image expression — the robust,
// region-portable AMI source.
func TestClassifyAndRenderSSMParameter(t *testing.T) {
	t.Parallel()
	const ssm = "/aws/service/canonical/ubuntu/server/24.04/stable/current/amd64/hvm/ebs-gp3/ami-id"
	if got := classifyImageKind(ProviderAWS, ssm); got != ImageKindSSMParameter {
		t.Fatalf("classifyImageKind(aws, ssm-path) = %q, want ssm-parameter", got)
	}
	ref := ImageRef{Provider: ProviderAWS, Kind: ImageKindSSMParameter, Ref: ssm}
	block, expr := RenderImageRefHCL(ref, "web")
	for _, want := range []string{
		`data "aws_ssm_parameter" "web_ami"`,
		`name = "` + ssm + `"`,
	} {
		if !strings.Contains(block, want) {
			t.Errorf("SSM data block missing %q\n%s", want, block)
		}
	}
	if expr != "data.aws_ssm_parameter.web_ami.value" {
		t.Errorf("SSM image expr = %q, want the data lookup .value", expr)
	}
}

// TestTranslateImageMissingIsHardError asserts a missing catalog image surfaces
// as a hard plan-time error (never a silent fallback), per SPEC §4.
func TestTranslateImageMissingIsHardError(t *testing.T) {
	t.Parallel()
	_, err := TranslateImage(context.Background(), MustEmbedded(), VMSpec{
		Region: "Frankfurt", Provider: "digitalocean", OS: "ubuntu", OSVersion: "99.99", Architecture: "x86_64",
	})
	if err == nil {
		t.Fatal("expected a hard plan-time error for a missing OS image")
	}
}

func TestTranslateImageUnknownProvider(t *testing.T) {
	t.Parallel()
	_, err := TranslateImage(context.Background(), MustEmbedded(), VMSpec{
		Region: "Frankfurt", Provider: "nimbus", OS: "ubuntu",
	})
	if err == nil {
		t.Fatal("expected an error for an unknown provider")
	}
}
