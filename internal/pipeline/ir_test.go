package pipeline

import (
	"strings"
	"testing"
)

// deployMCP expresses the real deploy-mcp pipeline in the IR — the SPEC §6
// coverage proof: a two-job DAG (test -> deploy) using checkout, setup-tool,
// run, cloud-auth (OIDC), an artifact handoff, and a step-up deploy gate.
func deployMCP() *Pipeline {
	return &Pipeline{
		Name: "deploy-mcp",
		Triggers: []Trigger{
			{Kind: TriggerPush, Branches: []string{"main"}},
			{Kind: TriggerManual, Inputs: []Input{{Name: "env", Default: "beta"}}},
		},
		Concurrency: &Concurrency{Group: "mcp-deploy-beta", CancelInProgress: false},
		Secrets:     []SecretRef{{Name: "AWS_ROLE_ARN", FromKey: "beta/mcp/deploy-role"}},
		Jobs: []Job{
			{
				ID:     "check-and-test",
				Runner: Runner{SizeClass: SizeStandard},
				Steps: []Step{
					{Name: "checkout", Capability: CapCheckout},
					{Name: "setup go", Capability: CapSetupTool, With: map[string]string{"tool": "go", "version": "1.22"}},
					{Name: "build/vet/test", Run: "cd mcp-go && go build ./... && go vet ./... && go test ./..."},
				},
				Produces: []Artifact{{Name: "mcp-bin", Paths: []string{"mcp-go/bin/"}}},
			},
			{
				ID:       "deploy",
				Needs:    []string{"check-and-test"},
				Runner:   Runner{SizeClass: SizeStandard},
				Gate:     &Gate{Type: "approval", Approvers: []string{"owner"}, RequireStepUp: true},
				Consumes: []string{"mcp-bin"},
				Steps: []Step{
					{Name: "aws oidc", Capability: CapCloudAuth, With: map[string]string{"provider": "aws", "via": "oidc"}},
					{Name: "terraform apply", Run: "terraform init -reconfigure && terraform apply -auto-approve"},
				},
			},
		},
	}
}

func TestDeployMCPIRValidates(t *testing.T) {
	if err := deployMCP().Validate(); err != nil {
		t.Fatalf("deploy-mcp IR should validate, got: %v", err)
	}
}

func TestTopoOrderDependenciesFirst(t *testing.T) {
	order := deployMCP().TopoOrder()
	if len(order) != 2 {
		t.Fatalf("expected 2 jobs in order, got %v", order)
	}
	if order[0] != "check-and-test" || order[1] != "deploy" {
		t.Fatalf("dependency must precede dependent, got %v", order)
	}
}

func TestValidateRejects(t *testing.T) {
	base := func() *Pipeline { return deployMCP() }
	cases := []struct {
		name   string
		mutate func(*Pipeline)
		want   string
	}{
		{"no name", func(p *Pipeline) { p.Name = "" }, "name is required"},
		{"no jobs", func(p *Pipeline) { p.Jobs = nil }, "at least one job"},
		{"dup id", func(p *Pipeline) { p.Jobs[1].ID = "check-and-test" }, "duplicate job id"},
		{"bad size", func(p *Pipeline) { p.Jobs[0].Runner.SizeClass = "huge" }, "sizeClass must be"},
		{"missing dep", func(p *Pipeline) { p.Jobs[1].Needs = []string{"ghost"} }, "needs unknown job"},
		{"self need", func(p *Pipeline) { p.Jobs[0].Needs = []string{"check-and-test"} }, "cannot need itself"},
		{"both cap and run", func(p *Pipeline) { p.Jobs[0].Steps[0].Run = "echo hi" }, "exactly one of capability or run"},
		{"neither cap nor run", func(p *Pipeline) { p.Jobs[0].Steps[2] = Step{Name: "empty"} }, "exactly one of capability or run"},
		{"unknown capability", func(p *Pipeline) { p.Jobs[0].Steps[0].Capability = "teleport" }, "unknown capability"},
		{"consume without produce", func(p *Pipeline) { p.Jobs[1].Consumes = []string{"nope"} }, "no job produces"},
		{"gate without approvers", func(p *Pipeline) { p.Jobs[1].Gate.Approvers = nil }, "at least one approver"},
		{"dup artifact", func(p *Pipeline) {
			p.Jobs[1].Produces = []Artifact{{Name: "mcp-bin", Paths: []string{"x/"}}}
		}, "already produced"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := base()
			c.mutate(p)
			err := p.Validate()
			if err == nil {
				t.Fatalf("expected validation error containing %q, got nil", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), c.want)
			}
		})
	}
}

func TestDetectCycle(t *testing.T) {
	p := &Pipeline{
		Name: "cyclic",
		Jobs: []Job{
			{ID: "a", Needs: []string{"b"}, Runner: Runner{SizeClass: SizeSmall}, Steps: []Step{{Name: "s", Run: "true"}}},
			{ID: "b", Needs: []string{"a"}, Runner: Runner{SizeClass: SizeSmall}, Steps: []Step{{Name: "s", Run: "true"}}},
		},
	}
	err := p.Validate()
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected cycle error, got %v", err)
	}
}
