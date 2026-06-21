// Package pipeline defines the PyxCloud DevOps Pipeline IR: a canonical,
// backend-agnostic model of a CI/CD pipeline. The IR is the contract that
// parifies pipelines across execution backends (GitHub Actions today, the
// super-custom AWS Lambda control plane next) — the same inversion the TF
// provider applies to infrastructure. See PIPELINE-SPEC.md.
//
// This package is the canonical model only: backend adapters (compile/run) and
// the surface parsers (YAML/HCL) live in later phases. What ships here is the
// type system plus Validate(), which fail-closes a pipeline before any backend
// sees it.
package pipeline

import (
	"fmt"
	"sort"
	"strings"
)

// Capability is the closed set of portable, typed step actions. Every backend
// must implement every capability natively (SPEC §3). The set is deliberately
// small so cross-backend parity stays tractable.
type Capability string

const (
	CapCheckout         Capability = "checkout"
	CapSetupTool        Capability = "setup-tool"
	CapCache            Capability = "cache"
	CapArtifactUpload   Capability = "artifact-upload"
	CapArtifactDownload Capability = "artifact-download"
	CapCloudAuth        Capability = "cloud-auth"
)

// knownCapabilities is the closed set of typed, portable capabilities. The raw
// shell escape hatch is the Step.Run FIELD (not a capability): a step is either
// a typed Capability invocation XOR a Run script (SPEC §2/§3).
func knownCapabilities() map[Capability]bool {
	return map[Capability]bool{
		CapCheckout: true, CapSetupTool: true, CapCache: true,
		CapArtifactUpload: true, CapArtifactDownload: true,
		CapCloudAuth: true,
	}
}

// SizeClass abstracts runner capacity; a backend maps it to a concrete runner
// (a GHA label, or a Lambda memory tier / Fargate task size).
type SizeClass string

const (
	SizeSmall    SizeClass = "small"
	SizeStandard SizeClass = "standard"
	SizeLarge    SizeClass = "large"
)

// TriggerKind enumerates the portable trigger shapes.
type TriggerKind string

const (
	TriggerPush        TriggerKind = "push"
	TriggerPullRequest TriggerKind = "pull_request"
	TriggerManual      TriggerKind = "manual"
	TriggerSchedule    TriggerKind = "schedule"
)

// Pipeline is a DAG of jobs with triggers, plus pipeline-wide env and named
// secret references. It is the top-level IR object.
type Pipeline struct {
	Name        string            `json:"name"`
	Triggers    []Trigger         `json:"triggers,omitempty"`
	Concurrency *Concurrency      `json:"concurrency,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Secrets     []SecretRef       `json:"secrets,omitempty"`
	Jobs        []Job             `json:"jobs"`
}

// Trigger is a portable pipeline trigger. Only the fields relevant to Kind are
// meaningful (Branches for push/pull_request, Cron for schedule, Inputs for
// manual).
type Trigger struct {
	Kind     TriggerKind `json:"kind"`
	Branches []string    `json:"branches,omitempty"`
	Cron     string      `json:"cron,omitempty"`
	Inputs   []Input     `json:"inputs,omitempty"`
}

// Input is a manual-dispatch input parameter.
type Input struct {
	Name     string `json:"name"`
	Default  string `json:"default,omitempty"`
	Required bool   `json:"required,omitempty"`
}

// Concurrency serializes pipeline runs sharing a group, optionally cancelling
// an in-progress run when a newer one starts.
type Concurrency struct {
	Group            string `json:"group"`
	CancelInProgress bool   `json:"cancelInProgress,omitempty"`
}

// SecretRef names a secret the pipeline needs. FromKey is the lookup key in the
// backend's secret store. The IR never carries secret values.
type SecretRef struct {
	Name    string `json:"name"`
	FromKey string `json:"fromKey"`
}

// Runner abstracts where a job executes.
type Runner struct {
	SizeClass SizeClass `json:"sizeClass"`
	Image     string    `json:"image,omitempty"`
	Arch      string    `json:"arch,omitempty"`
}

// Job is a node in the pipeline DAG. Needs lists upstream job ids.
type Job struct {
	ID             string              `json:"id"`
	Needs          []string            `json:"needs,omitempty"`
	Runner         Runner              `json:"runner"`
	Matrix         map[string][]string `json:"matrix,omitempty"`
	If             string              `json:"if,omitempty"`
	Env            map[string]string   `json:"env,omitempty"`
	TimeoutMinutes int                 `json:"timeoutMinutes,omitempty"`
	Gate           *Gate               `json:"gate,omitempty"`
	Steps          []Step              `json:"steps"`
	Produces       []Artifact          `json:"produces,omitempty"`
	Consumes       []string            `json:"consumes,omitempty"`
}

// Step is one action in a job: either a typed Capability invocation or a raw
// Run (exactly one of the two).
type Step struct {
	Name            string            `json:"name"`
	Capability      Capability        `json:"capability,omitempty"`
	Run             string            `json:"run,omitempty"`
	With            map[string]string `json:"with,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
	WorkingDir      string            `json:"workingDir,omitempty"`
	ContinueOnError bool              `json:"continueOnError,omitempty"`
}

// Gate guards a job behind an approval, optionally requiring passkey/biometric
// step-up. It resolves against the board's approval + step-up surfaces.
type Gate struct {
	Type          string   `json:"type"` // currently only "approval"
	Approvers     []string `json:"approvers,omitempty"`
	RequireStepUp bool     `json:"requireStepUp,omitempty"`
}

// Artifact is a named set of paths a job produces for downstream jobs.
type Artifact struct {
	Name          string   `json:"name"`
	Paths         []string `json:"paths"`
	RetentionDays int      `json:"retentionDays,omitempty"`
}

