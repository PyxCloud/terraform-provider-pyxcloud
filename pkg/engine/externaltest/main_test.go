package engineconsumer

import (
	"context"
	"strings"
	"testing"

	"github.com/PyxCloud/terraform-provider-pyxcloud/pkg/engine"
)

// The DEP-01.2 done-when: an EXTERNAL module imports the facade and renders
// a DO environment.
func TestExternalModuleRendersDOEnvironment(t *testing.T) {
	docs, err := engine.Render(context.Background(), engine.AssembleInput{
		Name:     "wp-consumer",
		Provider: "digitalocean",
		Region:   "Frankfurt",
		Components: []engine.AssembleComponent{{
			Name:       "web",
			Type:       "virtual-machine-scale-group",
			Count:      1,
			ScaleGroup: &engine.AssembleScaleGroup{CPU: "2", RAM: "4", Min: 1, Max: 1, Desired: 1},
		}},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(docs) == 0 {
		t.Fatal("render produced no documents")
	}
	joined := strings.Join(docs, "\n")
	if !strings.Contains(joined, "digitalocean") {
		t.Fatalf("render is not a DO environment: %d docs", len(docs))
	}
}
