package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
)

// tfRunner executes the backend-translated concrete terraform in a work dir,
// inheriting the calling process environment so the cloud providers resolve
// credentials from the standard env-var chain (AWS_ACCESS_KEY_ID / AWS_PROFILE /
// AWS_REGION, GOOGLE_APPLICATION_CREDENTIALS, DIGITALOCEAN_TOKEN, …) — exactly how
// the per-provider terraform scripts authenticate today. This is Mode A of
// DEPLOY-GATE.md: no accountBinding, no backend-side credentials, no raw secrets
// over the API; the cloud's own IAM (via the ambient env) is the authorization.
//
// State lives in workDir (local backend), so the dir must be stable across the
// resource's plan/apply/refresh/destroy lifecycle — the resource keeps it in state.
type tfRunner struct {
	workDir  string
	execPath string
}

type importCandidate struct {
	Address string
	ID      string
}

// newTFRunner locates the terraform binary on PATH and prepares the work dir.
func newTFRunner(workDir string) (*tfRunner, error) {
	execPath, err := exec.LookPath("terraform")
	if err != nil {
		return nil, fmt.Errorf("terraform binary not found on PATH: the pyxcloud_environment resource runs the "+
			"backend-translated terraform locally with your provider env credentials, so a terraform executable is "+
			"required (install it or add it to PATH): %w", err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating work dir %q: %w", workDir, err)
	}
	return &tfRunner{workDir: workDir, execPath: execPath}, nil
}

// writeConfig writes each translated HCL document as NN.tf in the work dir,
// replacing any prior generated files so re-applies reflect the current plan.
func (r *tfRunner) writeConfig(docs []string) error {
	// Clear previously generated .tf files (keep terraform.tfstate and .terraform/).
	entries, _ := os.ReadDir(r.workDir)
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".tf") {
			_ = os.Remove(filepath.Join(r.workDir, e.Name()))
		}
	}
	for i, doc := range docs {
		name := filepath.Join(r.workDir, fmt.Sprintf("pyx_%03d.tf", i))
		if err := os.WriteFile(name, []byte(doc), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", name, err)
		}
	}
	return nil
}

func (r *tfRunner) tf() (*tfexec.Terraform, error) {
	tf, err := tfexec.NewTerraform(r.workDir, r.execPath)
	if err != nil {
		return nil, err
	}
	// tfexec inherits the parent process environment by default, so AWS_* / GOOGLE_* /
	// DIGITALOCEAN_TOKEN flow through to the cloud providers. We do NOT call SetEnv
	// (which would replace, not augment, the env). Surface terraform's own logs.
	tf.SetStdout(os.Stderr)
	tf.SetStderr(os.Stderr)
	return tf, nil
}

// apply writes the config, inits, and applies. Returns the terraform outputs as a
// flat string map (JSON-encoded values).
func (r *tfRunner) apply(ctx context.Context, docs []string) (map[string]string, error) {
	if err := r.writeConfig(docs); err != nil {
		return nil, err
	}
	tf, err := r.tf()
	if err != nil {
		return nil, err
	}
	if err := tf.Init(ctx, tfexec.Upgrade(false)); err != nil {
		return nil, fmt.Errorf("terraform init: %w", err)
	}
	r.adoptExistingAWSResources(ctx, tf, docs)
	if err := tf.Apply(ctx); err != nil {
		return nil, fmt.Errorf("terraform apply: %w", err)
	}
	r.pruneOrphanedSGRules(ctx, docs)
	return r.outputs(ctx, tf)
}

