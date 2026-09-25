// Package engine is DEP-01.2 (POST-EXECUTION-PIPELINE-V1): the PUBLIC facade
// over internal/catalog — the one import surface the pyx-backend's pyxengine
// (DEP-01.3) and any external consumer build against. Type aliases only:
// every type is the internal one, byte for byte; the facade adds no behavior
// and no second vocabulary.
//
// Why a facade: internal/ packages are unimportable across modules. The
// deploy path (spec §8.4) needs the engine AS A LIBRARY: render a C1
// topology to Terraform without forking the renderer. Tag v0.2.0 is the
// version the backend pins.
//
// Adding a field or function here is a breaking change for every consumer:
// extend internal/catalog, then re-expose here, and bump the tag.
package engine

import (
	"context"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/catalog"
	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/tfplanparser"
)

// Catalog is the catalogue interface (archcatalog C1's provider-side twin):
// the kinds, sizes, regions and relations the renderer resolves.
type Catalog = catalog.Catalog

// EmbeddedCatalog is the built-in catalogue returned by NewEmbedded.
type EmbeddedCatalog = catalog.EmbeddedCatalog

// NewEmbedded loads the embedded V1 catalogue (CSV-backed: kinds, sizes,
// regions). The external consumer never builds a Catalog by hand.
var NewEmbedded = catalog.NewEmbedded

// AssembleInput is the render request: one environment's shape (provider,
// region, network, components).
type AssembleInput = catalog.AssembleInput

// AssembleComponent is one component of the environment to render.
type AssembleComponent = catalog.AssembleComponent

// AssembleVM is the VM component payload (the app-on-VM shape F-2 locks).
type AssembleVM = catalog.AssembleVM

// AssembleScaleGroup is the scale-group component shape.
type AssembleScaleGroup = catalog.AssembleScaleGroup

// AssembleMDB is the managed-database component shape.
type AssembleMDB = catalog.AssembleMDB

// SecurityRule is one ingress rule of AssembleInput.
type SecurityRule = catalog.SecurityRule

// AssembleHCL is THE translation entry point: turn the environment into
// concrete terraform documents (one per file, ordered).
var AssembleHCL = catalog.AssembleHCL

// PlanSummary is the parsed plan the executor guards against the sealed
// bundle (the plan-hash pattern); PlanCostSignal is one cost signal row.
type PlanSummary = tfplanparser.PlanSummary

type PlanCostSignal = tfplanparser.CostSignal

// ParsePlanJSON is the plan parser entry point (verbatim alias).
var ParsePlanJSON = tfplanparser.ParsePlanJSON

// Render is the convenience one-liner the backend uses: embedded catalogue +
// assemble in one call. Same guarantees as the two-call form.
func Render(ctx context.Context, in catalog.AssembleInput) ([]string, error) {
	cat, err := NewEmbedded()
	if err != nil {
		return nil, err
	}
	return AssembleHCL(ctx, cat, in)
}