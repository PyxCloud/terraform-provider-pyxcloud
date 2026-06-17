package catalog

import "testing"

func TestTranslateBlockStorageAWS(t *testing.T) {
	plan, err := TranslateBlockStorage(BlockStorageSpec{
		Name:     "mesh",
		Provider: "aws",
		Volumes: []BlockVolumeSpec{
			{Name: "data", SizeGiB: 50, Encrypted: true, AZ: "eu-west-1a", AttachTo: "mcp-1", DeviceName: "/dev/sdf"},
		},
	})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if plan.Volumes[0].Type != "gp3" {
		t.Errorf("default type = %q want gp3", plan.Volumes[0].Type)
	}
	hcl, err := RenderBlockStorageHCL(plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"resource \"aws_ebs_volume\"",
		"availability_zone = \"eu-west-1a\"",
		"size              = 50",
		"encrypted         = true",
		"resource \"aws_volume_attachment\"",
		"device_name = \"/dev/sdf\"",
		"instance_id = aws_instance.mcp-1.id",
	} {
		if !contains(hcl, want) {
			t.Errorf("block-storage HCL missing %q\n---\n%s", want, hcl)
		}
	}
}

func TestTranslateBlockStorageValidation(t *testing.T) {
	if _, err := TranslateBlockStorage(BlockStorageSpec{Provider: "aws", Volumes: []BlockVolumeSpec{{Name: "v", SizeGiB: 1, AZ: "z"}}}); err == nil {
		t.Error("expected error: name required")
	}
	if _, err := TranslateBlockStorage(BlockStorageSpec{Name: "n", Provider: "aws"}); err == nil {
		t.Error("expected error: need a volume")
	}
	if _, err := TranslateBlockStorage(BlockStorageSpec{Name: "n", Provider: "aws", Volumes: []BlockVolumeSpec{{Name: "v", SizeGiB: 10}}}); err == nil {
		t.Error("expected error: AWS volume needs AZ")
	}
	if _, err := TranslateBlockStorage(BlockStorageSpec{Name: "n", Provider: "aws", Volumes: []BlockVolumeSpec{{Name: "v", SizeGiB: 10, AZ: "z", AttachTo: "i"}}}); err == nil {
		t.Error("expected error: attach needs device_name")
	}
	if _, err := TranslateBlockStorage(BlockStorageSpec{Name: "n", Provider: "digitalocean", Volumes: []BlockVolumeSpec{{Name: "v", SizeGiB: 10}}}); err == nil {
		t.Error("expected error: unsupported provider")
	}
}
