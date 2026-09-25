package catalog

import (
	"strings"
	"testing"
)

// DEP-01.4 golden test: user_data is user-derived and must land in the
// rendered .tf literally. An attacker-controlled payload must not be able to
// (a) open an HCL template interpolation or directive inside the heredoc, or
// (b) close the heredoc early with a line matching the heredoc delimiter.
func TestVMHeredocEscapesUserDerivedUserData(t *testing.T) {
	t.Parallel()

	payload := strings.Join([]string{
		"#!/bin/sh",
		"echo ${malicious_injection} > /tmp/x",
		"echo %{ if stolen_secrets } secret %{ endif } >> /tmp/y",
		"PYXUSERDATA",
		" PYXUSERDATA_", // also collides with the grown delimiter: must grow again
		"echo owned",    // must stay inside user_data, never top-level HCL
	}, "\n")

	out := vmHeredoc(payload)

	if !strings.Contains(out, "$${malicious_injection}") {
		t.Errorf("dollar-brace not escaped in output:\n%s", out)
	}
	if !strings.Contains(out, "%%{ if stolen_secrets }") {
		t.Errorf("percent-brace not escaped in output:\n%s", out)
	}
	// The delimiter must have been grown until no payload line matches it.
	if !strings.HasPrefix(out, "<<-PYXUSERDATA__\n") {
		t.Errorf("delimiter not grown past collisions, want <<-PYXUSERDATA__:\n%s", out)
	}
	if !strings.HasSuffix(out, "PYXUSERDATA__\n  ") {
		t.Errorf("closing delimiter wrong:\n%s", out)
	}
	// Content after the injected lines must remain inside the heredoc payload.
	if !strings.Contains(out, "\necho owned\n") {
		t.Errorf("trailing payload line lost:\n%s", out)
	}
}

// No collision: the canonical delimiter PYXUSERDATA must be used unchanged.
func TestVMHeredocNoCollisionUsesCanonicalDelimiter(t *testing.T) {
	t.Parallel()

	out := vmHeredoc("#!/bin/sh\necho ${var.secrets}\n")
	if !strings.HasPrefix(out, "<<-PYXUSERDATA\n") || !strings.HasSuffix(out, "PYXUSERDATA\n  ") {
		t.Errorf("canonical delimiter changed without collision:\n%s", out)
	}
	if !strings.Contains(out, "$${var.secrets}") {
		t.Errorf("dollar-brace not escaped in output:\n%s", out)
	}
}

// End-to-end through RenderVMHCL: the AWS path wraps the heredoc in
// base64encode, but escaping must already happen inside the heredoc itself.
func TestRenderVMHCLUserDataInjectionStaysLiteral(t *testing.T) {
	t.Parallel()

	out, err := RenderVMHCL(VMPlan{
		Provider:      ProviderAWS,
		VMName:        "web",
		InstanceType:  "t3.medium",
		Image:         "ami-00000000000000000",
		CSPRegion:     "eu-west-1",
		SubnetName:    "production-subnet-1",
		NetworkName:   "production",
		SecurityGroup: "production-web",
		UserData:      "#!/bin/sh\necho ${var.injected}\nPYXUSERDATA\n",
		Instances: []VMInstancePlan{{
			Name: "web-1",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "$${var.injected}") {
		t.Errorf("rendered VM user_data is not escaped:\n%s", out)
	}
	if !strings.Contains(out, "<<-PYXUSERDATA_\n") {
		t.Errorf("heredoc delimiter not grown for injected terminator line:\n%s", out)
	}
}