// plan writes the config, inits, and runs plan. It returns (hasChanges, rawPlan, parsedPlan, error).
func (r *tfRunner) plan(ctx context.Context, docs []string) (bool, string, *tfjson.Plan, error) {
	if err := r.writeConfig(docs); err != nil {
		return false, "", nil, err
	}
	tf, err := r.tf()
	if err != nil {
		return false, "", nil, err
	}
	if err := tf.Init(ctx, tfexec.Upgrade(false)); err != nil {
		return false, "", nil, fmt.Errorf("terraform init: %w", err)
	}

	planPath := filepath.Join(r.workDir, "pyx_drift.tfplan")
	defer os.Remove(planPath)

	hasChanges, err := tf.Plan(ctx, tfexec.Out(planPath))
	if err != nil {
		return false, "", nil, fmt.Errorf("terraform plan: %w", err)
	}

	rawPlan, err := tf.ShowPlanFileRaw(ctx, planPath)
	if err != nil {
		return false, "", nil, fmt.Errorf("terraform show raw: %w", err)
	}

	parsedPlan, err := tf.ShowPlanFile(ctx, planPath)
	if err != nil {
		return false, "", nil, fmt.Errorf("terraform show parsed: %w", err)
	}

	return hasChanges, rawPlan, parsedPlan, nil
}

// refresh reads current outputs without changing infrastructure (best-effort).
func (r *tfRunner) refresh(ctx context.Context) (map[string]string, error) {
	tf, err := r.tf()
	if err != nil {
		return nil, err
	}
	// If the work dir was never initialized (e.g. a fresh machine), there is nothing
	// to read; treat as empty rather than erroring the whole Read.
	if _, statErr := os.Stat(filepath.Join(r.workDir, ".terraform")); statErr != nil {
		return map[string]string{}, nil
	}
	return r.outputs(ctx, tf)
}

// destroy tears the environment down.
func (r *tfRunner) destroy(ctx context.Context) error {
	tf, err := r.tf()
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(filepath.Join(r.workDir, ".terraform")); statErr != nil {
		// Never initialized → nothing provisioned.
		return nil
	}
	if err := tf.Init(ctx, tfexec.Upgrade(false)); err != nil {
		return fmt.Errorf("terraform init (destroy): %w", err)
	}
	if err := tf.Destroy(ctx); err != nil {
		return fmt.Errorf("terraform destroy: %w", err)
	}
	return nil
}

func (r *tfRunner) outputs(ctx context.Context, tf *tfexec.Terraform) (map[string]string, error) {
	raw, err := tf.Output(ctx)
	if err != nil {
		return nil, fmt.Errorf("terraform output: %w", err)
	}
	out := make(map[string]string, len(raw))
	for k, meta := range raw {
		// meta.Value is JSON; for a string value strip the quotes, else keep JSON.
		var s string
		if json.Unmarshal(meta.Value, &s) == nil {
			out[k] = s
		} else {
			out[k] = string(meta.Value)
		}
	}
	return out, nil
}

func (r *tfRunner) adoptExistingAWSResources(ctx context.Context, tf *tfexec.Terraform, docs []string) {
	for _, candidate := range discoverAWSImportCandidates(ctx, docs) {
		if candidate.Address == "" || candidate.ID == "" || candidate.ID == "None" {
			continue
		}
		if err := tf.Import(ctx, candidate.Address, candidate.ID); err != nil {
			fmt.Fprintf(os.Stderr, "pyxcloud: skipped terraform import %s %s: %v\n", candidate.Address, candidate.ID, err)
		}
	}
}

