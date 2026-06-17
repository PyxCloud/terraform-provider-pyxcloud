package catalog

import "testing"

func vmPlanWith(provider, csp string) VMPlan {
	return VMPlan{
		Provider:     provider,
		CSP:          csp,
		CSPRegion:    "eu-west-1",
		VMName:       "web",
		InstanceType: "t3.medium",
		Image:        "ami-123",
		Instances:    []VMInstancePlan{{Name: "web-1"}},
		NetworkName:  "prod",
		SubnetName:   "prod-a",
		UserData:     "#!/bin/bash\nset -e\necho \"hello $USER\"\n",
	}
}

func TestRenderVMAWSUserDataAndProfile(t *testing.T) {
	p := vmPlanWith(ProviderAWS, "aws")
	p.InstanceProfile = "web-instance-profile"
	hcl, err := RenderVMHCL(p)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"resource \"aws_instance\"",
		"user_data = base64encode(<<-PYXUSERDATA",
		"echo \"hello $USER\"", // heredoc preserves $ and quotes unescaped
		"PYXUSERDATA",
		"iam_instance_profile = \"web-instance-profile\"",
	} {
		if !contains(hcl, want) {
			t.Errorf("AWS VM HCL missing %q\n---\n%s", want, hcl)
		}
	}
}

func TestRenderVMNoUserDataOmitsBlock(t *testing.T) {
	p := vmPlanWith(ProviderAWS, "aws")
	p.UserData = ""
	hcl, err := RenderVMHCL(p)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if contains(hcl, "user_data") || contains(hcl, "iam_instance_profile") {
		t.Errorf("empty user_data/profile must omit the blocks:\n%s", hcl)
	}
}

func TestRenderVMDOUserData(t *testing.T) {
	p := vmPlanWith(ProviderDigitalOcean, "do")
	hcl, err := RenderVMHCL(p)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !contains(hcl, "user_data = <<-PYXUSERDATA") {
		t.Errorf("DO droplet missing user_data heredoc:\n%s", hcl)
	}
}

func TestRenderVMGCPStartupScript(t *testing.T) {
	p := vmPlanWith(ProviderGCP, "gcp")
	hcl, err := RenderVMHCL(p)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !contains(hcl, "startup-script =") {
		t.Errorf("GCP instance missing metadata startup-script:\n%s", hcl)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (indexOf(haystack, needle) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
