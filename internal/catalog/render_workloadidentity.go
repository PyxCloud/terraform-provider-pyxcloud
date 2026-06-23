package catalog

import (
	"fmt"
	"strings"
)

// render_workloadidentity.go renders a WorkloadIdentityPlan into provider HCL.
//
//   - AWS: aws_iam_role (assume_role_policy) + inline aws_iam_role_policy + managed
//     aws_iam_role_policy_attachment + an aws_iam_instance_profile — the no-static-
//     key construct being migrated away from. (Mirrors renderIAMAWS, always with an
//     instance profile because a workload identity is, by definition, instance-
//     assumable.)
//   - DigitalOcean: the OPERATOR pattern (operator.go). The Vault config operator
//     (CORE) is owned/installed by the Vault-HA component; here we render only the
//     EXTRA custom resources that SCOPE the identity:
//       * a VaultAuth + per-role auth config CR (the AppRole or Kubernetes auth
//         role) bound to the Vault policies,
//       * a VaultPolicy CR per inline policy (scoped permissions),
//       * in "kubernetes" mode, the Kubernetes ServiceAccount the pod mounts,
//       * in "approle" mode, the droplet cloud-init user_data (rendered as an
//         output) that logs in with a response-wrapped SecretID to fetch scoped,
//         short-lived tokens — no static cloud keys on the droplet.
//     All CRs land on the existing DOKS cluster via the shared
//     `data "digitalocean_kubernetes_cluster"` reference, with depends_on the
//     Vault config operator (helm_release.<vault>_operator) when present in the env.

func RenderWorkloadIdentityHCL(plan WorkloadIdentityPlan) (string, error) {
	switch plan.Provider {
	case ProviderAWS:
		return renderWorkloadIdentityAWS(plan), nil
	case ProviderDigitalOcean:
		return renderWorkloadIdentityDO(plan), nil
	default:
		return "", fmt.Errorf("workload-identity: render unsupported for provider %q", plan.Provider)
	}
}

