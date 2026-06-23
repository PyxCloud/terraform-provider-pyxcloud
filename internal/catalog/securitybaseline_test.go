package catalog

import (
	"context"
	"strings"
	"testing"
)

// TestDeriveSecurityBaselineEgress asserts the baseline derives least-privilege
// egress only when the topology places compute AND exposes ingress, and that the
// egress set is exactly DNS(53 tcp+udp) + HTTPS(443) + NTP(123 udp).
func TestDeriveSecurityBaselineEgress(t *testing.T) {
	t.Parallel()

	// Compute + ingress -> egress baseline derived.
	in := AssembleInput{
		Name: "env", Provider: "aws", Region: "Dublin",
		Expose: []int{443},
		Components: []AssembleComponent{
			{Name: "web", Type: "virtual-machine-scale-group", ScaleGroup: &AssembleScaleGroup{CPU: "2", RAM: "4", Min: 1, Max: 1, Desired: 1}},
		},
	}
	b := DeriveSecurityBaseline(in)
	if len(b.EgressRules) != 4 {
		t.Fatalf("want 4 baseline egress rules (dns tcp+udp, https, ntp), got %d: %+v", len(b.EgressRules), b.EgressRules)
	}
	gotPorts := map[string]bool{}
	for _, r := range b.EgressRules {
		if r.Direction != DirEgress {
			t.Errorf("baseline rule not egress: %+v", r)
		}
		gotPorts[r.Protocol+"/"+itoaPort(r.FromPort)] = true
	}
	for _, want := range []string{"tcp/53", "udp/53", "tcp/443", "udp/123"} {
		if !gotPorts[want] {
			t.Errorf("baseline egress missing %s; got %v", want, gotPorts)
		}
	}

	// No ingress exposed -> no SG emitted -> no egress baseline.
	noIngress := in
	noIngress.Expose = nil
	if got := DeriveSecurityBaseline(noIngress); len(got.EgressRules) != 0 {
		t.Errorf("no ingress should derive no egress baseline, got %d", len(got.EgressRules))
	}

	// No compute -> no SG -> no egress baseline (storage-only env).
	storageOnly := AssembleInput{
		Name: "env", Provider: "aws", Region: "Dublin", Expose: []int{443},
		Components: []AssembleComponent{{Name: "b", Type: "object-storage"}},
	}
	if got := DeriveSecurityBaseline(storageOnly); len(got.EgressRules) != 0 {
		t.Errorf("storage-only env should derive no egress baseline, got %d", len(got.EgressRules))
	}
}

// TestDeriveSecurityBaselineSecrets asserts the baseline sets the production-safe
// secrets default (force-destroy=false, keep recovery window) when secrets exist.
func TestDeriveSecurityBaselineSecrets(t *testing.T) {
	t.Parallel()
	in := AssembleInput{
		Name: "env", Provider: "aws", Region: "Dublin",
		Components: []AssembleComponent{{Name: "s", Type: "secrets-manager"}},
	}
	b := DeriveSecurityBaseline(in)
	if b.SecretsForceDestroy == nil || *b.SecretsForceDestroy != false {
		t.Errorf("secrets baseline want force_destroy=false (recoverable), got %v", b.SecretsForceDestroy)
	}
	// No secrets -> no secrets default.
	noSec := AssembleInput{Name: "env", Provider: "aws", Region: "Dublin",
		Components: []AssembleComponent{{Name: "b", Type: "object-storage"}}}
	if got := DeriveSecurityBaseline(noSec); got.SecretsForceDestroy != nil {
		t.Errorf("no secrets should derive no secrets default, got %v", got.SecretsForceDestroy)
	}
}

// TestAssembleHCLSecurityBaselineEgressLockdown is the plan-only proof that the
// baseline, when wired into the assembler, REPLACES the allow-all egress on the
// environment SG with the least-privilege set — and that without the opt-in the
// allow-all default is unchanged (backward compatible).
func TestAssembleHCLSecurityBaselineEgressLockdown(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	mk := func(baseline bool) string {
		docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
			Name: "env", Provider: "aws", Region: "Dublin",
			Expose:                []int{443},
			ApplySecurityBaseline: baseline,
			Components: []AssembleComponent{
				{Name: "web", Type: "virtual-machine-scale-group", ScaleGroup: &AssembleScaleGroup{
					Architecture: "x86_64", CPU: "2", RAM: "4", OS: "ubuntu", Min: 1, Max: 1, Desired: 1,
				}},
			},
		})
		if err != nil {
			t.Fatalf("AssembleHCL baseline=%v: %v", baseline, err)
		}
		return strings.Join(docs, "\n")
	}

	// Without the baseline: the allow-all egress default is present.
	off := mk(false)
	if !strings.Contains(off, `from_port         = 0`) {
		t.Errorf("baseline-off env should keep the allow-all egress (from_port 0):\n%s", off)
	}

	// With the baseline: least-privilege egress (443/53/123), and NO allow-all
	// egress (all-protocol port-0 rule) remains.
	on := mk(true)
	if strings.Contains(on, `protocol          = "-1"`) {
		t.Errorf("baseline-on env must not keep an allow-all (-1) egress rule:\n%s", on)
	}
	for _, want := range []string{`from_port         = 443`, `from_port         = 53`, `from_port         = 123`} {
		if !strings.Contains(on, want) {
			t.Errorf("baseline-on env missing least-priv egress %q:\n%s", want, on)
		}
	}
	// The exposed ingress (443) is still open — the baseline never narrows ingress.
	if !strings.Contains(on, `to_port           = 443`) {
		t.Errorf("baseline must not drop the exposed 443 ingress:\n%s", on)
	}
}

// TestAssembleHCLSecurityBaselineSecretsRecoverable proves the secrets baseline
// keeps the recovery window (no force-destroy) when assembled with the baseline.
func TestAssembleHCLSecurityBaselineSecretsRecoverable(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "env", Provider: "aws", Region: "Dublin",
		ApplySecurityBaseline: true,
		Components:            []AssembleComponent{{Name: "appsecret", Type: "secrets-manager"}},
	})
	if err != nil {
		t.Fatalf("AssembleHCL secrets baseline: %v", err)
	}
	all := strings.Join(docs, "\n")
	if !strings.Contains(all, `resource "aws_secretsmanager_secret"`) {
		t.Fatalf("expected a secrets-manager resource:\n%s", all)
	}
	// Production-safe: the force-delete escape hatch (recovery_window_in_days = 0)
	// must NOT be emitted, so the provider's default recovery window is kept.
	if strings.Contains(all, "recovery_window_in_days = 0") {
		t.Errorf("security baseline must keep the secret recovery window (no force-destroy):\n%s", all)
	}
}

func itoaPort(p int) string {
	switch p {
	case 53:
		return "53"
	case 123:
		return "123"
	case 443:
		return "443"
	default:
		return "?"
	}
}