func discoverAWSImportCandidates(ctx context.Context, docs []string) []importCandidate {
	var candidates []importCandidate
	seen := map[string]struct{}{}
	add := func(address, id string) {
		id = strings.TrimSpace(id)
		if address == "" || id == "" || id == "None" {
			return
		}
		key := address + "\x00" + id
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, importCandidate{Address: address, ID: id})
	}

	all := strings.Join(docs, "\n")
	for _, m := range resourceNameRE("aws_iam_role").FindAllStringSubmatch(all, -1) {
		add("aws_iam_role."+m[1], m[2])
	}
	for _, m := range resourceNameRE("aws_iam_instance_profile").FindAllStringSubmatch(all, -1) {
		add("aws_iam_instance_profile."+m[1], m[2])
	}
	if accountID := awsOutput(ctx, "sts", "get-caller-identity", "--query", "Account", "--output", "text"); accountID != "" {
		for _, m := range resourceNameRE("aws_iam_policy").FindAllStringSubmatch(all, -1) {
			add("aws_iam_policy."+m[1], "arn:aws:iam::"+accountID+":policy/"+m[2])
		}
	}
	for _, m := range resourceNameRE("aws_cloudwatch_log_group").FindAllStringSubmatch(all, -1) {
		add("aws_cloudwatch_log_group."+m[1], m[2])
	}
	sgIDs := map[string]string{}
	for _, m := range resourceNameRE("aws_security_group").FindAllStringSubmatch(all, -1) {
		if id := awsSecurityGroupID(ctx, m[2]); id != "" {
			sgIDs[m[1]] = id
			add("aws_security_group."+m[1], id)
		}
	}
	for _, candidate := range awsSecurityGroupRuleImportCandidates(all, sgIDs) {
		add(candidate.Address, candidate.ID)
	}
	for _, m := range resourceNameRE("aws_autoscaling_group").FindAllStringSubmatch(all, -1) {
		add("aws_autoscaling_group."+m[1], m[2])
	}
	for _, m := range resourceNameRE("aws_lb_target_group").FindAllStringSubmatch(all, -1) {
		if arn := awsOutput(ctx, "elbv2", "describe-target-groups", "--names", m[2], "--query", "TargetGroups[0].TargetGroupArn", "--output", "text"); arn != "" {
			add("aws_lb_target_group."+m[1], arn)
		}
	}
	for _, m := range iamRolePolicyRE.FindAllStringSubmatch(all, -1) {
		add("aws_iam_role_policy."+m[1], m[3]+":"+m[2])
	}
	for _, m := range iamRolePolicyAttachmentRE.FindAllStringSubmatch(all, -1) {
		add("aws_iam_role_policy_attachment."+m[1], m[2]+"/"+m[3])
	}
	for _, m := range lbListenerRuleRE.FindAllStringSubmatch(all, -1) {
		query := fmt.Sprintf("Rules[?Priority=='%s'].RuleArn | [0]", m[3])
		if arn := awsOutput(ctx, "elbv2", "describe-rules", "--listener-arn", m[2], "--query", query, "--output", "text"); arn != "" {
			add("aws_lb_listener_rule."+m[1], arn)
		}
	}

	return candidates
}

// parsedSGRule is one aws_security_group_rule parsed from the rendered HCL, with its
// owning SG id resolved (via sgIDs) and each source resolved to a concrete token: a
// CIDR string, or "sg:<group-id>" for a source_security_group_id rule (either an
// in-plan aws_security_group.<name>.id or an external "sg-..." literal). Pure — no
// AWS calls — and shared by the import-adoption and orphan-prune paths.
type parsedSGRule struct {
	address  string
	sgName   string
	sgID     string
	ruleType string // ingress | egress
	protocol string // tcp | udp | icmp | -1
	fromPort string
	toPort   string
	sources  []string // CIDRs and/or "sg:<group-id>" tokens
}

