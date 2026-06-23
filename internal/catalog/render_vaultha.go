package catalog

import (
	"fmt"
	"strings"
)

// render_vaultha.go renders a VaultHAPlan into provider HCL.
//
//   - AWS: aws_kms_key (+ alias) + aws_secretsmanager_secret — the managed
//     Secrets Manager / KMS pairing being migrated away from. The KMS key is the
//     AWS seal/unseal peer of Transit auto-unseal.
//   - DigitalOcean: the OPERATOR pattern (operator.go). CORE = the official
//     hashicorp/vault Helm chart installed in HA Raft mode (and, when enabled,
//     Transit auto-unseal) as a `helm_release`; the chart also installs the Vault
//     Secrets Operator the EXTRA CRs target. EXTRA = a VaultConnection (how VSO
//     reaches this cluster) + a VaultAuthGlobal default per enabled auth method.

func RenderVaultHAHCL(plan VaultHAPlan) (string, error) {
	switch plan.Provider {
	case ProviderAWS:
		return renderVaultHAAWS(plan), nil
	case ProviderDigitalOcean:
		return renderVaultHADO(plan), nil
	default:
		return "", fmt.Errorf("vault-ha: render unsupported for provider %q", plan.Provider)
	}
}

// renderVaultHAAWS renders the AWS Secrets Manager + KMS peer.
func renderVaultHAAWS(p VaultHAPlan) string {
	name := tfName(p.Name)
	var b strings.Builder

	// KMS key: the AWS-managed key that backs encryption + the seal/unseal peer of
	// Transit auto-unseal. Rotation on, a 30-day deletion window (production-safe).
	fmt.Fprintf(&b, "resource \"aws_kms_key\" %q {\n", name)
	fmt.Fprintf(&b, "  description             = %q\n", "pyxcloud "+p.Name+" KMS key (Secrets Manager + auto-unseal peer)")
	b.WriteString("  enable_key_rotation     = true\n")
	b.WriteString("  deletion_window_in_days = 30\n")
	b.WriteString("  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n\n")

	fmt.Fprintf(&b, "resource \"aws_kms_alias\" %q {\n", name)
	fmt.Fprintf(&b, "  name          = %q\n", "alias/"+p.Name)
	fmt.Fprintf(&b, "  target_key_id = aws_kms_key.%s.key_id\n", name)
	b.WriteString("}\n\n")

	// Secrets Manager secret container (KMS-encrypted). The value is supplied
	// out-of-band, never in state (mirrors the secrets-manager component).
	fmt.Fprintf(&b, "resource \"aws_secretsmanager_secret\" %q {\n", name)
	fmt.Fprintf(&b, "  name       = %q\n", p.Name)
	fmt.Fprintf(&b, "  kms_key_id = aws_kms_key.%s.arn\n", name)
	b.WriteString("  tags = { pyxcloud = \"true\" }\n")
	b.WriteString("}\n")
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// renderVaultHADO renders the DigitalOcean Vault-HA operator-pattern stack:
// CORE = the official Vault Helm chart (HA Raft + Transit auto-unseal); EXTRA =
// VaultConnection + VaultAuthGlobal config CRs.
func renderVaultHADO(p VaultHAPlan) string {
	name := tfName(p.Name)
	clusterData := name + "_cluster"
	vaultRel := name + "_operator"

	// ── CORE: the official Vault Helm chart in HA Raft mode ──
	set := []HelmSet{
		{Name: "server.ha.enabled", Value: "true"},
		{Name: "server.ha.raft.enabled", Value: "true"},
		{Name: "server.ha.replicas", Value: fmt.Sprintf("%d", p.Replicas)},
		// Install the Vault Secrets Operator (controller + CRDs) the EXTRA CRs target.
		{Name: "injector.enabled", Value: "false"},
	}
	if p.TransitUnseal {
		// Transit auto-unseal: the chart writes a `seal "transit"` stanza so peers
		// auto-unseal against the Transit key (no manual unseal after restart). The
		// Transit token/address are supplied out-of-band (env), never in state.
		set = append(set,
			HelmSet{Name: "server.ha.raft.config", Value: vaultRaftConfigWithTransit(p)},
		)
	} else {
		set = append(set,
			HelmSet{Name: "server.ha.raft.config", Value: vaultRaftConfig(p)},
		)
	}
	core := []HelmReleaseSpec{
		{
			TFName:          vaultRel,
			ReleaseName:     p.Name,
			Repository:      vaultChartRepo,
			Chart:           vaultChart,
			Version:         p.ChartVersion,
			Namespace:       p.Namespace,
			CreateNamespace: true,
			Set:             set,
			ClusterDataRef:  clusterData,
		},
	}

	// ── EXTRA: the config CRs the Vault Secrets Operator reconciles ──
	var extra []ManifestCR
	// VaultConnection — how VSO reaches this Vault cluster's in-cluster service.
	extra = append(extra, ManifestCR{
		TFName:    name + "_connection",
		Manifest:  renderVaultConnectionManifest(p),
		DependsOn: []string{"helm_release." + vaultRel},
	})
	// A default VaultAuthGlobal per enabled auth method, so a workload-identity's
	// per-role VaultAuth has a default to inherit.
	for _, m := range p.AuthMethods {
		extra = append(extra, ManifestCR{
			TFName:    name + "_authglobal_" + tfName(m),
			Manifest:  renderVaultAuthGlobalManifest(p, m),
			DependsOn: []string{"helm_release." + vaultRel},
		})
	}

	return renderOperatorComponent(clusterData, p.ClusterName, core, extra)
}

// vaultRaftConfig is the Raft storage stanza (HCL) the chart writes verbatim into
// the Vault config. Integrated storage = the HA backend (no external Consul).
func vaultRaftConfig(p VaultHAPlan) string {
	return strings.Join([]string{
		`ui = true`,
		`listener "tcp" {`,
		`  tls_disable = 1`,
		`  address     = "[::]:8200"`,
		`  cluster_address = "[::]:8201"`,
		`}`,
		`storage "raft" {`,
		`  path = "/vault/data"`,
		`}`,
		`service_registration "kubernetes" {}`,
	}, "\n")
}

// vaultRaftConfigWithTransit adds the Transit auto-unseal seal stanza. The Transit
// address/token come from the environment (out-of-band), never the rendered plan.
func vaultRaftConfigWithTransit(p VaultHAPlan) string {
	seal := strings.Join([]string{
		`seal "transit" {`,
		fmt.Sprintf(`  key_name        = %q`, p.TransitKeyName),
		`  mount_path      = "transit/"`,
		`  # address + token supplied out-of-band via VAULT_TRANSIT_ADDR / VAULT_TRANSIT_TOKEN`,
		`}`,
	}, "\n")
	return vaultRaftConfig(p) + "\n" + seal
}

// renderVaultConnectionManifest renders the VaultConnection CR (the Vault Secrets
// Operator API) pointing VSO at this cluster's in-cluster Vault service.
func renderVaultConnectionManifest(p VaultHAPlan) string {
	addr := fmt.Sprintf("http://%s.%s.svc.cluster.local:8200", p.Name, p.Namespace)
	var b strings.Builder
	b.WriteString("  manifest = {\n")
	b.WriteString("    apiVersion = \"secrets.hashicorp.com/v1beta1\"\n")
	b.WriteString("    kind       = \"VaultConnection\"\n")
	b.WriteString("    metadata = {\n")
	fmt.Fprintf(&b, "      name      = %q\n", p.Name+"-connection")
	fmt.Fprintf(&b, "      namespace = %q\n", p.Namespace)
	b.WriteString("      labels    = { app = \"pyx-vault-ha\", pyxcloud = \"true\" }\n")
	b.WriteString("    }\n")
	b.WriteString("    spec = {\n")
	fmt.Fprintf(&b, "      address = %q\n", addr)
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	return b.String()
}

// renderVaultAuthGlobalManifest renders a default VaultAuthGlobal CR for one auth
// method, so per-workload VaultAuth roles inherit a default mount/connection.
func renderVaultAuthGlobalManifest(p VaultHAPlan, method string) string {
	mount := vaultDefaultMount
	if method == WIDeliveryKubernetes {
		mount = vaultK8sMount
	}
	var b strings.Builder
	b.WriteString("  manifest = {\n")
	b.WriteString("    apiVersion = \"secrets.hashicorp.com/v1beta1\"\n")
	b.WriteString("    kind       = \"VaultAuthGlobal\"\n")
	b.WriteString("    metadata = {\n")
	fmt.Fprintf(&b, "      name      = %q\n", p.Name+"-auth-"+method)
	fmt.Fprintf(&b, "      namespace = %q\n", p.Namespace)
	b.WriteString("      labels    = { app = \"pyx-vault-ha\", pyxcloud = \"true\" }\n")
	b.WriteString("    }\n")
	b.WriteString("    spec = {\n")
	b.WriteString("      defaultVaultConnectionRef = {\n")
	fmt.Fprintf(&b, "        name = %q\n", p.Name+"-connection")
	b.WriteString("      }\n")
	fmt.Fprintf(&b, "      defaultAuthMethod = %q\n", method)
	fmt.Fprintf(&b, "      %s = {\n", method)
	fmt.Fprintf(&b, "        mount = %q\n", mount)
	b.WriteString("      }\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	return b.String()
}
