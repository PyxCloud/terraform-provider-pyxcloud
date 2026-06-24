package catalog

import (
	"context"
	"fmt"
	"strings"
)

// pipeline-control-plane is the abstract `pipeline-control-plane` component: the
// runtime infrastructure that EXECUTES a PyxCloud DevOps Pipeline IR pipeline on
// the "super-custom AWS Lambda" backend (the fully-custom executor, see
// [[devops-pipeline-ir]]). The pyx-pipeline-ir compiler emits the Step Functions
// ASL for a pipeline (e.g. aws/ci.json); THIS component provisions the AWS
// control-plane that ASL runs on — exactly the split called out in that ASL's
// header comment ("TF provisioning of the Step Functions state machine, Lambda,
// Fargate, and CodeBuild resources is handled by terraform-provider-pyxcloud, not
// by this compiler").
//
// This is the DOGFOOD task (pd-DEP-PYXLAMBDA-CONTROLPLANE): instead of bespoke
// terraform, express the control-plane as an abstract, catalog-driven component
// the provider descends to concrete AWS resources — the same abstract-first
// inversion the rest of the provider applies (SPEC §1).
//
// Per-provider mapping:
//
//   - AWS: the closed set of control-plane resources —
//     * aws_sfn_state_machine        (the pipeline orchestrator; ASL supplied by
//                                      the pyx-pipeline-ir compiler, referenced as
//                                      a variable so this component does NOT fork
//                                      the IR compiler)
//     * aws_lambda_function          (the PyxRunner Lambda — short steps)
//     * aws_ecs_cluster + Fargate    (long / Docker steps — capacity providers
//                                      FARGATE + FARGATE_SPOT)
//     * aws_codebuild_project        (container image builds — the Docker-in-CI
//                                      escape hatch)
//     * aws_iam_openid_connect_provider + roles (GitHub OIDC: the webhook/CI
//                                      identity assumes a role instead of long-lived
//                                      keys — kills the static-key fragility class).
//   - Every other provider: the pyx-lambda backend is AWS-specific (Step Functions
//     + Lambda + Fargate + CodeBuild have no clean cross-provider equivalent set),
//     so a non-AWS provider surfaces a clean ErrComponentUnsupported directing the
//     user to a managed-kubernetes-based runner (a future pipeline backend), never
//     an invented resource (SPEC §1, §4).
//
// REGION: like object-storage / scheduled-trigger / cache, the control-plane has
// no sizing catalog — the resources are region/account-scoped. The only catalog
// lookup is the region (region_name + provider -> csp_region), resolved via the
// RegionCatalog exactly like the other region-scoped components. No instance-type
// / price table is consulted (Lambda/Fargate/CodeBuild are serverless-priced).

// Canonical pipeline-control-plane type tokens. `pipeline-control-plane` is the
// canonical token; `pyx-lambda-control-plane` and `pipeline-runner` are accepted
// aliases (all name the same component).
const (
	TypePipelineControlPlane  = "pipeline-control-plane"
	TypePyxLambdaControlPlane = "pyx-lambda-control-plane"
	TypePipelineRunner        = "pipeline-runner"
)

// Default sizing for the control-plane resources. These are deliberately modest
// (a CI control plane, not a production fleet) and overridable on the spec.
const (
	defaultRunnerMemoryMB    = 512
	defaultRunnerTimeoutSecs = 900 // Lambda max for short steps; long steps go to Fargate
	defaultRunnerRuntime     = "provided.al2023"
	defaultFargateCPU        = "1024" // 1 vCPU
	defaultFargateMemoryMB   = "2048" // 2 GB
	defaultCodeBuildCompute  = "BUILD_GENERAL1_SMALL"
	defaultCodeBuildImage    = "aws/codebuild/amazonlinux2-x86_64-standard:5.0"
)