func parseSGRules(hcl string, sgIDs map[string]string) []parsedSGRule {
	var out []parsedSGRule
	for _, m := range awsSGRuleRE.FindAllStringSubmatch(hcl, -1) {
		body := m[2]
		pr := parsedSGRule{address: "aws_security_group_rule." + m[1]}
		if sg := awsSGRuleSGRefRE.FindStringSubmatch(body); len(sg) == 2 {
			pr.sgName = sg[1]
			pr.sgID = sgIDs[sg[1]]
		}
		for _, attr := range hclStringAttrRE.FindAllStringSubmatch(body, -1) {
			switch attr[1] {
			case "type":
				pr.ruleType = attr[2]
			case "protocol":
				pr.protocol = attr[2]
			}
		}
		pr.fromPort = hclNumberAttr(body, "from_port")
		pr.toPort = hclNumberAttr(body, "to_port")
		for _, c := range awsSGRuleCIDRRE.FindAllStringSubmatch(body, -1) {
			if len(c) == 2 {
				pr.sources = append(pr.sources, c[1])
			}
		}
		for _, c := range awsSGRuleIPv6CIDRRE.FindAllStringSubmatch(body, -1) {
			if len(c) == 2 {
				pr.sources = append(pr.sources, c[1])
			}
		}
		// source_security_group_id: an in-plan SG (resolve to its id) or an external literal.
		if sg := awsSGRuleSourceSGRefRE.FindStringSubmatch(body); len(sg) == 2 {
			if id := sgIDs[sg[1]]; id != "" {
				pr.sources = append(pr.sources, "sg:"+id)
			}
		} else if lit := awsSGRuleSourceSGLiteralRE.FindStringSubmatch(body); len(lit) == 2 {
			pr.sources = append(pr.sources, "sg:"+lit[1])
		}
		out = append(out, pr)
	}
	return out
}

// awsSGRuleImportID builds the terraform import id for one source of an SG rule:
// "<sg-id>_<type>_<proto>_<from>_<to>_<source>" (the source is a CIDR, or a peer group
// id with the "sg:" prefix stripped) — the aws_security_group_rule import format for
// both CIDR and source-SG rules.
func awsSGRuleImportID(sgID, ruleType, proto, from, to, source string) string {
	if proto == "-1" {
		proto = "all"
	}
	return strings.Join([]string{sgID, ruleType, proto, from, to, strings.TrimPrefix(source, "sg:")}, "_")
}

func awsSecurityGroupRuleImportCandidates(hcl string, sgIDs map[string]string) []importCandidate {
	var candidates []importCandidate
	for _, pr := range parseSGRules(hcl, sgIDs) {
		if pr.sgID == "" || pr.ruleType == "" || pr.protocol == "" || pr.fromPort == "" || pr.toPort == "" || len(pr.sources) == 0 {
			continue
		}
		for _, source := range pr.sources {
			candidates = append(candidates, importCandidate{
				Address: pr.address,
				ID:      awsSGRuleImportID(pr.sgID, pr.ruleType, pr.protocol, pr.fromPort, pr.toPort, source),
			})
		}
	}
	return candidates
}

// sgRuleKey canonicalizes a rule for set comparison between desired (HCL) and actual
// (AWS): "<dir>|<proto>|<from>|<to>|<source>". Ports are dropped for the all/-1
// protocol (AWS reports -1/-1 while the HCL may say 0/0), and the source is a CIDR or
// a bare peer group id ("sg:" stripped).
func sgRuleKey(direction, proto, from, to, source string) string {
	p := strings.ToLower(strings.TrimSpace(proto))
	if p == "-1" || p == "all" {
		p, from, to = "all", "", ""
	}
	return strings.ToLower(strings.TrimSpace(direction)) + "|" + p + "|" + from + "|" + to + "|" +
		strings.ToLower(strings.TrimPrefix(strings.TrimSpace(source), "sg:"))
}

// desiredSGRuleKeys is the set of canonical rule keys the HCL declares for ownerSgID.
func desiredSGRuleKeys(rules []parsedSGRule, ownerSgID string) map[string]bool {
	keys := map[string]bool{}
	for _, pr := range rules {
		if pr.sgID != ownerSgID {
			continue
		}
		for _, source := range pr.sources {
			keys[sgRuleKey(pr.ruleType, pr.protocol, pr.fromPort, pr.toPort, source)] = true
		}
	}
	return keys
}

// actualSGRule is one live ingress/egress rule on an AWS SG.
type actualSGRule struct {
	id     string
	egress bool
	key    string
}

