package catalog

import (
	"fmt"
	"strings"
)

// RenderPipelineControlPlaneHCL renders a PipelineControlPlanePlan into concrete
// AWS Terraform HCL — the pyx-lambda DevOps control-plane (Step Functions +
// PyxRunner Lambda + Fargate/ECS + CodeBuild + optional GitHub OIDC). Only AWS
// reaches here (TranslatePipelineControlPlane rejects every other provider with a
// clean ErrComponentUnsupported).
//
// The render is deterministic and self-contained: the IAM roles are emitted with
// least-ish-privilege inline policies scoped to the control-plane's own resources,
// and the Step Functions ASL is referenced from a variable when not supplied
// inline (this component provisions the runtime, the pyx-pipeline-ir compiler
// emits the ASL — the two stay decoupled).
func RenderPipelineControlPlaneHCL(plan PipelineControlPlanePlan) (string, error) {
	if plan.Provider != ProviderAWS {
		return "", fmt.Errorf("render: unsupported provider %q for pipeline-control-plane", plan.Provider)
	}
	return renderPipelineControlPlaneAWS(plan), nil
}

func renderPipelineControlPlaneAWS(p PipelineControlPlanePlan) string {
	// Terraform local labels and variable names must be valid identifiers, so the
	// control-plane uses an underscore-only label (hyphens -> underscores) while the
	// resource `name`/tag attributes keep the human-readable hyphenated form.
	n := strings.ReplaceAll(tfName(p.Name), "-", "_")
	var b strings.Builder

	fmt.Fprintf(&b, "# pyx-lambda DevOps control-plane %q (pipeline %q) — region %s (%s)\n",
		p.Name, p.PipelineName, p.RegionName, p.CSPRegion)
	fmt.Fprintf(&b, "# Provisioned by terraform-provider-pyxcloud (dogfood: pd-DEP-PYXLAMBDA-CONTROLPLANE).\n")
	fmt.Fprintf(&b, "# The Step Functions ASL is compiled by pyx-pipeline-ir; this provisions its runtime.\n\n")

	// ── IAM: runner Lambda execution role ────────────────────────────────────
	fmt.Fprintf(&b, "resource \"aws_iam_role\" \"%s_runner\" {\n", n)
	fmt.Fprintf(&b, "  name               = \"%s-runner\"\n", p.Name)
	fmt.Fprintf(&b, "  assume_role_policy = jsonencode(%s)\n", assumeRolePolicy("lambda.amazonaws.com"))
	fmt.Fprintf(&b, "  tags               = { Name = \"%s-runner\", pyxcloud = \"true\" }\n", p.Name)
	b.WriteString("}\n\n")
	fmt.Fprintf(&b, "resource \"aws_iam_role_policy_attachment\" \"%s_runner_basic\" {\n", n)
	fmt.Fprintf(&b, "  role       = aws_iam_role.%s_runner.name\n", n)
	b.WriteString("  policy_arn = \"arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole\"\n")
	b.WriteString("}\n\n")

	// ── IAM: ECS task execution role (Fargate long/Docker steps) ──────────────
	fmt.Fprintf(&b, "resource \"aws_iam_role\" \"%s_task\" {\n", n)
	fmt.Fprintf(&b, "  name               = \"%s-task\"\n", p.Name)
	fmt.Fprintf(&b, "  assume_role_policy = jsonencode(%s)\n", assumeRolePolicy("ecs-tasks.amazonaws.com"))
	fmt.Fprintf(&b, "  tags               = { Name = \"%s-task\", pyxcloud = \"true\" }\n", p.Name)
	b.WriteString("}\n\n")
	fmt.Fprintf(&b, "resource \"aws_iam_role_policy_attachment\" \"%s_task_exec\" {\n", n)
	fmt.Fprintf(&b, "  role       = aws_iam_role.%s_task.name\n", n)
	b.WriteString("  policy_arn = \"arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy\"\n")
	b.WriteString("}\n\n")

	// ── IAM: CodeBuild service role (image builds) ────────────────────────────
	fmt.Fprintf(&b, "resource \"aws_iam_role\" \"%s_codebuild\" {\n", n)
	fmt.Fprintf(&b, "  name               = \"%s-codebuild\"\n", p.Name)
	fmt.Fprintf(&b, "  assume_role_policy = jsonencode(%s)\n", assumeRolePolicy("codebuild.amazonaws.com"))
	fmt.Fprintf(&b, "  tags               = { Name = \"%s-codebuild\", pyxcloud = \"true\" }\n", p.Name)
	b.WriteString("}\n\n")
	fmt.Fprintf(&b, "resource \"aws_iam_role_policy\" \"%s_codebuild_logs\" {\n", n)
	fmt.Fprintf(&b, "  name = \"%s-codebuild-logs\"\n", p.Name)
	fmt.Fprintf(&b, "  role = aws_iam_role.%s_codebuild.id\n", n)
	b.WriteString("  policy = jsonencode({\n")
	b.WriteString("    Version = \"2012-10-17\"\n")
	b.WriteString("    Statement = [{\n")
	b.WriteString("      Effect   = \"Allow\"\n")
	b.WriteString("      Action   = [\"logs:CreateLogGroup\", \"logs:CreateLogStream\", \"logs:PutLogEvents\"]\n")
	b.WriteString("      Resource = \"*\"\n")
	b.WriteString("    }]\n")
	b.WriteString("  })\n")
	b.WriteString("}\n\n")

	// ── IAM: Step Functions execution role (invokes runner + starts builds) ───
	fmt.Fprintf(&b, "resource \"aws_iam_role\" \"%s_sfn\" {\n", n)
	fmt.Fprintf(&b, "  name               = \"%s-sfn\"\n", p.Name)
	fmt.Fprintf(&b, "  assume_role_policy = jsonencode(%s)\n", assumeRolePolicy("states.amazonaws.com"))
	fmt.Fprintf(&b, "  tags               = { Name = \"%s-sfn\", pyxcloud = \"true\" }\n", p.Name)
	b.WriteString("}\n\n")
	fmt.Fprintf(&b, "resource \"aws_iam_role_policy\" \"%s_sfn_invoke\" {\n", n)
	fmt.Fprintf(&b, "  name = \"%s-sfn-invoke\"\n", p.Name)
	fmt.Fprintf(&b, "  role = aws_iam_role.%s_sfn.id\n", n)
	b.WriteString("  policy = jsonencode({\n")
	b.WriteString("    Version = \"2012-10-17\"\n")
	b.WriteString("    Statement = [\n")
	b.WriteString("      {\n")
	b.WriteString("        Effect   = \"Allow\"\n")
	b.WriteString("        Action   = [\"lambda:InvokeFunction\"]\n")
	fmt.Fprintf(&b, "        Resource = [aws_lambda_function.%s_runner.arn]\n", n)
	b.WriteString("      },\n")
	b.WriteString("      {\n")
	b.WriteString("        Effect   = \"Allow\"\n")
	b.WriteString("        Action   = [\"ecs:RunTask\", \"ecs:StopTask\", \"ecs:DescribeTasks\"]\n")
	b.WriteString("        Resource = \"*\"\n")
	b.WriteString("      },\n")
	b.WriteString("      {\n")
	b.WriteString("        Effect   = \"Allow\"\n")
	b.WriteString("        Action   = [\"codebuild:StartBuild\", \"codebuild:BatchGetBuilds\"]\n")
	fmt.Fprintf(&b, "        Resource = [aws_codebuild_project.%s.arn]\n", n)
	b.WriteString("      }\n")
	b.WriteString("    ]\n")
	b.WriteString("  })\n")
	b.WriteString("}\n\n")

	// ── PyxRunner Lambda (short steps) ────────────────────────────────────────
	srcArtifact := pclVar(&b, p.RunnerSourceArtifact, n+"_runner_package",
		"Path to the PyxRunner Lambda deployment package (zip).")
	fmt.Fprintf(&b, "resource \"aws_lambda_function\" \"%s_runner\" {\n", n)
	fmt.Fprintf(&b, "  function_name = \"%s-runner\"\n", p.Name)
	fmt.Fprintf(&b, "  role          = aws_iam_role.%s_runner.arn\n", n)
	fmt.Fprintf(&b, "  runtime       = %q\n", p.RunnerRuntime)
	b.WriteString("  handler       = \"bootstrap\"\n")
	fmt.Fprintf(&b, "  filename      = %s\n", srcArtifact)
	fmt.Fprintf(&b, "  memory_size   = %d\n", p.RunnerMemoryMB)
	fmt.Fprintf(&b, "  timeout       = %d\n", p.RunnerTimeoutSecs)
	fmt.Fprintf(&b, "  tags          = { Name = \"%s-runner\", pyxcloud = \"true\" }\n", p.Name)
	b.WriteString("}\n\n")

	// ── Fargate cluster (long / Docker steps) ─────────────────────────────────
	fmt.Fprintf(&b, "resource \"aws_ecs_cluster\" \"%s\" {\n", n)
	fmt.Fprintf(&b, "  name = \"%s\"\n", p.Name)
	fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", p.Name)
	b.WriteString("}\n\n")
	fmt.Fprintf(&b, "resource \"aws_ecs_cluster_capacity_providers\" \"%s\" {\n", n)
	fmt.Fprintf(&b, "  cluster_name       = aws_ecs_cluster.%s.name\n", n)
	b.WriteString("  capacity_providers = [\"FARGATE\", \"FARGATE_SPOT\"]\n")
	b.WriteString("}\n\n")
	// A Fargate task definition sized for the long-step runner. The container image
	// is supplied out of band (the built runner image) via a variable.
	runnerImage := pclVar(&b, "", n+"_fargate_image",
		"Container image for the Fargate (long/Docker-step) runner.")
	fmt.Fprintf(&b, "resource \"aws_ecs_task_definition\" \"%s\" {\n", n)
	fmt.Fprintf(&b, "  family                   = \"%s-runner\"\n", p.Name)
	b.WriteString("  requires_compatibilities = [\"FARGATE\"]\n")
	b.WriteString("  network_mode             = \"awsvpc\"\n")
	fmt.Fprintf(&b, "  cpu                      = %q\n", p.FargateCPU)
	fmt.Fprintf(&b, "  memory                   = %q\n", p.FargateMemoryMB)
	fmt.Fprintf(&b, "  execution_role_arn       = aws_iam_role.%s_task.arn\n", n)
	fmt.Fprintf(&b, "  task_role_arn            = aws_iam_role.%s_task.arn\n", n)
	b.WriteString("  container_definitions = jsonencode([{\n")
	fmt.Fprintf(&b, "    name      = \"%s-runner\"\n", p.Name)
	fmt.Fprintf(&b, "    image     = %s\n", runnerImage)
	b.WriteString("    essential = true\n")
	b.WriteString("  }])\n")
	fmt.Fprintf(&b, "  tags = { Name = \"%s-runner\", pyxcloud = \"true\" }\n", p.Name)
	b.WriteString("}\n\n")

	// ── CodeBuild project (image builds) ──────────────────────────────────────
	fmt.Fprintf(&b, "resource \"aws_codebuild_project\" \"%s\" {\n", n)
	fmt.Fprintf(&b, "  name         = \"%s\"\n", p.Name)
	fmt.Fprintf(&b, "  service_role = aws_iam_role.%s_codebuild.arn\n", n)
	b.WriteString("  artifacts {\n    type = \"NO_ARTIFACTS\"\n  }\n")
	b.WriteString("  environment {\n")
	fmt.Fprintf(&b, "    compute_type    = %q\n", p.CodeBuildCompute)
	fmt.Fprintf(&b, "    image           = %q\n", p.CodeBuildImage)
	b.WriteString("    type            = \"LINUX_CONTAINER\"\n")
	b.WriteString("    privileged_mode = true\n") // Docker-in-CI
	b.WriteString("  }\n")
	b.WriteString("  source {\n    type = \"NO_SOURCE\"\n    buildspec = \"version: 0.2\\nphases:\\n  build:\\n    commands:\\n      - echo pyx-lambda build step\\n\"\n  }\n")
	fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", p.Name)
	b.WriteString("}\n\n")

	// ── Step Functions state machine (the orchestrator) ───────────────────────
	// The ASL is the pyx-pipeline-ir compiler output. Reference it from a variable
	// when not supplied inline so this component never forks the IR compiler.
	var defExpr string
	if p.HasStateMachineDefinition {
		// The compiled ASL references the runner Lambda via the placeholder
		// ${PyxRunnerLambdaArn} (the pyx-pipeline-ir compiler emits a CFN-style
		// placeholder, not an HCL interpolation). Embed the ASL with ALL ${...}
		// escaped to $${...} so terraform treats them as literals, then substitute
		// the runner placeholder with the real Lambda ARN via replace() at apply
		// time — this is how the dogfood wires the runtime resource INTO the
		// compiler's output without forking the compiler.
		// Emit the ASL as a normal double-quoted HCL string (Go's %q escapes
		// newlines/quotes — valid HCL, and sidesteps heredoc-terminator pitfalls).
		// The placeholder ${...} is pre-escaped to $${...} so HCL treats it as a
		// literal; replace()'s search arg "$${PyxRunnerLambdaArn}" also evaluates to
		// the literal ${PyxRunnerLambdaArn}, so the substitution matches.
		aslLiteral := fmt.Sprintf("%q", escapeHCLInterpolation(p.StateMachineDefinition))
		defExpr = fmt.Sprintf("replace(%s, \"$${PyxRunnerLambdaArn}\", aws_lambda_function.%s_runner.arn)", aslLiteral, n)
	} else {
		fmt.Fprintf(&b, "variable %q {\n  type        = string\n  description = %q\n}\n\n",
			n+"_asl", "Step Functions ASL JSON for pipeline "+p.PipelineName+" (compiled by pyx-pipeline-ir).")
		defExpr = "var." + n + "_asl"
	}
	fmt.Fprintf(&b, "resource \"aws_sfn_state_machine\" \"%s\" {\n", n)
	fmt.Fprintf(&b, "  name       = \"%s\"\n", p.Name)
	fmt.Fprintf(&b, "  role_arn   = aws_iam_role.%s_sfn.arn\n", n)
	fmt.Fprintf(&b, "  definition = %s\n", defExpr)
	fmt.Fprintf(&b, "  tags       = { Name = %q, pyxcloud = \"true\", pipeline = %q }\n", p.Name, p.PipelineName)
	b.WriteString("}\n\n")

	// ── Optional: GitHub OIDC identity + CI role ──────────────────────────────
	if p.GitHubOIDC {
		fmt.Fprintf(&b, "resource \"aws_iam_openid_connect_provider\" \"%s_github\" {\n", n)
		b.WriteString("  url             = \"https://token.actions.githubusercontent.com\"\n")
		b.WriteString("  client_id_list  = [\"sts.amazonaws.com\"]\n")
		b.WriteString("  thumbprint_list = [\"6938fd4d98bab03faadb97b34396831e3780aea1\"]\n")
		fmt.Fprintf(&b, "  tags            = { Name = \"%s-github-oidc\", pyxcloud = \"true\" }\n", p.Name)
		b.WriteString("}\n\n")

		fmt.Fprintf(&b, "resource \"aws_iam_role\" \"%s_github_ci\" {\n", n)
		fmt.Fprintf(&b, "  name = \"%s-github-ci\"\n", p.Name)
		b.WriteString("  assume_role_policy = jsonencode({\n")
		b.WriteString("    Version = \"2012-10-17\"\n")
		b.WriteString("    Statement = [{\n")
		b.WriteString("      Effect = \"Allow\"\n")
		fmt.Fprintf(&b, "      Principal = { Federated = aws_iam_openid_connect_provider.%s_github.arn }\n", n)
		b.WriteString("      Action = \"sts:AssumeRoleWithWebIdentity\"\n")
		b.WriteString("      Condition = {\n")
		b.WriteString("        StringEquals = { \"token.actions.githubusercontent.com:aud\" = \"sts.amazonaws.com\" }\n")
		fmt.Fprintf(&b, "        StringLike   = { \"token.actions.githubusercontent.com:sub\" = \"repo:%s:*\" }\n", p.GitHubOwnerRepo)
		b.WriteString("      }\n")
		b.WriteString("    }]\n")
		b.WriteString("  })\n")
		fmt.Fprintf(&b, "  tags = { Name = \"%s-github-ci\", pyxcloud = \"true\" }\n", p.Name)
		b.WriteString("}\n\n")
		// Allow the CI role to start the pipeline (least-privilege: just this SM).
		fmt.Fprintf(&b, "resource \"aws_iam_role_policy\" \"%s_github_ci_start\" {\n", n)
		fmt.Fprintf(&b, "  name = \"%s-github-ci-start\"\n", p.Name)
		fmt.Fprintf(&b, "  role = aws_iam_role.%s_github_ci.id\n", n)
		b.WriteString("  policy = jsonencode({\n")
		b.WriteString("    Version = \"2012-10-17\"\n")
		b.WriteString("    Statement = [{\n")
		b.WriteString("      Effect   = \"Allow\"\n")
		b.WriteString("      Action   = [\"states:StartExecution\"]\n")
		fmt.Fprintf(&b, "      Resource = [aws_sfn_state_machine.%s.arn]\n", n)
		b.WriteString("    }]\n")
		b.WriteString("  })\n")
		b.WriteString("}\n")
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

// assumeRolePolicy returns the HCL object literal (for jsonencode) of a
// service-principal assume-role trust policy.
func assumeRolePolicy(service string) string {
	return fmt.Sprintf("{\n"+
		"    Version = \"2012-10-17\"\n"+
		"    Statement = [{\n"+
		"      Effect    = \"Allow\"\n"+
		"      Principal = { Service = %q }\n"+
		"      Action    = \"sts:AssumeRole\"\n"+
		"    }]\n"+
		"  }", service)
}

// pclVar returns an HCL expression for a value that is either an inline literal
// (when supplied) or an out-of-band variable (declared here, returned as
// var.<name>). Keeps the control-plane expressible at plan time without baking in
// a build artifact or image that is supplied at apply time.
func pclVar(b *strings.Builder, inline, varName, desc string) string {
	if strings.TrimSpace(inline) != "" {
		return fmt.Sprintf("%q", inline)
	}
	fmt.Fprintf(b, "variable %q {\n  type        = string\n  description = %q\n}\n\n", varName, desc)
	return "var." + varName
}

// escapeHCLInterpolation neutralises terraform string-template interpolation in an
// embedded literal: every `${` becomes `$${` and every `%{` becomes `%%{`, so a
// CFN-style ASL placeholder (e.g. ${PyxRunnerLambdaArn}) is carried verbatim into
// the heredoc instead of being evaluated as an HCL reference.
func escapeHCLInterpolation(s string) string {
	s = strings.ReplaceAll(s, "${", "$${")
	s = strings.ReplaceAll(s, "%{", "%%{")
	return s
}
