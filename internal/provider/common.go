package provider

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"golang.org/x/sync/errgroup"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

// providerData is shared by every resource and data source of one provider
// instance.
type providerData struct {
	client         *sonarqube.Client
	anonymizeUsers bool
	locks          *keyedMutex
	info           *serverInfoCache
}

// ---------------------------------------------------------------------------
// Server version and edition

type serverInfo struct {
	Version string // e.g. "26.11.0.134175" or "2026.1.0.119033"
	Edition string // community, developer, enterprise, datacenter
}

// serverInfoCache detects the server version and edition once, on first use,
// rather than at provider configuration. Plans that never need it make no
// extra call, and a failed detection is retried rather than cached.
type serverInfoCache struct {
	client          *sonarqube.Client
	versionOverride string
	editionOverride string

	mu   sync.Mutex
	info *serverInfo
}

func (c *serverInfoCache) get(ctx context.Context) (*serverInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.info != nil {
		return c.info, nil
	}
	ver, ed := c.versionOverride, c.editionOverride
	g, gctx := errgroup.WithContext(ctx)
	if ver == "" {
		g.Go(func() error {
			b, _, err := c.client.Server.Version(gctx)
			ver = strings.TrimSpace(string(b))
			return err
		})
	}
	if ed == "" {
		g.Go(func() error {
			nav, _, err := c.client.Navigation.Global(gctx)
			if err == nil {
				ed = strings.ToLower(nav.Edition)
			}
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("detecting SonarQube version and edition (set installed_version and installed_edition to skip detection): %w", err)
	}
	if ed == "" {
		ed = "community"
	}
	c.info = &serverInfo{Version: ver, Edition: ed}
	return c.info, nil
}

var editionRank = map[string]int{"community": 0, "developer": 1, "enterprise": 2, "datacenter": 3}

func (s *serverInfo) hasEdition(minEdition string) bool {
	return editionRank[s.Edition] >= editionRank[minEdition]
}

// requireEdition adds an error unless the server edition is at least minEdition.
// It is meant for plan-time checks, so that an unsupported resource fails
// before anything is changed rather than half-way through an apply.
func (p *providerData) requireEdition(ctx context.Context, diags *diag.Diagnostics, feature, minEdition string) {
	info, err := p.info.get(ctx)
	if err != nil {
		diags.AddWarning("Could not verify SonarQube edition", err.Error())
		return
	}
	if !info.hasEdition(minEdition) {
		diags.AddError(feature+" is not available on this server",
			fmt.Sprintf("%s requires SonarQube %s edition or above; the server runs %s edition %s.", feature, minEdition, info.Edition, info.Version))
	}
}

// ---------------------------------------------------------------------------
// Keyed locks

// keyedMutex serialises read-modify-write sequences on one remote object,
// e.g. the conditions of a quality gate or the settings of a component, when
// several resources of the same apply touch it concurrently.
type keyedMutex struct {
	mu sync.Mutex
	m  map[string]*refMutex
}

type refMutex struct {
	sync.Mutex
	refs int
}

func newKeyedMutex() *keyedMutex { return &keyedMutex{m: map[string]*refMutex{}} }

func (k *keyedMutex) lock(key string) (unlock func()) {
	k.mu.Lock()
	m, ok := k.m[key]
	if !ok {
		m = &refMutex{}
		k.m[key] = m
	}
	m.refs++
	k.mu.Unlock()
	m.Lock()
	return func() {
		m.Unlock()
		k.mu.Lock()
		if m.refs--; m.refs == 0 {
			delete(k.m, key)
		}
		k.mu.Unlock()
	}
}

// ---------------------------------------------------------------------------
// Errors

// isGone reports whether err means the object no longer exists. A 404 for an
// endpoint the server does not have ("Unknown url", e.g. an Enterprise-only
// API on Community Build) is not treated as gone: removing the resource from
// state then would silently forget an object that may still exist.
func isGone(err error) bool {
	return sonarqube.IsNotFound(err) && !isUnknownURL(err)
}

func isUnknownURL(err error) bool {
	e, ok := errors.AsType[*sonarqube.ErrorResponse](err)
	if !ok || e.StatusCode != 404 {
		return false
	}
	for _, m := range e.Messages {
		if strings.HasPrefix(m, "Unknown url") {
			return true
		}
	}
	return false
}

// hasMessage reports whether err is an API error whose message contains s
// (case-insensitive). Some endpoints answer 400 rather than 404 for a missing
// object; callers use this to recognise those precisely.
func hasMessage(err error, s string) bool {
	e, ok := errors.AsType[*sonarqube.ErrorResponse](err)
	if !ok {
		return false
	}
	s = strings.ToLower(s)
	for _, m := range e.Messages {
		if strings.Contains(strings.ToLower(m), s) {
			return true
		}
	}
	return false
}

// apiError adds an error diagnostic with an actionable explanation of err.
func apiError(diags *diag.Diagnostics, summary string, err error) {
	diags.AddError(summary, explain(err))
}

func explain(err error) string {
	e, ok := errors.AsType[*sonarqube.ErrorResponse](err)
	if !ok {
		if errors.Is(err, context.DeadlineExceeded) {
			return err.Error() + "\n\nThe request timed out. Increase the provider's request_timeout if the server is slow."
		}
		return err.Error()
	}
	msg := strings.Join(e.Messages, "; ")
	if msg == "" {
		msg = string(e.Body)
	}
	detail := fmt.Sprintf("%s %s returned HTTP %d", e.Method, e.URL, e.StatusCode)
	if msg != "" {
		detail += ": " + msg
	}
	switch {
	case e.StatusCode == 401:
		detail += "\n\nAuthentication failed. Check the provider's token, or user and pass."
	case e.StatusCode == 403:
		detail += "\n\nThe credentials lack the permission this operation needs (often 'Administer System' or project 'Administer')."
	case isUnknownURL(err):
		detail += "\n\nThe server does not provide this API. It may need a higher edition or a different SonarQube version."
	}
	return detail
}

// alreadyExists recognises SonarQube's various "already exists" messages so
// that a create conflict can suggest an import instead of a bare 400.
func alreadyExists(err error) bool {
	return sonarqube.StatusCode(err) == 400 &&
		(hasMessage(err, "already exist") || hasMessage(err, "similar key already exists"))
}

func createError(diags *diag.Diagnostics, what, importID string, err error) {
	detail := explain(err)
	if alreadyExists(err) && importID != "" {
		detail += fmt.Sprintf("\n\nThe %s already exists in SonarQube. To manage it with Terraform, import it:\n\n  terraform import <address> %q\n\nor add an `import` block with id = %q.", what, importID, importID)
	}
	diags.AddError("Unable to create "+what, detail)
}

// ---------------------------------------------------------------------------
// Read-after-write

// waitFor polls check with backoff until it reports true, for up to the
// window. SonarQube serves several lookups from Elasticsearch, which may not
// show an object for a moment after it was written; polling avoids reporting
// a just-created object as missing.
func waitFor(ctx context.Context, window time.Duration, check func(context.Context) (bool, error)) error {
	ctx, cancel := context.WithTimeout(ctx, window)
	defer cancel()
	interval := 200 * time.Millisecond
	for {
		ok, err := check(ctx)
		if err != nil || ok {
			return err
		}
		jitter := time.Duration(rand.Int64N(int64(interval / 2))) //nolint:gosec // jitter only
		t := time.NewTimer(interval/2 + jitter)
		select {
		case <-ctx.Done():
			t.Stop()
			return errNotVisible
		case <-t.C:
		}
		interval = min(interval*2, 2*time.Second)
	}
}

var errNotVisible = errors.New("the object did not become visible in SonarQube in time")

const consistencyWindow = 30 * time.Second

// ---------------------------------------------------------------------------
// Paging

// maxPages bounds every paged read, so a server misreporting totals cannot
// keep a plan looping.
const maxPages = 200

// collect reads every page of a v1 or v2 search. fetch returns one page's
// items and the reported total.
func collect[T any](ctx context.Context, pageSize int, fetch func(ctx context.Context, page, size int) ([]T, int64, error)) ([]T, error) {
	var all []T
	for page := 1; page <= maxPages; page++ {
		items, total, err := fetch(ctx, page, pageSize)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if len(items) < pageSize || int64(len(all)) >= total {
			return all, nil
		}
	}
	return all, fmt.Errorf("stopped after %d pages of %d items", maxPages, pageSize)
}

func total(p *sonarqube.Paging) int64 {
	if p == nil {
		return 0
	}
	return p.Total
}

// ---------------------------------------------------------------------------
// Concurrency

// forEach runs fn for every item with bounded parallelism and returns the
// first error. Calls still pass through the client's rate limiter.
func forEach[T any](ctx context.Context, items []T, fn func(context.Context, T) error) error {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(4)
	for _, it := range items {
		g.Go(func() error { return fn(gctx, it) })
	}
	return g.Wait()
}

// ---------------------------------------------------------------------------
// Base types

type resourceBase struct{ p *providerData }

func (r *resourceBase) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	pd, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("got %T", req.ProviderData))
		return
	}
	r.p = pd
}

// configured reports whether the provider has been configured; plan-time
// hooks must not call the API when it has not (unknown provider config).
func (r *resourceBase) configured() bool { return r.p != nil }

func (r *resourceBase) c() *sonarqube.Client { return r.p.client }

type dataSourceBase struct{ p *providerData }

func (d *dataSourceBase) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	pd, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("got %T", req.ProviderData))
		return
	}
	d.p = pd
}

func (d *dataSourceBase) c() *sonarqube.Client { return d.p.client }

type ephemeralBase struct{ p *providerData }

func (e *ephemeralBase) Configure(_ context.Context, req ephemeral.ConfigureRequest, resp *ephemeral.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	pd, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("got %T", req.ProviderData))
		return
	}
	e.p = pd
}

// ---------------------------------------------------------------------------
// Import IDs

// splitID splits an import ID into exactly n non-empty parts.
func splitID(id, sep string, n int, format string) ([]string, error) {
	parts := strings.SplitN(id, sep, n)
	if len(parts) != n {
		return nil, fmt.Errorf("expected an import ID of the form %q, got %q", format, id)
	}
	if slices.Contains(parts, "") {
		return nil, fmt.Errorf("expected an import ID of the form %q, got %q", format, id)
	}
	return parts, nil
}