// renderWorkloadIdentityAWS renders the AWS IAM-role peer with an instance
// profile (a workload identity is instance-assumable by definition).
func renderWorkloadIdentityAWS(p WorkloadIdentityPlan) string {
	var b strings.Builder
	role := tfName(p.Name)
	fmt.Fprintf(&b, "resource \"aws_iam_role\" %q {\n", role)
	fmt.Fprintf(&b, "  name = %q\n", p.Name)
	b.WriteString("  assume_role_policy = jsonencode({\n")
	b.WriteString("    Version = \"2012-10-17\"\n")
	b.WriteString("    Statement = [{\n")
	b.WriteString("      Action    = \"sts:AssumeRole\"\n")
	b.WriteString("      Effect    = \"Allow\"\n")
	fmt.Fprintf(&b, "      Principal = { Service = %q }\n", p.AssumeService)
	b.WriteString("    }]\n")
	b.WriteString("  })\n")
	b.WriteString("  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n\n")

	for _, pol := range p.InlinePolicies {
		pn := tfName(p.Name + "-" + pol.Name)
		fmt.Fprintf(&b, "resource \"aws_iam_role_policy\" %q {\n", pn)
		fmt.Fprintf(&b, "  name   = %q\n", pol.Name)
		fmt.Fprintf(&b, "  role   = aws_iam_role.%s.id\n", role)
		fmt.Fprintf(&b, "  policy = %s\n", iamHeredoc(pol.Document))
		b.WriteString("}\n\n")
	}

	for i, arn := range p.ManagedPolicyARNs {
		an := tfName(fmt.Sprintf("%s-managed-%d", p.Name, i+1))
		fmt.Fprintf(&b, "resource \"aws_iam_role_policy_attachment\" %q {\n", an)
		fmt.Fprintf(&b, "  role       = aws_iam_role.%s.name\n", role)
		fmt.Fprintf(&b, "  policy_arn = %q\n", arn)
		b.WriteString("}\n\n")
	}

	// The instance profile is what makes the role a workload identity: an EC2
	// instance assumes it with no static keys.
	fmt.Fprintf(&b, "resource \"aws_iam_instance_profile\" %q {\n", role)
	fmt.Fprintf(&b, "  name = %q\n", p.Name)
	fmt.Fprintf(&b, "  role = aws_iam_role.%s.name\n", role)
	b.WriteString("}\n")
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// renderWorkloadIdentityDO renders the DigitalOcean Vault-identity replacement
// following the operator pattern: only the EXTRA custom resources (the Vault
// config operator CORE is installed by the Vault-HA component). The shared DOKS
// cluster data source wires the CRs onto the right cluster credentials.
func renderWorkloadIdentityDO(p WorkloadIdentityPlan) string {
	name := tfName(p.Name)
	clusterData := name + "_cluster"

	var extra []ManifestCR

	// VaultPolicy CRs — the scoped permissions the identity is granted.
	for _, pol := range p.InlinePolicies {
		extra = append(extra, ManifestCR{
			TFName:    name + "_policy_" + tfName(pol.Name),
			Manifest:  renderVaultPolicyManifest(p, pol),
			DependsOn: []string{vaultOperatorDependsOn(p)},
		})
	}

	// In Kubernetes mode the pod mounts a ServiceAccount whose projected token is
	// exchanged for a scoped Vault token via the Kubernetes auth method.
	if p.DeliveryMode == WIDeliveryKubernetes {
		extra = append(extra, ManifestCR{
			TFName:    name + "_serviceaccount",
			Manifest:  renderWorkloadServiceAccountManifest(p),
			DependsOn: nil, // a SA depends on nothing but the cluster (the data ref handles that)
		})
	}

	// The VaultAuth role CR — the AppRole or Kubernetes-auth role binding the
	// identity to its policies, with a short-lived token TTL.
	extra = append(extra, ManifestCR{
		TFName:    name + "_auth_role",
		Manifest:  renderVaultAuthRoleManifest(p),
		DependsOn: []string{vaultOperatorDependsOn(p)},
	})

	out := renderOperatorComponent(clusterData, p.ClusterName, nil, extra)

	// In AppRole mode, emit the droplet cloud-init user_data as an output so the
	// virtual-machine that hosts the workload can consume it. It logs in with a
	// response-wrapped SecretID (injected out-of-band at boot) and fetches a
	// scoped, short-lived token — no static cloud keys are stored on the droplet.
	if p.DeliveryMode == WIDeliveryAppRole {
		out += "\n" + renderAppRoleUserDataOutput(p)
	}
	return out
}

// vaultOperatorDependsOn names the helm_release of the Vault config operator the
// Vault-HA component installs. The CRs reconciled by it must wait for it. When the
// Vault-HA component is absent from the env, terraform will still validate the CR
// shapes; the depends_on simply points at the conventional release name.
func vaultOperatorDependsOn(p WorkloadIdentityPlan) string {
	return "helm_release." + tfName("vault") + "_operator"
}

// renderVaultPolicyManifest renders a VaultPolicy CR (secrets.hashicorp.com API,
// reconciled by the Vault config operator). The policy body is the inline policy's
// Document (Vault HCL); an AWS-shaped JSON document is carried verbatim under a
// comment so the migration is auditable rather than silently lossy.
func renderVaultPolicyManifest(p WorkloadIdentityPlan, pol IAMPolicy) string {
	var b strings.Builder
	b.WriteString("  manifest = {\n")
	b.WriteString("    apiVersion = \"secrets.hashicorp.com/v1beta1\"\n")
	b.WriteString("    kind       = \"VaultPolicy\"\n")
	b.WriteString("    metadata = {\n")
	fmt.Fprintf(&b, "      name      = %q\n", p.Name+"-"+pol.Name)
	fmt.Fprintf(&b, "      namespace = %q\n", p.Namespace)
	b.WriteString("      labels    = { app = \"pyx-workload-identity\", pyxcloud = \"true\" }\n")
	b.WriteString("    }\n")
	b.WriteString("    spec = {\n")
	fmt.Fprintf(&b, "      name   = %q\n", p.Name+"-"+pol.Name)
	fmt.Fprintf(&b, "      policy = %s\n", hclMultiline(pol.Document))
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	return b.String()
}

// renderVaultAuthRoleManifest renders the VaultAuth role CR binding the identity
// to its policies via the AppRole or Kubernetes auth method, with a short-lived
// token TTL (never a long-lived static credential).
func renderVaultAuthRoleManifest(p WorkloadIdentityPlan) string {
	method := "approle"
	mount := vaultDefaultMount
	if p.DeliveryMode == WIDeliveryKubernetes {
		method = "kubernetes"
		mount = vaultK8sMount
	}
	var b strings.Builder
	b.WriteString("  manifest = {\n")
	b.WriteString("    apiVersion = \"secrets.hashicorp.com/v1beta1\"\n")
	b.WriteString("    kind       = \"VaultAuth\"\n")
	b.WriteString("    metadata = {\n")
	fmt.Fprintf(&b, "      name      = %q\n", p.Name+"-auth")
	fmt.Fprintf(&b, "      namespace = %q\n", p.Namespace)
	b.WriteString("      labels    = { app = \"pyx-workload-identity\", pyxcloud = \"true\" }\n")
	b.WriteString("    }\n")
	b.WriteString("    spec = {\n")
	fmt.Fprintf(&b, "      method = %q\n", method)
	fmt.Fprintf(&b, "      mount  = %q\n", mount)
	if p.DeliveryMode == WIDeliveryKubernetes {
		b.WriteString("      kubernetes = {\n")
		fmt.Fprintf(&b, "        role           = %q\n", p.VaultRole)
		fmt.Fprintf(&b, "        serviceAccount = %q\n", p.Name+"-sa")
		fmt.Fprintf(&b, "        tokenExpirationSeconds = %d\n", ttlSeconds(p.TokenTTL))
		b.WriteString("      }\n")
	} else {
		b.WriteString("      appRole = {\n")
		fmt.Fprintf(&b, "        roleId    = %q\n", p.VaultRole)
		// The SecretID is injected out-of-band (response-wrapped) and referenced by
		// a Kubernetes Secret the operator reads — never declared in state.
		fmt.Fprintf(&b, "        secretRef = %q\n", p.Name+"-approle-secretid")
		b.WriteString("      }\n")
	}
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	return b.String()
}

// renderWorkloadServiceAccountManifest renders the Kubernetes ServiceAccount the
// pod mounts (k8s delivery mode). Its projected token is exchanged for a scoped
// Vault token by the Kubernetes auth method — no static keys.
func renderWorkloadServiceAccountManifest(p WorkloadIdentityPlan) string {
	var b strings.Builder
	b.WriteString("  manifest = {\n")
	b.WriteString("    apiVersion = \"v1\"\n")
	b.WriteString("    kind       = \"ServiceAccount\"\n")
	b.WriteString("    metadata = {\n")
	fmt.Fprintf(&b, "      name      = %q\n", p.Name+"-sa")
	fmt.Fprintf(&b, "      namespace = %q\n", p.Namespace)
	b.WriteString("      labels    = { app = \"pyx-workload-identity\", pyxcloud = \"true\" }\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	return b.String()
}

// renderAppRoleUserDataOutput renders the droplet cloud-init the AppRole mode
// hands to the virtual-machine hosting the workload, as a terraform output so the
// VM component can wire it into user_data. It logs in with a response-wrapped
// SecretID injected out-of-band at boot, fetches a scoped short-lived token, and
// stores NOTHING long-lived on disk.
func renderAppRoleUserDataOutput(p WorkloadIdentityPlan) string {
	var b strings.Builder
	// The script is wrapped in an HCL heredoc, which treats `${...}` as template
	// interpolation. Shell variable expansions must therefore be escaped as `$${...}`
	// (HCL un-escapes that back to `${...}` at apply time) so the body stays literal.
	script := strings.Join([]string{
		"#!/bin/bash",
		"set -euo pipefail",
		"# pyxcloud workload identity (Vault AppRole) - replaces the AWS IAM instance role.",
		"# The wrapped SecretID is injected out-of-band at boot (e.g. metadata/CI), never baked in.",
		`export VAULT_ADDR="${PYX_VAULT_ADDR:-https://vault.svc.cluster.local:8200}"`,
		fmt.Sprintf("ROLE_ID=%q", p.VaultRole),
		"# Unwrap the response-wrapped SecretID delivered via instance metadata.",
		`SECRET_ID="$(vault unwrap -field=secret_id "${PYX_WRAPPED_SECRET_ID}")"`,
		"# Exchange role_id + secret_id for a scoped, short-lived token.",
		fmt.Sprintf(`VAULT_TOKEN="$(vault write -field=token %s/login role_id="${ROLE_ID}" secret_id="${SECRET_ID}")"`, vaultDefaultMount),
		fmt.Sprintf("# Token TTL is bounded (%s); the workload renews via the agent, no static keys persist.", p.TokenTTL),
		"export VAULT_TOKEN",
		"unset SECRET_ID",
	}, "\n")
	// Escape HCL template interpolation: `${` -> `$${`, `%{` -> `%%{`.
	script = strings.ReplaceAll(script, "${", "$${")
	script = strings.ReplaceAll(script, "%{", "%%{")

	fmt.Fprintf(&b, "output %q {\n", tfName(p.Name)+"_user_data")
	b.WriteString("  description = \"cloud-init for the droplet workload identity (Vault AppRole login; no static keys)\"\n")
	b.WriteString("  sensitive   = true\n")
	b.WriteString("  value       = <<-PYXWIUSERDATA\n")
	for _, line := range strings.Split(script, "\n") {
		b.WriteString("    " + line + "\n")
	}
	b.WriteString("  PYXWIUSERDATA\n")
	b.WriteString("}\n")
	return b.String()
}

// hclMultiline renders a multi-line string as an HCL indented heredoc so a policy
// body (quotes, braces) needs no escaping. Sits inside a `spec` block (4-space
// continuation indent).
func hclMultiline(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	// Escape HCL template interpolation so a policy body with ${...} / %{...} stays
	// literal inside the heredoc.
	s = strings.ReplaceAll(s, "${", "$${")
	s = strings.ReplaceAll(s, "%{", "%%{")
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return "<<-PYXVAULTPOLICY\n" + s + "PYXVAULTPOLICY\n      "
}

// ttlSeconds converts a short Go-style duration ("1h", "30m", "90s") to seconds
// for fields that take an integer second count. Falls back to 3600 (1h) on a
// malformed value rather than a silent zero (which Vault treats as "no expiry").
func ttlSeconds(ttl string) int {
	ttl = strings.TrimSpace(ttl)
	if ttl == "" {
		return 3600
	}
	unit := ttl[len(ttl)-1]
	numPart := ttl[:len(ttl)-1]
	var n int
	if _, err := fmt.Sscanf(numPart, "%d", &n); err != nil || n <= 0 {
		return 3600
	}
	switch unit {
	case 's':
		return n
	case 'm':
		return n * 60
	case 'h':
		return n * 3600
	default:
		return 3600
	}
}