// rulesToRevoke returns the actual rules whose key is absent from the desired set —
// the orphans to prune. Pure; the AWS describe/revoke glue lives in pruneOrphanedSGRules.
func rulesToRevoke(desired map[string]bool, actual []actualSGRule) []actualSGRule {
	var orphans []actualSGRule
	for _, a := range actual {
		if !desired[a.key] {
			orphans = append(orphans, a)
		}
	}
	return orphans
}

// pruneOrphanedSGRules revokes ingress/egress rules that exist in AWS but are absent
// from the desired HCL, for each SG the provider manages. The local nested state is
// empty on a fresh runner, so terraform can only create/adopt what is in the current
// HCL and cannot itself destroy a rule dropped from config — without this prune such a
// rule lingers (e.g. a public 0.0.0.0/0 door left open after a port is moved to an
// ALB-scoped rule). Conservative: an SG whose desired set parses empty is skipped so a
// parse failure can never blindly strip a live SG.
func (r *tfRunner) pruneOrphanedSGRules(ctx context.Context, docs []string) {
	all := strings.Join(docs, "\n")
	sgIDs := map[string]string{}
	for _, m := range resourceNameRE("aws_security_group").FindAllStringSubmatch(all, -1) {
		if id := awsSecurityGroupID(ctx, m[2]); id != "" {
			sgIDs[m[1]] = id
		}
	}
	if len(sgIDs) == 0 {
		return
	}
	rules := parseSGRules(all, sgIDs)
	for _, sgID := range sgIDs {
		desired := desiredSGRuleKeys(rules, sgID)
		if len(desired) == 0 {
			continue // never prune blind on a parse miss
		}
		for _, orphan := range rulesToRevoke(desired, describeSGRules(ctx, sgID)) {
			if err := awsRevokeSGRule(ctx, sgID, orphan.id, orphan.egress); err != nil {
				fmt.Fprintf(os.Stderr, "pyxcloud: skipped revoke of orphaned SG rule %s on %s: %v\n", orphan.id, sgID, err)
			} else {
				fmt.Fprintf(os.Stderr, "pyxcloud: revoked orphaned SG rule %s on %s (not in desired config)\n", orphan.id, sgID)
			}
		}
	}
}

// describeSGRules lists the live ingress+egress rules on an SG, keyed for comparison.
func describeSGRules(ctx context.Context, sgID string) []actualSGRule {
	out := awsOutput(ctx, "ec2", "describe-security-group-rules",
		"--filters", "Name=group-id,Values="+sgID,
		"--query", "SecurityGroupRules[].{id:SecurityGroupRuleId,egress:IsEgress,proto:IpProtocol,from:FromPort,to:ToPort,cidr:CidrIpv4,cidr6:CidrIpv6,peer:ReferencedGroupInfo.GroupId}",
		"--output", "json")
	if out == "" {
		return nil
	}
	var rows []struct {
		ID    string `json:"id"`
		Egr   bool   `json:"egress"`
		Proto string `json:"proto"`
		From  int    `json:"from"`
		To    int    `json:"to"`
		Cidr  string `json:"cidr"`
		Cidr6 string `json:"cidr6"`
		Peer  string `json:"peer"`
	}
	if json.Unmarshal([]byte(out), &rows) != nil {
		return nil
	}
	var actual []actualSGRule
	for _, row := range rows {
		dir := "ingress"
		if row.Egr {
			dir = "egress"
		}
		var source string
		switch {
		case row.Cidr != "":
			source = row.Cidr
		case row.Cidr6 != "":
			source = row.Cidr6
		case row.Peer != "":
			source = "sg:" + row.Peer
		default:
			continue // prefix-list / self rules are not modelled — leave them alone
		}
		actual = append(actual, actualSGRule{
			id:     row.ID,
			egress: row.Egr,
			key:    sgRuleKey(dir, row.Proto, strconv.Itoa(row.From), strconv.Itoa(row.To), source),
		})
	}
	return actual
}