// PipelineControlPlaneSpec is the abstract description of a pyx-lambda DevOps
// control-plane. Provider-neutral surface; AWS is the only backend today.
type PipelineControlPlaneSpec struct {
	Name     string // control-plane name, e.g. "pyx-ci" (prefixes every resource)
	Region   string // abstract pyx region_name
	Provider string // aws (only supported backend)

	// PipelineName is the logical pipeline this control-plane runs (e.g. "ci").
	// Empty -> defaults to Name. Used to label the state machine.
	PipelineName string

	// StateMachineDefinition is the Step Functions ASL JSON the pyx-pipeline-ir
	// compiler emits for the pipeline (e.g. the contents of aws/ci.json). Empty ->
	// the render declares an out-of-band variable so the ASL is supplied at apply
	// time, keeping this component decoupled from the IR compiler (it provisions
	// the runtime, it does NOT compile the pipeline).
	StateMachineDefinition string

	// Runner (short-step Lambda) sizing. 0 -> defaults.
	RunnerMemoryMB       int
	RunnerTimeoutSecs    int
	RunnerRuntime        string // Lambda runtime id; "" -> provided.al2023 (the Go custom runtime)
	RunnerSourceArtifact string // deployment package ref ("" -> out-of-band variable)

	// Fargate (long/Docker-step ECS) sizing. "" -> defaults.
	FargateCPU      string // task vCPU units, e.g. "1024"
	FargateMemoryMB string // task memory MB, e.g. "2048"

	// CodeBuild (image-build) sizing. "" -> defaults.
	CodeBuildCompute string // BUILD_GENERAL1_SMALL | _MEDIUM | _LARGE
	CodeBuildImage   string // managed build image

	// GitHubOIDC enables the GitHub Actions OIDC identity provider + a CI role the
	// webhook/runner assumes (no long-lived keys). When set, GitHubOwnerRepo scopes
	// the trust to a single repo "owner/repo" (required when GitHubOIDC is true).
	GitHubOIDC      bool
	GitHubOwnerRepo string // "owner/repo" the OIDC role trusts (e.g. "PyxCloud/terraform-provider-pyxcloud")
}

// PipelineControlPlanePlan is the deterministic, catalog-resolved concrete
// translation of a PipelineControlPlaneSpec. STRUCTURED plan (not rendered .tf) —
// the provider owns rendering and state, consistent with the other components
// (SPEC §8). It enumerates the resource types the control-plane provisions so a
// reviewer can see the whole closed set at a glance.
type PipelineControlPlanePlan struct {
	Provider   string `json:"provider"`    // aws
	CSP        string `json:"csp"`         // aws
	RegionName string `json:"region_name"` // abstract pyx region
	CSPRegion  string `json:"csp_region"`  // concrete provider region (catalog-resolved)

	Name         string `json:"name"`
	PipelineName string `json:"pipeline_name"`

	// HasStateMachineDefinition is true when the ASL was supplied inline; false
	// means the render declares an out-of-band variable for it.
	HasStateMachineDefinition bool   `json:"has_state_machine_definition"`
	StateMachineDefinition    string `json:"state_machine_definition,omitempty"`

	RunnerMemoryMB       int    `json:"runner_memory_mb"`
	RunnerTimeoutSecs    int    `json:"runner_timeout_secs"`
	RunnerRuntime        string `json:"runner_runtime"`
	RunnerSourceArtifact string `json:"runner_source_artifact,omitempty"`

	FargateCPU      string `json:"fargate_cpu"`
	FargateMemoryMB string `json:"fargate_memory_mb"`

	CodeBuildCompute string `json:"codebuild_compute"`
	CodeBuildImage   string `json:"codebuild_image"`

	GitHubOIDC      bool   `json:"github_oidc"`
	GitHubOwnerRepo string `json:"github_owner_repo,omitempty"`

	// ResourceTypes is the closed set of concrete provider resources this plan
	// provisions, in render order — the dogfood "control plane" enumerated.
	ResourceTypes []string `json:"resource_types"`
}

