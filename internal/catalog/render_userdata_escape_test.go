package catalog

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"
)

// DEP-01.4 (post-execution-v1 spec, Appendix D.3): user-derived user_data is
// baked into an UNQUOTED HCL heredoc by vmHeredoc. Before this task the body
// was emitted verbatim, so an injection payload like `${var.token}` interpolated
// Terraform variables into the droplet bootstrap, and a body line equal to the
// heredoc terminator spliced attacker-controlled HCL after the heredoc.
//
// The tests below pin the escape-by-default contract: `${`→`$${`, `%{`→`%%{`,
// terminator collision → unique terminator; clean values untouched; engine
// authored bootstraps keep their deliberate ${var.}/${data.} references.

func TestVMHeredocEscapesUserDerivedUserData(t *testing.T) {
	const base = "PYXUSERDATA"
	cases := []struct {
		name    string
		in      string
		want    []string // substrings that MUST appear in the rendered heredoc body
		notWant []string // substrings that must NOT appear
	}{
		{
			name: "dollar-brace variable reference renders literally",
			in:   "echo ${var.leak}\n",
			want: []string{"echo $${var.leak}"},
			notWant: []string{
				"echo ${var.leak}", // unescaped reference would interpolate
			},
		},
		{
			name: "percent-brace directive renders literally",
			in:   "cfg %{if leak}end%{endif}\n",
			want: []string{"cfg %%{if leak}end%%{endif}"},
			notWant: []string{
				"cfg %{if leak}", // unescaped directive is a template error at validate
			},
		},
		{
			name:    "clean value is unchanged",
			in:      "#!/bin/sh\necho hello\napt-get install -y nginx\n",
			want:    []string{"#!/bin/sh\necho hello\napt-get install -y nginx\n"},
			notWant: []string{"$$", "%%"},
		},
		{
			name: "already-escaped sequences stay idempotent",
			in:   "echo $${kept} and %%{kept}\n",
			want: []string{"echo $${kept} and %%{kept}"},
		},
		{
			name: "terminator line switches to a unique token",
			in:   "#!/bin/sh\nPYXUSERDATA\necho done\n",
			want: []string{"#!/bin/sh\nPYXUSERDATA\necho done\n"},
		},
		{
			name: "indented terminator line also switches the token",
			in:   "printf 'x'\n   PYXUSERDATA\necho done\n",
			want: []string{"printf 'x'\n   PYXUSERDATA\necho done\n"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := vmHeredoc(tc.in)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("heredoc missing %q\n---\n%s", w, got)
				}
			}
			for _, nw := range tc.notWant {
				if strings.Contains(got, nw) {
					t.Errorf("heredoc still contains %q (not escaped)\n---\n%s", nw, got)
				}
			}
		})
	}

	// Terminator-collision specifics: the token MUST differ from the base token
	// and the body must still be closed by the SAME chosen token.
	t.Run("colliding body yields unique terminator that closes the heredoc", func(t *testing.T) {
		body := "#!/bin/sh\n" + base + "\necho done\n"
		got := vmHeredoc(body)
		if !strings.HasPrefix(got, "<<-"+base+"\n") {
			// good: token switched
		} else {
			t.Fatalf("colliding body kept base terminator %q:\n%s", base, got)
		}
		// opener token: "<<-TOKEN"; closer: TOKEN immediately before the trailing "\n  "
		open := got[:strings.Index(got, "\n")]
		token := strings.TrimPrefix(open, "<<-")
		if token == base {
			t.Fatalf("token not made unique: %q", token)
		}
		if !strings.HasSuffix(got, token+"\n  ") {
			t.Errorf("heredoc not closed by its unique token %q:\n%s", token, got)
		}
		if !strings.Contains(got, "\n"+base+"\n") {
			t.Errorf("colliding body line not preserved literally:\n%s", got)
		}
	})
}