func awsRevokeSGRule(ctx context.Context, sgID, ruleID string, egress bool) error {
	if _, err := exec.LookPath("aws"); err != nil {
		return fmt.Errorf("aws CLI not found")
	}
	api := "revoke-security-group-ingress"
	if egress {
		api = "revoke-security-group-egress"
	}
	cmd := exec.CommandContext(ctx, "aws", "ec2", api, "--group-id", sgID, "--security-group-rule-ids", ruleID)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

func resourceNameRE(resourceType string) *regexp.Regexp {
	return regexp.MustCompile(`(?s)resource\s+"` + regexp.QuoteMeta(resourceType) + `"\s+"([^"]+)"\s+\{.*?\n\s+name\s+=\s+"([^"]+)"`)
}

var (
	iamRolePolicyRE            = regexp.MustCompile(`(?s)resource\s+"aws_iam_role_policy"\s+"([^"]+)"\s+\{.*?\n\s+name\s+=\s+"([^"]+)".*?\n\s+role\s+=\s+aws_iam_role\.([A-Za-z0-9_-]+)\.id`)
	iamRolePolicyAttachmentRE  = regexp.MustCompile(`(?s)resource\s+"aws_iam_role_policy_attachment"\s+"([^"]+)"\s+\{.*?\n\s+role\s+=\s+aws_iam_role\.([A-Za-z0-9_-]+)\.name.*?\n\s+policy_arn\s+=\s+"([^"]+)"`)
	lbListenerRuleRE           = regexp.MustCompile(`(?s)resource\s+"aws_lb_listener_rule"\s+"([^"]+)"\s+\{.*?\n\s+listener_arn\s+=\s+"([^"]+)".*?\n\s+priority\s+=\s+([0-9]+)`)
	awsSGRuleRE                = regexp.MustCompile(`(?s)resource\s+"aws_security_group_rule"\s+"([^"]+)"\s+\{(.*?)\n\}`)
	awsSGRuleSGRefRE           = regexp.MustCompile(`(?m)^\s+security_group_id\s+=\s+aws_security_group\.([A-Za-z0-9_-]+)\.id`)
	awsSGRuleSourceSGRefRE     = regexp.MustCompile(`(?m)^\s+source_security_group_id\s+=\s+aws_security_group\.([A-Za-z0-9_-]+)\.id`)
	awsSGRuleSourceSGLiteralRE = regexp.MustCompile(`(?m)^\s+source_security_group_id\s+=\s+"(sg-[A-Za-z0-9]+)"`)
	awsSGRuleCIDRRE            = regexp.MustCompile(`(?m)^\s+cidr_blocks\s+=\s+\[\s*"([^"]+)"`)
	awsSGRuleIPv6CIDRRE        = regexp.MustCompile(`(?m)^\s+ipv6_cidr_blocks\s+=\s+\[\s*"([^"]+)"`)
	hclStringAttrRE            = regexp.MustCompile(`(?m)^\s+([a-zA-Z_]+)\s+=\s+"([^"]*)"`)
)

func hclNumberAttr(body, name string) string {
	re := regexp.MustCompile(`(?m)^\s+` + regexp.QuoteMeta(name) + `\s+=\s+(-?\d+)`)
	m := re.FindStringSubmatch(body)
	if len(m) != 2 {
		return ""
	}
	return m[1]
}

func awsOutput(ctx context.Context, args ...string) string {
	if _, err := exec.LookPath("aws"); err != nil {
		return ""
	}
	cmd := exec.CommandContext(ctx, "aws", args...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func awsSecurityGroupID(ctx context.Context, groupName string) string {
	if groupName == "" {
		return ""
	}
	return awsOutput(ctx, "ec2", "describe-security-groups",
		"--filters", "Name=group-name,Values="+groupName,
		"--query", "sort_by(SecurityGroups,&VpcId)[0].GroupId",
		"--output", "text")
}