// TranslatePipelineControlPlane resolves a PipelineControlPlaneSpec into a
// concrete PipelineControlPlanePlan using the catalog. Deterministic and
// catalog-driven: the csp_region comes from the region catalog (never invented).
// AWS is the only backend; any other provider surfaces a clean ErrComponentUnsupported
// (never a silent fallback), per SPEC §4.
func TranslatePipelineControlPlane(ctx context.Context, cat RegionCatalog, spec PipelineControlPlaneSpec) (PipelineControlPlanePlan, error) {
	if err := validatePipelineControlPlaneSpec(spec); err != nil {
		return PipelineControlPlanePlan{}, err
	}
	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return PipelineControlPlanePlan{}, err
	}
	provider := lc(spec.Provider)
	if provider != ProviderAWS {
		return PipelineControlPlanePlan{}, ErrComponentUnsupported{
			Component: TypePipelineControlPlane, Provider: provider, CSP: row.CSP, CSPRegion: row.CSPRegion,
			Alternative: "the pyx-lambda DevOps control-plane is AWS-specific (Step Functions + Lambda + " +
				"Fargate + CodeBuild have no clean cross-provider equivalent set); run the pipeline on a " +
				"managed-kubernetes-based runner, or target aws for the pyx-lambda backend",
		}
	}

	name := canonicalName(spec.Name, "pyx-ci")
	pipeline := strings.TrimSpace(spec.PipelineName)
	if pipeline == "" {
		pipeline = name
	}

	mem := spec.RunnerMemoryMB
	if mem <= 0 {
		mem = defaultRunnerMemoryMB
	}
	timeout := spec.RunnerTimeoutSecs
	if timeout <= 0 {
		timeout = defaultRunnerTimeoutSecs
	}
	runtime := strings.TrimSpace(spec.RunnerRuntime)
	if runtime == "" {
		runtime = defaultRunnerRuntime
	}
	fargateCPU := strings.TrimSpace(spec.FargateCPU)
	if fargateCPU == "" {
		fargateCPU = defaultFargateCPU
	}
	fargateMem := strings.TrimSpace(spec.FargateMemoryMB)
	if fargateMem == "" {
		fargateMem = defaultFargateMemoryMB
	}
	cbCompute := strings.TrimSpace(spec.CodeBuildCompute)
	if cbCompute == "" {
		cbCompute = defaultCodeBuildCompute
	}
	cbImage := strings.TrimSpace(spec.CodeBuildImage)
	if cbImage == "" {
		cbImage = defaultCodeBuildImage
	}

	def := strings.TrimSpace(spec.StateMachineDefinition)

	plan := PipelineControlPlanePlan{
		Provider:     provider,
		CSP:          row.CSP,
		RegionName:   row.RegionName,
		CSPRegion:    row.CSPRegion,
		Name:         name,
		PipelineName: pipeline,

		HasStateMachineDefinition: def != "",
		StateMachineDefinition:    def,

		RunnerMemoryMB:       mem,
		RunnerTimeoutSecs:    timeout,
		RunnerRuntime:        runtime,
		RunnerSourceArtifact: strings.TrimSpace(spec.RunnerSourceArtifact),

		FargateCPU:      fargateCPU,
		FargateMemoryMB: fargateMem,

		CodeBuildCompute: cbCompute,
		CodeBuildImage:   cbImage,

		GitHubOIDC:      spec.GitHubOIDC,
		GitHubOwnerRepo: strings.TrimSpace(spec.GitHubOwnerRepo),
	}

	// The closed set of control-plane resources, in render order. IAM roles come
	// first (referenced by the runtime resources), then the runtime, then the
	// orchestrator, then the optional OIDC identity.
	plan.ResourceTypes = []string{
		"aws_iam_role",          // runner Lambda execution role
		"aws_iam_role",          // ECS task execution role
		"aws_iam_role",          // CodeBuild service role
		"aws_iam_role",          // Step Functions execution role
		"aws_lambda_function",   // PyxRunner (short steps)
		"aws_ecs_cluster",       // Fargate cluster (long/Docker steps)
		"aws_codebuild_project", // image builds
		"aws_sfn_state_machine", // the pipeline orchestrator
	}
	if plan.GitHubOIDC {
		plan.ResourceTypes = append(plan.ResourceTypes,
			"aws_iam_openid_connect_provider", // GitHub OIDC identity provider
			"aws_iam_role",                    // GitHub CI role (assumed via OIDC)
		)
	}
	return plan, nil
}

func validatePipelineControlPlaneSpec(spec PipelineControlPlaneSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("pipeline-control-plane: region (abstract pyx region_name) is required")
	}
	if strings.TrimSpace(spec.Provider) == "" {
		return fmt.Errorf("pipeline-control-plane: provider is required (aws)")
	}
	if _, ok := ProviderToCSP(spec.Provider); !ok {
		return fmt.Errorf("pipeline-control-plane: unknown provider %q (aws)", spec.Provider)
	}
	if spec.RunnerMemoryMB < 0 {
		return fmt.Errorf("pipeline-control-plane: runner_memory_mb must be >= 0")
	}
	if spec.RunnerTimeoutSecs < 0 {
		return fmt.Errorf("pipeline-control-plane: runner_timeout_secs must be >= 0")
	}
	// EventBridge/Lambda cap a single invocation at 15 minutes (900s); longer steps
	// belong on Fargate, so reject an over-cap runner timeout deterministically.
	if spec.RunnerTimeoutSecs > 900 {
		return fmt.Errorf("pipeline-control-plane: runner_timeout_secs must be <= 900 " +
			"(Lambda's 15-minute cap); route longer steps to the Fargate runner")
	}
	if spec.GitHubOIDC && strings.TrimSpace(spec.GitHubOwnerRepo) == "" {
		return fmt.Errorf("pipeline-control-plane: github_owner_repo (\"owner/repo\") is required when " +
			"github_oidc is enabled (the OIDC role trust must be scoped to a repo, never wildcarded)")
	}
	if repo := strings.TrimSpace(spec.GitHubOwnerRepo); repo != "" {
		if parts := strings.Split(repo, "/"); len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return fmt.Errorf("pipeline-control-plane: github_owner_repo must be %q (got %q)", "owner/repo", repo)
		}
	}
	return nil
}

// CanonicalPipelineControlPlaneType maps an accepted type token to the canonical
// pipeline-control-plane token, reporting whether it is recognised.
func CanonicalPipelineControlPlaneType(t string) (string, bool) {
	switch lc(t) {
	case TypePipelineControlPlane, TypePyxLambdaControlPlane, TypePipelineRunner:
		return TypePipelineControlPlane, true
	default:
		return "", false
	}
}