// Validate fail-closes a pipeline before any backend compiles it (SPEC §5).
// It returns the FIRST problem found, with a stable, actionable message.
func (p *Pipeline) Validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("pipeline: name is required")
	}
	if len(p.Jobs) == 0 {
		return fmt.Errorf("pipeline %q: at least one job is required", p.Name)
	}

	secretNames := map[string]bool{}
	for _, s := range p.Secrets {
		if s.Name == "" || s.FromKey == "" {
			return fmt.Errorf("pipeline %q: secret needs both name and fromKey", p.Name)
		}
		secretNames[s.Name] = true
	}

	caps := knownCapabilities()
	ids := map[string]bool{}
	// produced artifact name -> producing job id, for consume validation.
	produced := map[string]string{}

	for _, j := range p.Jobs {
		if j.ID == "" {
			return fmt.Errorf("pipeline %q: a job has an empty id", p.Name)
		}
		if ids[j.ID] {
			return fmt.Errorf("pipeline %q: duplicate job id %q", p.Name, j.ID)
		}
		ids[j.ID] = true

		if j.Runner.SizeClass != SizeSmall && j.Runner.SizeClass != SizeStandard && j.Runner.SizeClass != SizeLarge {
			return fmt.Errorf("job %q: runner.sizeClass must be small|standard|large, got %q", j.ID, j.Runner.SizeClass)
		}
		if len(j.Steps) == 0 {
			return fmt.Errorf("job %q: at least one step is required", j.ID)
		}
		if j.Gate != nil {
			if j.Gate.Type != "approval" {
				return fmt.Errorf("job %q: gate.type must be \"approval\", got %q", j.ID, j.Gate.Type)
			}
			if len(j.Gate.Approvers) == 0 {
				return fmt.Errorf("job %q: gate needs at least one approver", j.ID)
			}
		}
		for i, st := range j.Steps {
			hasCap := st.Capability != ""
			hasRun := strings.TrimSpace(st.Run) != ""
			if hasCap == hasRun {
				return fmt.Errorf("job %q step %d (%q): set exactly one of capability or run", j.ID, i, st.Name)
			}
			if hasCap && !caps[st.Capability] {
				return fmt.Errorf("job %q step %d: unknown capability %q", j.ID, i, st.Capability)
			}
		}
		for _, a := range j.Produces {
			if a.Name == "" || len(a.Paths) == 0 {
				return fmt.Errorf("job %q: produced artifact needs a name and at least one path", j.ID)
			}
			if owner, dup := produced[a.Name]; dup {
				return fmt.Errorf("job %q: artifact %q already produced by job %q", j.ID, a.Name, owner)
			}
			produced[a.Name] = j.ID
		}
	}

	// needs must reference existing jobs; consumes must reference produced artifacts.
	for _, j := range p.Jobs {
		for _, n := range j.Needs {
			if !ids[n] {
				return fmt.Errorf("job %q: needs unknown job %q", j.ID, n)
			}
			if n == j.ID {
				return fmt.Errorf("job %q: cannot need itself", j.ID)
			}
		}
		for _, c := range j.Consumes {
			if _, ok := produced[c]; !ok {
				return fmt.Errorf("job %q: consumes artifact %q that no job produces", j.ID, c)
			}
		}
	}

	if cycle := p.detectCycle(); len(cycle) > 0 {
		return fmt.Errorf("pipeline %q: job dependency cycle: %s", p.Name, strings.Join(cycle, " -> "))
	}
	return nil
}

// detectCycle returns a representative cycle path through Needs, or nil if the
// job graph is acyclic (DFS with a recursion stack).
func (p *Pipeline) detectCycle() []string {
	deps := map[string][]string{}
	for _, j := range p.Jobs {
		d := append([]string(nil), j.Needs...)
		sort.Strings(d) // deterministic traversal/messages
		deps[j.ID] = d
	}
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	var path []string
	var dfs func(string) []string
	dfs = func(u string) []string {
		color[u] = gray
		path = append(path, u)
		for _, v := range deps[u] {
			if color[v] == gray {
				return append(path, v) // back-edge: cycle
			}
			if color[v] == white {
				if c := dfs(v); c != nil {
					return c
				}
			}
		}
		color[u] = black
		path = path[:len(path)-1]
		return nil
	}
	idsSorted := make([]string, 0, len(deps))
	for id := range deps {
		idsSorted = append(idsSorted, id)
	}
	sort.Strings(idsSorted)
	for _, id := range idsSorted {
		if color[id] == white {
			path = nil
			if c := dfs(id); c != nil {
				return c
			}
		}
	}
	return nil
}

// TopoOrder returns job ids in a deterministic topological order (dependencies
// before dependents). It assumes Validate has passed (acyclic); on a cycle it
// returns the partial order built so far.
func (p *Pipeline) TopoOrder() []string {
	deps := map[string][]string{}
	indeg := map[string]int{}
	for _, j := range p.Jobs {
		if _, ok := indeg[j.ID]; !ok {
			indeg[j.ID] = 0
		}
		for _, n := range j.Needs {
			deps[n] = append(deps[n], j.ID)
			indeg[j.ID]++
		}
	}
	var ready []string
	for id, d := range indeg {
		if d == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	var order []string
	for len(ready) > 0 {
		u := ready[0]
		ready = ready[1:]
		order = append(order, u)
		next := append([]string(nil), deps[u]...)
		sort.Strings(next)
		for _, v := range next {
			indeg[v]--
			if indeg[v] == 0 {
				ready = append(ready, v)
			}
		}
		sort.Strings(ready)
	}
	return order
}
