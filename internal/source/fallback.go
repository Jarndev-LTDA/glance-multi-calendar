package source

import "context"

// Warner is an optional interface: a Source that can partially fail (one
// account down, the others fine) reports the details here after Fetch.
type Warner interface {
	Warnings() []string
}

// Fallback serves Alt (typically the demo data) until UsePrimary says the
// real source is ready, e.g. once the first account is connected. The
// decision is made per call, so connecting an account needs no restart.
type Fallback struct {
	Primary    Source
	Alt        Source
	UsePrimary func() bool
}

func (f Fallback) pick() Source {
	if f.UsePrimary() {
		return f.Primary
	}
	return f.Alt
}

// Accounts delegates to the active source.
func (f Fallback) Accounts(ctx context.Context) ([]Account, error) { return f.pick().Accounts(ctx) }

// Fetch delegates to the active source.
func (f Fallback) Fetch(ctx context.Context, w Window) ([]Event, error) {
	return f.pick().Fetch(ctx, w)
}

// Warnings delegates when the active source supports it.
func (f Fallback) Warnings() []string {
	if w, ok := f.pick().(Warner); ok {
		return w.Warnings()
	}
	return nil
}

// Demo reports whether the fallback data is being served.
func (f Fallback) Demo() bool { return !f.UsePrimary() }