// TestUserDerivedUserDataInjectionValidates renders a full DO environment whose
// VM carries an injection payload as user_data and runs `terraform init &&
// terraform validate` on it. The payload must survive as LITERAL text: validate
// would reject any leaked template directive (%{if}) and any interpolation of an
// undeclared variable (${var.leak}).
//
// Skipped automatically when no terraform binary is on PATH (same pattern as
// TestFullEstateTerraformValidateDO). Set PYX_TF_VALIDATE=0 to force-skip.
func TestUserDerivedUserDataInjectionValidates(t *testing.T) {
	if os.Getenv("PYX_TF_VALIDATE") == "0" {
		t.Skip("PYX_TF_VALIDATE=0: terraform validate explicitly disabled")
	}
	execPath, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("terraform binary not on PATH: string round-trips prove the escaping; install terraform to run init/validate")
	}

	payload := "#!/bin/sh\n" +
		"echo ${var.leak}\n" +
		"echo %{if leak}injected%{endif}\n" +
		"PYXUSERDATA\n" +
		"echo after-terminator\n"

	docs, err := AssembleHCL(context.Background(), MustEmbedded(),
		AssembleInput{
			Name:     "injection",
			Provider: "digitalocean",
			Region:   "Frankfurt",
			Components: []AssembleComponent{
				{Name: "app", Type: "virtual-machine", Count: 1,
					VM: &AssembleVM{Architecture: "x86_64", CPU: "2", RAM: "4", OS: "ubuntu", UserData: payload}},
			},
		})
	if err != nil {
		t.Fatalf("AssembleHCL: %v", err)
	}

	all := ""
	for _, d := range docs {
		all += d + "\n"
	}
	if strings.Contains(all, "echo ${var.leak}") {
		t.Fatalf("rendered HCL still contains the unescaped ${var.leak} payload:\n%s", all)
	}
	if !strings.Contains(all, "echo $${var.leak}") || !strings.Contains(all, "echo %%{if leak}") {
		t.Fatalf("rendered HCL did not escape the payload literally:\n%s", all)
	}

	dir := t.TempDir()
	for i, d := range docs {
		name := filepath.Join(dir, fmt.Sprintf("pyx_%03d.tf", i))
		if werr := os.WriteFile(name, []byte(d), 0o644); werr != nil {
			t.Fatalf("write doc %d: %v", i, werr)
		}
	}
	tf, err := tfexec.NewTerraform(dir, execPath)
	if err != nil {
		t.Fatalf("tfexec: %v", err)
	}
	ctx := context.Background()
	if err := tf.Init(ctx, tfexec.Upgrade(false)); err != nil {
		t.Fatalf("terraform init failed: %v", err)
	}
	vout, verr := tf.Validate(ctx)
	if verr != nil {
		t.Fatalf("terraform validate failed — the injection payload did not render as literal HCL: %v", verr)
	}
	if !vout.Valid {
		t.Fatalf("terraform validate reported INVALID: %d diagnostics", vout.ErrorCount)
	}
	t.Log("terraform init && validate: GREEN — injection payload renders as literal user_data")
}

// TestEngineHeredocPreservesTerraformReferences pins the engine seam: the
// canonical platform bootstraps deliberately interpolate ${var.<x>} (secret
// var-model) and ${data.<x>...} (Vault KV data sources). Those references must
// survive, while every other bare `${` is escaped.
func TestEngineHeredocPreservesTerraformReferences(t *testing.T) {
	in := "TOKEN=${var.do_token}\n" +
		"URL=${data.vault_kv_secret_v2.sso.data[\"url\"]}\n" +
		"HOST=${endpoint%%:*}\n" +
		"ALREADY=$${kept}\n" +
		"DIRECTIVE=%{if leak}\n"
	got := engineHeredoc(in)
	for _, want := range []string{
		"TOKEN=${var.do_token}",
		"URL=${data.vault_kv_secret_v2.sso.data[\"url\"]}",
		"HOST=$${endpoint%%:*}",
		"ALREADY=$${kept}",
		"DIRECTIVE=%%{if leak}",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("engineHeredoc missing %q\n---\n%s", want, got)
		}
	}
}

// TestScaleGroupEngineAuthoredFlagWiring pins the propagation path:
// AssembleScaleGroup.EngineAuthoredUserData -> ScaleGroupSpec -> ScaleGroupPlan
// -> scaleGroupHeredoc. An engine-authored DO scale-group keeps ${var.
// references; the same content WITHOUT the flag is escaped (fail-closed).
func TestScaleGroupEngineAuthoredFlagWiring(t *testing.T) {
	const body = "TOKEN=${var.pyx_token}\n"
	engine := renderScaleGroupDO(ScaleGroupPlan{
		Provider: "digitalocean", RegionName: "fra1", CSPRegion: "fra1",
		GroupName: "svc", InstanceType: "s-1vcpu-1gb", Image: "ubuntu-24-04-x64",
		Min: 1, Max: 1, Desired: 1,
		UserData: body, EngineAuthoredUserData: true,
	})
	if !strings.Contains(engine, "user_data          = <<-PYXUSERDATA\nTOKEN=${var.pyx_token}") {
		t.Errorf("engine-authored DO scale-group lost its ${var.} reference:\n%s", engine)
	}
	user := renderScaleGroupDO(ScaleGroupPlan{
		Provider: "digitalocean", RegionName: "fra1", CSPRegion: "fra1",
		GroupName: "svc", InstanceType: "s-1vcpu-1gb", Image: "ubuntu-24-04-x64",
		Min: 1, Max: 1, Desired: 1,
		UserData: body,
	})
	if strings.Contains(user, "TOKEN=${var.pyx_token}") {
		t.Errorf("user-derived DO scale-group leaked an unescaped ${var.} reference:\n%s", user)
	}
}

// TestPlatformScaleGroupComponentsMarkEngineAuthored pins the wiring of the
// canonical platform builders to the engine seam.
func TestPlatformScaleGroupComponentsMarkEngineAuthored(t *testing.T) {
	comps := PlatformScaleGroupComponentsWithProviderBootstrap("x86_64", "ubuntu", "", PlatformBootstraps{"svc": "ud"}, nil)
	for _, c := range comps {
		if c.ScaleGroup == nil || !c.ScaleGroup.EngineAuthoredUserData {
			t.Fatalf("platform scale-group %q not marked EngineAuthoredUserData", c.Name)
		}
	}
}
