// Package licence is the model licence gate: core.LicenceService over a
// repository of acceptances. A model whose catalogue licence requires
// acceptance (catalog.Licence.RequiresAcceptance: non-commercial weights
// such as OmniVoice's CC BY-NC 4.0) cannot be loaded, generated with or
// downloaded until the user accepted that licence, for that model or for
// every model under it. Repos outside the catalogue pass: the user brought
// them and their terms.
//
// The repository is store.Licences once the SQLite store lands (thread A);
// until then DocRepo keeps the acceptances in a JSON document.
//
// Thread C owns this package. Imports: core, catalog.
package licence

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/core"
)

// Repo stores acceptances; store.Licences satisfies it.
type Repo interface {
	Put(ctx context.Context, a core.LicenceAcceptance) error
	Delete(ctx context.Context, licence, model string) error
	List(ctx context.Context) ([]core.LicenceAcceptance, error)
}

// Gate implements core.LicenceService.
type Gate struct {
	repo    Repo
	catalog func() *catalog.Catalog
	bus     *core.Bus
	now     func() time.Time
}

var _ core.LicenceService = (*Gate)(nil)

// New returns a gate over repo for the entries of cat (nil = the built-in
// catalogue), publishing LicenceChanged on bus (may be nil).
func New(repo Repo, cat *catalog.Catalog, bus *core.Bus) *Gate {
	if cat == nil {
		cat = catalog.Builtin()
	}
	return &Gate{repo: repo, catalog: func() *catalog.Catalog { return cat }, bus: bus, now: time.Now}
}

// Entry finds the catalogue entry of model, a repo id or an entry id.
func (g *Gate) Entry(model string) (catalog.Entry, bool) {
	c := g.catalog()
	if e, ok := c.ByRepo(model); ok {
		return e, true
	}
	return c.ByID(model)
}

// Required returns the error Check returns for e when nothing is accepted
// for it, or nil when e's licence needs no acceptance.
func Required(e catalog.Entry) *core.LicenceRequiredError {
	if !e.Licence.RequiresAcceptance {
		return nil
	}
	return &core.LicenceRequiredError{Model: e.Repo, Licence: e.Licence.ID, Name: e.Licence.Name, URL: e.Licence.URL}
}

// Check returns nil when model may be used, else a *LicenceRequiredError.
func (g *Gate) Check(ctx context.Context, model string) error {
	e, ok := g.Entry(model)
	if !ok {
		return nil
	}
	req := Required(e)
	if req == nil {
		return nil
	}
	list, err := g.repo.List(ctx)
	if err != nil {
		return fmt.Errorf("licences: %w", err)
	}
	if Covers(list, e) {
		return nil
	}
	return req
}

// Covers reports whether one of list accepts e's licence for e.
func Covers(list []core.LicenceAcceptance, e catalog.Entry) bool {
	return slices.ContainsFunc(list, func(a core.LicenceAcceptance) bool {
		return a.Licence == e.Licence.ID && (a.Model == "" || a.Model == e.Repo)
	})
}

// Accept records a; AcceptedAt defaults to now.
func (g *Gate) Accept(ctx context.Context, a core.LicenceAcceptance) error {
	if a.Licence == "" {
		return fmt.Errorf("licence acceptance without a licence: %w", core.ErrInvalid)
	}
	if a.AcceptedAt.IsZero() {
		a.AcceptedAt = g.now()
	}
	if err := g.repo.Put(ctx, a); err != nil {
		return fmt.Errorf("licences: %w", err)
	}
	g.bus.Publish(core.LicenceChanged{Acceptance: a, Accepted: true})
	return nil
}

// Revoke deletes the acceptance of licence for model ("" = the one for
// every model).
func (g *Gate) Revoke(ctx context.Context, licence, model string) error {
	if err := g.repo.Delete(ctx, licence, model); err != nil {
		return fmt.Errorf("licences: %w", err)
	}
	g.bus.Publish(core.LicenceChanged{Acceptance: core.LicenceAcceptance{Licence: licence, Model: model}})
	return nil
}

// List returns the acceptances on record.
func (g *Gate) List(ctx context.Context) ([]core.LicenceAcceptance, error) {
	return g.repo.List(ctx)
}

// Documents is a key → JSON store; store.Documents and
// settings.DirDocuments satisfy it.
type Documents interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Put(ctx context.Context, key string, doc []byte) error
}

// DocKey is the document DocRepo keeps the acceptances in.
const DocKey = "licences:accepted"

// DocRepo is a Repo in one JSON document (an array of acceptances). Docs
// nil keeps them in memory.
type DocRepo struct {
	Docs Documents

	mu  sync.Mutex
	mem []core.LicenceAcceptance
}

func (r *DocRepo) load(ctx context.Context) ([]core.LicenceAcceptance, error) {
	if r.Docs == nil {
		return slices.Clone(r.mem), nil
	}
	data, ok, err := r.Docs.Get(ctx, DocKey)
	if err != nil || !ok {
		return nil, err
	}
	var list []core.LicenceAcceptance
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func (r *DocRepo) save(ctx context.Context, list []core.LicenceAcceptance) error {
	if r.Docs == nil {
		r.mem = list
		return nil
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return r.Docs.Put(ctx, DocKey, data)
}

func (r *DocRepo) Put(ctx context.Context, a core.LicenceAcceptance) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	list, err := r.load(ctx)
	if err != nil {
		return err
	}
	list = slices.DeleteFunc(list, func(x core.LicenceAcceptance) bool { return x.Licence == a.Licence && x.Model == a.Model })
	return r.save(ctx, append(list, a))
}

func (r *DocRepo) Delete(ctx context.Context, licence, model string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	list, err := r.load(ctx)
	if err != nil {
		return err
	}
	n := len(list)
	list = slices.DeleteFunc(list, func(x core.LicenceAcceptance) bool { return x.Licence == licence && x.Model == model })
	if len(list) == n {
		return fmt.Errorf("acceptance of %s for %q: %w", licence, model, core.ErrNotFound)
	}
	return r.save(ctx, list)
}

func (r *DocRepo) List(ctx context.Context) ([]core.LicenceAcceptance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.load(ctx)
}
