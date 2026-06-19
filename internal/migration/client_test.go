package migration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/migration/runtime"
)

func TestRequestPlanRedactsBackendErrorBody(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":           "failed to produce migration plan",
			"token":           "secret-token-value",
			"secretAccessKey": "AKIA-not-real",
			"nested": map[string]any{
				"detail": "Authorization: Bearer should-not-leak",
			},
		})
	}))
	defer srv.Close()

	client := NewClient(Config{Endpoint: srv.URL, HTTPClient: srv.Client()})
	_, err := client.RequestPlan(context.Background(), PlanInput{
		Place: "web", SourceProvider: "aws", TargetProvider: "gcp",
	}, [32]byte{1}, runtime.Evidence{Measurement: []byte("m"), Nonce: []byte("n")})
	if err == nil {
		t.Fatal("expected backend error")
	}
	got := err.Error()
	for _, forbidden := range []string{"secret-token-value", "AKIA-not-real", "should-not-leak", "Bearer"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("backend error leaked %q in %q", forbidden, got)
		}
	}
	if !strings.Contains(got, "redacted") {
		t.Fatalf("expected redacted marker, got %q", got)
	}
}
