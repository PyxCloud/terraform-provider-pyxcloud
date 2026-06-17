package catalog

import (
	"context"
	"fmt"
	"strings"
)

// BlockStorage is the abstract `block-storage` component: a persistent disk
// attached to a VM — the canonical form of the per-provider scripts'
// aws_ebs_volume + aws_volume_attachment glue.
//
//   - AWS: aws_ebs_volume (in the target instance's AZ) + aws_volume_attachment.
//   - GCP: google_compute_disk + google_compute_attached_disk.
//   - DigitalOcean: digitalocean_volume + digitalocean_volume_attachment.
//
// It attaches to a VM component in the same environment (TargetVM); the disk is
// placed in that VM's availability zone.

// BlockStorageSpec is the abstract persistent-disk description.
type BlockStorageSpec struct {
	Name       string
	Region     string
	Provider   string
	SizeGB     int
	VolumeType string // aws: gp3 (default) | io2 | ...; gcp: pd-ssd | pd-balanced; do: (ignored)
	DeviceName string // attach device, e.g. /dev/sdf (AWS); defaults provided
	TargetVM   string // the VM component this disk attaches to (required)
}

// BlockStoragePlan is the resolved concrete plan.
type BlockStoragePlan struct {
	Provider     string `json:"provider"`
	CSP          string `json:"csp"`
	RegionName   string `json:"region_name"`
	CSPRegion    string `json:"csp_region"`
	Name         string `json:"name"`
	SizeGB       int    `json:"size_gb"`
	VolumeType   string `json:"volume_type"`
	DeviceName   string `json:"device_name"`
	TargetVM     string `json:"target_vm"`
	ResourceType string `json:"resource_type"`
}

// TranslateBlockStorage resolves a BlockStorageSpec.
func TranslateBlockStorage(ctx context.Context, cat RegionCatalog, spec BlockStorageSpec) (BlockStoragePlan, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return BlockStoragePlan{}, fmt.Errorf("block-storage: name is required")
	}
	if spec.SizeGB <= 0 {
		return BlockStoragePlan{}, fmt.Errorf("block-storage %q: size_gb must be > 0", spec.Name)
	}
	if strings.TrimSpace(spec.TargetVM) == "" {
		return BlockStoragePlan{}, fmt.Errorf("block-storage %q: target_vm (the VM to attach to) is required", spec.Name)
	}
	csp, ok := ProviderToCSP(spec.Provider)
	if !ok {
		return BlockStoragePlan{}, fmt.Errorf("block-storage: unknown provider %q", spec.Provider)
	}
	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return BlockStoragePlan{}, err
	}
	plan := BlockStoragePlan{
		Provider: strings.ToLower(spec.Provider), CSP: csp,
		RegionName: row.RegionName, CSPRegion: row.CSPRegion,
		Name: spec.Name, SizeGB: spec.SizeGB, VolumeType: spec.VolumeType,
		DeviceName: spec.DeviceName, TargetVM: spec.TargetVM,
	}
	switch plan.Provider {
	case ProviderAWS:
		if plan.VolumeType == "" {
			plan.VolumeType = "gp3"
		}
		if plan.DeviceName == "" {
			plan.DeviceName = "/dev/sdf"
		}
		plan.ResourceType = "aws_ebs_volume"
	case ProviderGCP:
		if plan.VolumeType == "" {
			plan.VolumeType = "pd-balanced"
		}
		plan.ResourceType = "google_compute_disk"
	case ProviderDigitalOcean:
		plan.ResourceType = "digitalocean_volume"
	default:
		return BlockStoragePlan{}, fmt.Errorf("block-storage: unsupported provider %q", spec.Provider)
	}
	return plan, nil
}

// RenderBlockStorageHCL renders a BlockStoragePlan, attached to its target VM.
func RenderBlockStorageHCL(p BlockStoragePlan) (string, error) {
	vm := tfName(p.TargetVM + "-1") // first instance of the target VM component
	name := tfName(p.Name)
	var b strings.Builder
	switch p.Provider {
	case ProviderAWS:
		fmt.Fprintf(&b, "resource \"aws_ebs_volume\" %q {\n", name)
		fmt.Fprintf(&b, "  availability_zone = aws_instance.%s.availability_zone\n", vm)
		fmt.Fprintf(&b, "  size              = %d\n", p.SizeGB)
		fmt.Fprintf(&b, "  type              = %q\n", p.VolumeType)
		fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
		b.WriteString("}\n\n")
		fmt.Fprintf(&b, "resource \"aws_volume_attachment\" %q {\n", name)
		fmt.Fprintf(&b, "  device_name = %q\n", p.DeviceName)
		fmt.Fprintf(&b, "  volume_id   = aws_ebs_volume.%s.id\n", name)
		fmt.Fprintf(&b, "  instance_id = aws_instance.%s.id\n", vm)
		b.WriteString("}\n")
		return b.String(), nil
	case ProviderGCP:
		fmt.Fprintf(&b, "resource \"google_compute_disk\" %q {\n", name)
		fmt.Fprintf(&b, "  name = %q\n", p.Name)
		fmt.Fprintf(&b, "  size = %d\n", p.SizeGB)
		fmt.Fprintf(&b, "  type = %q\n", p.VolumeType)
		fmt.Fprintf(&b, "  zone = google_compute_instance.%s.zone\n", vm)
		b.WriteString("}\n\n")
		fmt.Fprintf(&b, "resource \"google_compute_attached_disk\" %q {\n", name)
		fmt.Fprintf(&b, "  disk     = google_compute_disk.%s.id\n", name)
		fmt.Fprintf(&b, "  instance = google_compute_instance.%s.id\n", vm)
		b.WriteString("}\n")
		return b.String(), nil
	case ProviderDigitalOcean:
		fmt.Fprintf(&b, "resource \"digitalocean_volume\" %q {\n", name)
		fmt.Fprintf(&b, "  name   = %q\n", p.Name)
		fmt.Fprintf(&b, "  region = %q\n", p.CSPRegion)
		fmt.Fprintf(&b, "  size   = %d\n", p.SizeGB)
		b.WriteString("}\n\n")
		fmt.Fprintf(&b, "resource \"digitalocean_volume_attachment\" %q {\n", name)
		fmt.Fprintf(&b, "  droplet_id = digitalocean_droplet.%s.id\n", vm)
		fmt.Fprintf(&b, "  volume_id  = digitalocean_volume.%s.id\n", name)
		b.WriteString("}\n")
		return b.String(), nil
	default:
		return "", fmt.Errorf("block-storage: render unsupported for provider %q", p.Provider)
	}
}
