package catalog

import (
	"context"
	"fmt"
)

// BackendCatalog is the live-BE implementation of RegionCatalog. It will fetch
// region rows from the PyxCloud backend over HTTP, so the provider resolves
// against the authoritative `region` table rather than an embedded snapshot.
//
// STUB: the transport is not wired yet. The endpoint and bearer auth land with
// the BE work; until then NewBackend falls back to the embedded snapshot so the
// provider is fully functional. The interface boundary means swapping in the
// live transport is a localized change.
//
// TODO(pd-TF-REGION-VPC / BE): implement
//
//	GET {endpoint}/api/catalog/regions?provider={provider}&region_name={name}
//	Authorization: Bearer {PYXCLOUD_TOKEN}   (passobuild realm)
//
// returning the `region` row(s). Prefer a structured response (the RegionRow
// fields) over rendered .tf — §8 open question resolved in favour of a
// structured plan the provider renders. Do NOT deploy the BE as part of this task.
type BackendCatalog struct {
	endpoint string
	token    string
	fallback RegionCatalog
}

var _ RegionCatalog = (*BackendCatalog)(nil)

// NewBackend returns a RegionCatalog that will use the live BE once the
// transport is implemented; for now it delegates to the embedded snapshot.
func NewBackend(endpoint, token string) (*BackendCatalog, error) {
	emb, err := NewEmbedded()
	if err != nil {
		return nil, err
	}
	return &BackendCatalog{endpoint: endpoint, token: token, fallback: emb}, nil
}

// ResolveRegion implements RegionCatalog.
func (b *BackendCatalog) ResolveRegion(ctx context.Context, regionName, provider string) (RegionRow, error) {
	// TODO(pd-TF-REGION-VPC / BE): replace this delegation with a live HTTP call
	// (see type doc). Until the endpoint exists, resolve against the embedded
	// catalog snapshot — same data, no network.
	if b.fallback == nil {
		return RegionRow{}, fmt.Errorf("backend catalog: no transport and no fallback configured")
	}
	return b.fallback.ResolveRegion(ctx, regionName, provider)
}
