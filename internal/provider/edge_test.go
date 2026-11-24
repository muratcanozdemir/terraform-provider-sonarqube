package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

// ---------------------------------------------------------------------------
// A fake SonarQube for helpers that call the API.

type fakeCall struct {
	method, path string
	form         url.Values
	query        url.Values
}

type fakeServer struct {
	t     *testing.T
	mu    sync.Mutex
	calls []fakeCall
	// routes maps "METHOD /path" to a handler returning status and body.
	routes map[string]func(r *http.Request) (int, any)
}

func newFakeServer(t *testing.T, routes map[string]func(r *http.Request) (int, any)) (*fakeServer, *sonarqube.Client) {
	t.Helper()
	f := &fakeServer{t: t, routes: routes}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.calls = append(f.calls, fakeCall{method: r.Method, path: r.URL.Path, form: r.PostForm, query: r.URL.Query()})
		f.mu.Unlock()
		h, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprintf(w, `{"errors":[{"msg":"Unknown url : %s"}]}`, r.URL.Path)
			return
		}
		status, body := h(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		switch b := body.(type) {
		case nil:
		case string:
			_, _ = w.Write([]byte(b))
		default:
			_ = json.NewEncoder(w).Encode(b)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := sonarqube.NewClient(srv.URL, sonarqube.WithToken("t"), sonarqube.WithRetryPolicy(sonarqube.RetryPolicy{}), sonarqube.WithRateLimit(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	return f, c
}

func (f *fakeServer) callsTo(method, path string) []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeCall
	for _, c := range f.calls {
		if c.method == method && c.path == path {
			out = append(out, c)
		}
	}
	return out
}

func ok(body any) func(*http.Request) (int, any) {
	return func(*http.Request) (int, any) { return 200, body }
}

// ---------------------------------------------------------------------------
// Paging and concurrency

func TestCollect(t *testing.T) {
	ctx := context.Background()
	pages := func(sizes []int, reported int64) (func(context.Context, int, int) ([]int, int64, error), *int) {
		calls := 0
		return func(_ context.Context, page, _ int) ([]int, int64, error) {
			calls++
			if page > len(sizes) {
				return nil, reported, nil
			}
			return make([]int, sizes[page-1]), reported, nil
		}, &calls
	}
	for _, c := range []struct {
		name      string
		sizes     []int
		reported  int64
		wantItems int
		wantCalls int
	}{
		{"short first page", []int{3}, 3, 3, 1},
		{"empty result", []int{0}, 0, 0, 1},
		{"exact multiple stops on total", []int{10, 10}, 20, 20, 2},
		{"short last page", []int{10, 10, 4}, 24, 24, 3},
		// A total smaller than reality must not loop; a short page ends it anyway.
		{"under-reported total", []int{10, 10, 2}, 5, 10, 1},
		// An over-reported total ends at the first short page.
		{"over-reported total", []int{10, 10, 2}, 1000, 22, 3},
		// No paging information (total 0): read until a short page rather
		// than stopping after a full first page.
		{"no total, several pages", []int{10, 10, 3}, 0, 23, 3},
		{"no total, exact multiple", []int{10, 10}, 0, 20, 3},
	} {
		fetch, calls := pages(c.sizes, c.reported)
		got, err := collect(ctx, 10, fetch)
		if err != nil || len(got) != c.wantItems || *calls != c.wantCalls {
			t.Errorf("%s: %d items after %d calls (err %v), want %d after %d", c.name, len(got), *calls, err, c.wantItems, c.wantCalls)
		}
	}

	// A server that always answers a full page and a growing total is cut off.
	calls := 0
	got, err := collect(ctx, 2, func(_ context.Context, page, size int) ([]int, int64, error) {
		calls++
		return make([]int, size), int64(page*size + 1), nil
	})
	if err == nil || calls != maxPages || len(got) != 2*maxPages {
		t.Errorf("runaway paging: %d calls, %d items, err %v", calls, len(got), err)
	}

	// An error on a later page discards the partial result.
	boom := errors.New("boom")
	got, err = collect(ctx, 1, func(_ context.Context, page, _ int) ([]int, int64, error) {
		if page == 2 {
			return nil, 0, boom
		}
		return []int{page}, 5, nil
	})
	if !errors.Is(err, boom) || got != nil {
		t.Errorf("collect = %v, %v; want nil and the page error", got, err)
	}

	if total(nil) != 0 || total(&sonarqube.Paging{Total: 7}) != 7 {
		t.Error("total")
	}
}

func TestForEach(t *testing.T) {
	ctx := context.Background()
	var running, peak, seen atomic.Int32
	items := make([]int, 20)
	err := forEach(ctx, items, func(context.Context, int) error {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		running.Add(-1)
		seen.Add(1)
		return nil
	})
	if err != nil || seen.Load() != 20 {
		t.Errorf("forEach ran %d of 20 items, err %v", seen.Load(), err)
	}
	if peak.Load() > 4 {
		t.Errorf("forEach ran %d calls at once, want at most 4", peak.Load())
	}

	boom := errors.New("boom")
	err = forEach(ctx, []int{1, 2, 3}, func(_ context.Context, i int) error {
		if i == 2 {
			return boom
		}
		return nil
	})
	if !errors.Is(err, boom) {
		t.Errorf("forEach error = %v, want boom", err)
	}
	if err := forEach(ctx, []int(nil), func(context.Context, int) error { return boom }); err != nil {
		t.Errorf("forEach over nothing = %v", err)
	}
}

// ---------------------------------------------------------------------------
// Server version and edition detection

func TestServerInfoCache(t *testing.T) {
	ctx := context.Background()

	t.Run("detects and caches", func(t *testing.T) {
		f, c := newFakeServer(t, map[string]func(*http.Request) (int, any){
			"GET /api/server/version":    ok(" 2026.5.0.1 \n"),
			"GET /api/navigation/global": ok(map[string]any{"edition": "Enterprise"}),
		})
		cache := &serverInfoCache{client: c}
		for range 3 {
			info, err := cache.get(ctx)
			if err != nil || info.Version != "2026.5.0.1" || info.Edition != "enterprise" {
				t.Fatalf("get = %+v, %v", info, err)
			}
		}
		if n := len(f.callsTo("GET", "/api/server/version")); n != 1 {
			t.Errorf("version fetched %d times, want once", n)
		}
	})

	t.Run("missing edition is community", func(t *testing.T) {
		_, c := newFakeServer(t, map[string]func(*http.Request) (int, any){
			"GET /api/server/version":    ok("26.11.0.1"),
			"GET /api/navigation/global": ok(map[string]any{}),
		})
		info, err := (&serverInfoCache{client: c}).get(ctx)
		if err != nil || info.Edition != "community" {
			t.Errorf("get = %+v, %v", info, err)
		}
	})

	t.Run("overrides skip detection", func(t *testing.T) {
		f, c := newFakeServer(t, nil)
		info, err := (&serverInfoCache{client: c, versionOverride: "9.9", editionOverride: "developer"}).get(ctx)
		if err != nil || info.Version != "9.9" || info.Edition != "developer" {
			t.Errorf("get = %+v, %v", info, err)
		}
		if len(f.calls) != 0 {
			t.Errorf("%d API calls despite overrides", len(f.calls))
		}
	})

	t.Run("failure is retried, not cached", func(t *testing.T) {
		var fail atomic.Bool
		fail.Store(true)
		_, c := newFakeServer(t, map[string]func(*http.Request) (int, any){
			"GET /api/server/version": func(*http.Request) (int, any) {
				if fail.Load() {
					return 500, `{"errors":[{"msg":"down"}]}`
				}
				return 200, "1.0"
			},
			"GET /api/navigation/global": ok(map[string]any{"edition": "developer"}),
		})
		cache := &serverInfoCache{client: c}
		if _, err := cache.get(ctx); err == nil || !strings.Contains(err.Error(), "installed_version") {
			t.Fatalf("get error = %v, want a hint about installed_version", err)
		}
		fail.Store(false)
		if info, err := cache.get(ctx); err != nil || info.Edition != "developer" {
			t.Errorf("get after recovery = %+v, %v", info, err)
		}
	})
}

func TestRequireEdition(t *testing.T) {
	ctx := context.Background()
	p := &providerData{info: &serverInfoCache{versionOverride: "1", editionOverride: "developer"}}
	var d diag.Diagnostics
	p.requireEdition(ctx, &d, "Branches", "developer")
	if d.HasError() || d.WarningsCount() != 0 {
		t.Errorf("developer on developer: %v", d)
	}
	p.requireEdition(ctx, &d, "Portfolios", "enterprise")
	if !d.HasError() || !strings.Contains(d.Errors()[0].Detail(), "developer edition 1") {
		t.Errorf("developer below enterprise: %v", d)
	}

	// Detection failing must not block the plan: it only warns.
	_, c := newFakeServer(t, nil)
	p = &providerData{info: &serverInfoCache{client: c}}
	d = nil
	p.requireEdition(ctx, &d, "Portfolios", "enterprise")
	if d.HasError() || d.WarningsCount() != 1 {
		t.Errorf("failed detection: %v", d)
	}

	// An unknown edition name ranks as community.
	if (&serverInfo{Edition: "mystery"}).hasEdition("developer") {
		t.Error("unknown edition should not satisfy developer")
	}
}

// ---------------------------------------------------------------------------
// Errors

func TestExplainEdgeCases(t *testing.T) {
	// No messages: the raw body is shown instead.
	err := &sonarqube.ErrorResponse{StatusCode: 502, Method: "GET", URL: "http://sq/api/x", Body: []byte("Bad Gateway")}
	if got := explain(err); !strings.HasSuffix(got, "returned HTTP 502: Bad Gateway") {
		t.Errorf("explain = %q", got)
	}
	// Neither messages nor body: no trailing colon.
	err = &sonarqube.ErrorResponse{StatusCode: 500, Method: "GET", URL: "http://sq/api/x"}
	if got := explain(err); got != "GET http://sq/api/x returned HTTP 500" {
		t.Errorf("explain = %q", got)
	}
	// A 404 for a missing object is not reported as a missing API.
	if got := explain(apiErr(404, "Project 'x' not found")); strings.Contains(got, "does not provide this API") {
		t.Errorf("explain = %q", got)
	}
	// Context wrapped around an API error is kept in front of it.
	wrapped := fmt.Errorf("revoke admin: %w", &sonarqube.ErrorResponse{StatusCode: 400, Method: "POST", URL: "http://sq/api/x", Messages: []string{"Last administrator"}})
	if got := explain(wrapped); got != "revoke admin: POST http://sq/api/x returned HTTP 400: Last administrator" {
		t.Errorf("explain(wrapped) = %q", got)
	}
	// A bare API error gets no prefix.
	bare := &sonarqube.ErrorResponse{StatusCode: 400, Method: "POST", URL: "http://sq/api/x", Messages: []string{"m"}}
	if got := explain(bare); got != "POST http://sq/api/x returned HTTP 400: m" {
		t.Errorf("explain(bare) = %q", got)
	}
	if hasMessage(apiErr(400), "x") {
		t.Error("hasMessage with no messages")
	}
}

func TestCreateError(t *testing.T) {
	var d diag.Diagnostics
	createError(&d, "group", "devs", apiErr(400, "Group 'devs' already exists"))
	if !d.HasError() || !strings.Contains(d.Errors()[0].Detail(), `terraform import <address> "devs"`) {
		t.Errorf("createError should suggest an import: %v", d)
	}
	d = nil
	createError(&d, "group", "", apiErr(400, "Group 'devs' already exists"))
	if strings.Contains(d.Errors()[0].Detail(), "terraform import") {
		t.Error("no import hint without an import ID")
	}
	d = nil
	createError(&d, "group", "devs", apiErr(400, "Name is too long"))
	if strings.Contains(d.Errors()[0].Detail(), "terraform import") {
		t.Error("no import hint for an unrelated 400")
	}
	if d.Errors()[0].Summary() != "Unable to create group" {
		t.Errorf("summary = %q", d.Errors()[0].Summary())
	}
}

func TestIdentityError(t *testing.T) {
	var d diag.Diagnostics
	identityError(&d, "alice", apiErr(500))
	if !strings.Contains(d.Errors()[0].Detail(), "identity_providers") {
		t.Errorf("500 should explain the likely causes: %v", d)
	}
	d = nil
	identityError(&d, "alice", apiErr(400, "bad"))
	if strings.Contains(d.Errors()[0].Detail(), "identity_providers") {
		t.Error("only a 500 gets the hint")
	}
}

// ---------------------------------------------------------------------------
// Import IDs and permissions IDs

func TestSplitIDMoreParts(t *testing.T) {
	got, err := splitID("gate/proj/main", "/", 3, "a/b/c")
	if err != nil || !slices.Equal(got, []string{"gate", "proj", "main"}) {
		t.Errorf("splitID = %v, %v", got, err)
	}
	for _, bad := range []string{"a/b", "a//c", "/b/c"} {
		if _, err := splitID(bad, "/", 3, "a/b/c"); err == nil {
			t.Errorf("splitID(%q) accepted", bad)
		}
	}
}

func TestPermissionsScopeAndID(t *testing.T) {
	null := types.StringNull()
	v := types.StringValue
	for _, c := range []struct {
		m    permissionsModel
		want string
	}{
		{permissionsModel{LoginName: v("alice"), GroupName: null, ProjectKey: null, TemplateID: null, TemplateName: null}, "user-alice-global-permissions"},
		{permissionsModel{LoginName: null, GroupName: v("devs"), ProjectKey: v("p"), TemplateID: null, TemplateName: null}, "group-devs-p_p-permissions"},
		{permissionsModel{LoginName: null, GroupName: v("devs"), ProjectKey: null, TemplateID: v("AX1"), TemplateName: null}, "group-devs-t_AX1-permissions"},
		{permissionsModel{LoginName: null, GroupName: v("devs"), ProjectKey: null, TemplateID: null, TemplateName: v("Default")}, "group-devs-tn_Default-permissions"},
		{permissionsModel{LoginName: null, GroupName: null, ProjectKey: null, TemplateID: null, TemplateName: v("T")}, "project-creator-tn_T-permissions"},
	} {
		if got := c.m.id().ValueString(); got != c.want {
			t.Errorf("id = %q, want %q", got, c.want)
		}
	}
	m := permissionsModel{TemplateID: null, TemplateName: null}
	if m.template() {
		t.Error("template() without a template")
	}
	m.TemplateName = v("T")
	if !m.template() {
		t.Error("template() with a template name")
	}
}

func TestSearchQEdgeCases(t *testing.T) {
	for in, want := range map[string]string{"": "", "abc": "abc", "ANYONE": "", "anyone-else": "anyone-else"} {
		if got := searchQ(in); got != want {
			t.Errorf("searchQ(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Validators

func runStringValidator(v validator.String, val types.String) diag.Diagnostics {
	var resp validator.StringResponse
	v.ValidateString(context.Background(), validator.StringRequest{Path: path.Root("x"), ConfigValue: val}, &resp)
	return resp.Diagnostics
}

func TestValidators(t *testing.T) {
	for _, c := range []struct {
		name string
		v    validator.String
		good []string
		bad  []string
	}{
		{"version", versionStringValidator{}, []string{"2026", "2026.1", "26.11.0.134175"}, []string{"", "v1", "1.", ".1", "1..2", "1.2-SNAPSHOT"}},
		{"duration", durationValidator{}, []string{"1ns", "30s", "2m", "1h30m"}, []string{"", "0s", "-1s", "30", "soon"}},
		{"project key", projectKeyValidator{}, []string{"a", "my-project", "org:proj.v1_2", "1a", strings.Repeat("a", 400)},
			[]string{"", "123", "has space", "é", "a/b", strings.Repeat("a", 401)}},
		{"no surrounding space", noSurroundingSpace{}, []string{"", "a b", "x"}, []string{" a", "a ", "\ta", "a\n"}},
	} {
		for _, s := range c.good {
			if d := runStringValidator(c.v, types.StringValue(s)); d.HasError() {
				t.Errorf("%s: %q rejected: %v", c.name, s, d)
			}
		}
		for _, s := range c.bad {
			if d := runStringValidator(c.v, types.StringValue(s)); !d.HasError() {
				t.Errorf("%s: %q accepted", c.name, s)
			}
		}
		for _, s := range []types.String{types.StringNull(), types.StringUnknown()} {
			if d := runStringValidator(c.v, s); d.HasError() {
				t.Errorf("%s: %v rejected", c.name, s)
			}
		}
		if c.v.Description(context.Background()) == "" || c.v.MarkdownDescription(context.Background()) == "" {
			t.Errorf("%s: empty description", c.name)
		}
	}
}

func TestValueHelpers(t *testing.T) {
	if !strOrNull("").IsNull() || strOrNull("x").ValueString() != "x" {
		t.Error("strOrNull")
	}
	l := stringList([]string{"b", "a", "b"})
	if len(l.Elements()) != 3 || l.Elements()[0].(types.String).ValueString() != "b" {
		t.Error("stringList must keep order and duplicates")
	}
	if l := stringList(nil); l.IsNull() || len(l.Elements()) != 0 {
		t.Error("stringList(nil) must be an empty, non-null list")
	}
	if s := stringSet(nil); s.IsNull() || len(s.Elements()) != 0 {
		t.Error("stringSet(nil) must be an empty, non-null set")
	}
	if a, b := randSuffix(), randSuffix(); len(a) != 12 || a == b {
		t.Errorf("randSuffix = %q, %q", a, b)
	}
}

func TestParseParamsEdgeCases(t *testing.T) {
	// Values keep their spaces and further '='; keys are trimmed.
	got := parseParams(" max = 10 ;;format=^[a-z]+=$")
	if got["max"] != " 10 " || got["format"] != "^[a-z]+=$" || len(got) != 2 {
		t.Errorf("parseParams = %q", got)
	}
	// A key that is only whitespace is no key.
	if got := parseParams(" =x"); len(got) != 0 {
		t.Errorf("parseParams(\" =x\") = %q, want no entries", got)
	}
}

// ---------------------------------------------------------------------------
// Tokens

func TestTokenDateTimeZones(t *testing.T) {
	// The date is the server's local date, not the UTC one.
	for in, want := range map[string]string{
		"2027-12-31T23:59:59-1100": "2027-12-31",
		"2027-01-01T00:30:00+1400": "2027-01-01",
		"":                         "",
	} {
		if got := tokenDate(in); got != want {
			t.Errorf("tokenDate(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRotationDueEdgeCases(t *testing.T) {
	day := func(s string) time.Time { d, _ := time.Parse("2006-01-02", s); return d }
	// rotate_before longer than the token's lifetime: due at once.
	if !rotationDue("2027-01-31", 90, day("2027-01-01")) {
		t.Error("rotate_before beyond the lifetime should be due")
	}
	// Month boundaries and leap days.
	if !rotationDue("2028-03-01", 1, day("2028-02-29")) || rotationDue("2028-03-01", 1, day("2028-02-28")) {
		t.Error("leap-year boundary")
	}
	if rotationDue("", 0, day("2027-01-01")) || tokenExpired("", day("2027-01-01")) {
		t.Error("no expiration date is never due nor expired")
	}
}

func TestTokenMatches(t *testing.T) {
	r := &userTokenResource{}
	v, null := types.StringValue, types.StringNull()
	project := func(k string) *struct {
		Key string `json:"key"`
	} {
		return &struct {
			Key string `json:"key"`
		}{k}
	}
	user := &userTokenModel{Type: v("USER_TOKEN"), ProjectKey: null, ExpirationDate: null}
	if err := r.matches(user, apiToken{Name: "t", Type: "USER_TOKEN"}); err != nil {
		t.Error(err)
	}
	if err := r.matches(user, apiToken{Name: "t", Type: "GLOBAL_ANALYSIS_TOKEN"}); err == nil {
		t.Error("type mismatch accepted")
	}
	proj := &userTokenModel{Type: v("PROJECT_ANALYSIS_TOKEN"), ProjectKey: v("p"), ExpirationDate: null}
	if err := r.matches(proj, apiToken{Name: "t", Type: "PROJECT_ANALYSIS_TOKEN"}); err == nil {
		t.Error("project token without a project accepted")
	}
	if err := r.matches(proj, apiToken{Name: "t", Type: "PROJECT_ANALYSIS_TOKEN", Project: project("q")}); err == nil {
		t.Error("wrong project accepted")
	}
	if err := r.matches(proj, apiToken{Name: "t", Type: "PROJECT_ANALYSIS_TOKEN", Project: project("p")}); err != nil {
		t.Error(err)
	}
	dated := &userTokenModel{Type: v("USER_TOKEN"), ProjectKey: null, ExpirationDate: v("2027-01-31")}
	if err := r.matches(dated, apiToken{Type: "USER_TOKEN", ExpirationDate: "2027-01-31T00:00:00+0000"}); err != nil {
		t.Error(err)
	}
	if err := r.matches(dated, apiToken{Type: "USER_TOKEN", ExpirationDate: "2027-02-01T00:00:00+0000"}); err == nil {
		t.Error("expiration mismatch accepted")
	}
	// An unknown planned date (expiration_days) matches any date.
	dated.ExpirationDate = types.StringUnknown()
	if err := r.matches(dated, apiToken{Type: "USER_TOKEN", ExpirationDate: "2030-01-01T00:00:00+0000"}); err != nil {
		t.Error(err)
	}
}

// ---------------------------------------------------------------------------
// Settings

func settingBlockOf(key string) settingModel {
	return settingModel{Key: types.StringValue(key), Value: types.StringNull(),
		Values: types.ListNull(types.StringType), FieldValues: types.ListNull(fieldValuesType.ElemType)}
}

func TestSameSettingEdgeCases(t *testing.T) {
	for _, c := range []struct {
		want, have apiSetting
		same       bool
	}{
		// Unset on the server: never the same as a configured value.
		{apiSetting{Value: "a"}, apiSetting{}, false},
		{apiSetting{Values: []string{"a"}}, apiSetting{}, false},
		{apiSetting{Values: []string{"a"}}, apiSetting{Values: []string{"a", "b"}}, false},
		// Field values compare independently of map key order.
		{apiSetting{FieldValues: []map[string]string{{"a": "1", "b": "2"}}}, apiSetting{FieldValues: []map[string]string{{"b": "2", "a": "1"}}}, true},
		// Field value order is significant.
		{apiSetting{FieldValues: []map[string]string{{"k": "1"}, {"k": "2"}}}, apiSetting{FieldValues: []map[string]string{{"k": "2"}, {"k": "1"}}}, false},
		{apiSetting{FieldValues: []map[string]string{{"k": "1"}}}, apiSetting{}, false},
		// A setting without a definition stores values joined by ',', with a
		// ',' inside a value encoded as %2C (observed on SonarQube 26.11).
		{apiSetting{Values: []string{"a,b", "c"}}, apiSetting{Value: "a%2Cb,c"}, true},
		{apiSetting{Values: []string{"a,b", "c"}}, apiSetting{Values: []string{"a,b", "c"}}, true},
	} {
		if got := sameSetting(c.want, c.have); got != c.same {
			t.Errorf("sameSetting(%+v, %+v) = %v, want %v", c.want, c.have, got, c.same)
		}
	}
}

func TestSettingKeys(t *testing.T) {
	unknown := settingBlockOf("")
	unknown.Key = types.StringUnknown()
	if got := settingKeys([]settingModel{settingBlockOf("a"), unknown, settingBlockOf("b")}); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("settingKeys = %v", got)
	}
	if got := settingKeys(nil); got == nil || len(got) != 0 {
		t.Errorf("settingKeys(nil) = %#v", got)
	}
}

func TestValidateSettingBlocksSkipsUnknownKeys(t *testing.T) {
	a, b := settingBlockOf(""), settingBlockOf("")
	a.Key, b.Key = types.StringUnknown(), types.StringUnknown()
	var d diag.Diagnostics
	validateSettingBlocks([]settingModel{a, b}, &d)
	if d.HasError() {
		t.Errorf("unknown keys (from a for_each) are not duplicates: %v", d)
	}
}

func TestSettingToModel(t *testing.T) {
	ctx := context.Background()
	var d diag.Diagnostics
	multi := apiSetting{Key: "k", Values: []string{"a", "b"}}

	shape := settingBlockOf("k")
	shape.Value = types.StringValue("a,b")
	if m := settingToModel(ctx, multi, shape, &d); m.Value.ValueString() != "a,b" || !m.Values.IsNull() {
		t.Errorf("configured as value: %+v", m)
	}
	shape = settingBlockOf("k")
	shape.Values = stringList([]string{"x"})
	if m := settingToModel(ctx, apiSetting{Key: "k", Value: "a,b"}, shape, &d); len(m.Values.Elements()) != 2 || !m.Value.IsNull() {
		t.Errorf("configured as values: %+v", m)
	}
	if m := settingToModel(ctx, apiSetting{Key: "k", Value: "a%2Cb,c"}, shape, &d); !slices.Equal(stringsOf(ctx, m.Values, &d), []string{"a,b", "c"}) {
		t.Errorf("encoded comma: %+v", m.Values)
	}
	// Unconfigured settings take the server's form.
	if m := settingToModel(ctx, multi, settingModel{}, &d); len(m.Values.Elements()) != 2 {
		t.Errorf("unconfigured multi-value: %+v", m)
	}
	fv := apiSetting{Key: "k", FieldValues: []map[string]string{{"a": "1"}}}
	if m := settingToModel(ctx, fv, settingModel{}, &d); len(m.FieldValues.Elements()) != 1 || !m.Value.IsNull() {
		t.Errorf("unconfigured field values: %+v", m)
	}
	if m := settingToModel(ctx, apiSetting{Key: "k", Value: "v"}, settingModel{}, &d); m.Value.ValueString() != "v" {
		t.Errorf("unconfigured single value: %+v", m)
	}
	if d.HasError() {
		t.Error(d)
	}
}

func TestSettingsState(t *testing.T) {
	ctx := context.Background()
	var d diag.Diagnostics
	prior := []settingModel{settingBlockOf("z"), settingBlockOf("gone"), settingBlockOf("a"), settingBlockOf("z")}
	for i := range prior {
		prior[i].Value = types.StringValue("x")
	}
	current := []apiSetting{
		{Key: "a", Value: "1"},
		{Key: "z", Value: "2"},
		{Key: "drift-b", Value: "3"},
		{Key: "drift-a", Value: "4"},
		{Key: "inherited", Value: "5", Inherited: true},
	}
	var keys []string
	for _, m := range settingsState(ctx, prior, current, &d) {
		keys = append(keys, m.Key.ValueString())
	}
	// Configured order first (once each, deleted ones dropped), then drift
	// sorted by key; inherited values are not the component's own.
	if want := []string{"z", "a", "drift-a", "drift-b"}; !slices.Equal(keys, want) {
		t.Errorf("settingsState keys = %v, want %v", keys, want)
	}
	if got := settingsState(ctx, nil, nil, &d); got != nil {
		t.Errorf("no settings = %v", got)
	}
}

func TestDesiredSetting(t *testing.T) {
	ctx := context.Background()
	var d diag.Diagnostics
	b := settingBlockOf("k")
	b.FieldValues = fieldValuesList(ctx, []map[string]string{{"a": "1"}}, &d)
	got := b.desired(ctx, &d)
	if got.Key != "k" || got.Value != "" || got.Values != nil || len(got.FieldValues) != 1 || got.FieldValues[0]["a"] != "1" {
		t.Errorf("desired = %+v", got)
	}
	b = settingBlockOf("k")
	b.FieldValues = types.ListUnknown(fieldValuesType.ElemType)
	if got := b.desired(ctx, &d); got.FieldValues != nil {
		t.Errorf("unknown field values = %+v", got.FieldValues)
	}
	if d.HasError() {
		t.Error(d)
	}
}

func settingsRoutes(all, named []apiSetting, onSet, onReset func(*http.Request) (int, any)) map[string]func(*http.Request) (int, any) {
	return map[string]func(*http.Request) (int, any){
		"GET /api/settings/values": func(r *http.Request) (int, any) {
			if r.URL.Query().Get("keys") != "" {
				return 200, apiSettings{Settings: named}
			}
			return 200, apiSettings{Settings: all}
		},
		"POST /api/settings/set":   onSet,
		"POST /api/settings/reset": onReset,
	}
}

func TestReadComponentSettingsMerges(t *testing.T) {
	_, c := newFakeServer(t, settingsRoutes(
		[]apiSetting{{Key: "a", Value: "1"}, {Key: "b", Value: "2"}},
		[]apiSetting{{Key: "b", Value: "ignored"}, {Key: "undefined", Value: "3"}},
		nil, nil))
	got, err := readComponentSettings(context.Background(), c, "p", []string{"b", "undefined"})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, s := range got {
		keys = append(keys, s.Key+"="+s.Value)
	}
	if want := []string{"a=1", "b=2", "undefined=3"}; !slices.Equal(keys, want) {
		t.Errorf("readComponentSettings = %v, want %v", keys, want)
	}

	// A malformed body is an error, not an empty list.
	_, c = newFakeServer(t, map[string]func(*http.Request) (int, any){"GET /api/settings/values": ok("{not json")})
	if _, err := readComponentSettings(context.Background(), c, "p", nil); err == nil {
		t.Error("malformed body accepted")
	}
}

func TestSyncComponentSettings(t *testing.T) {
	ctx := context.Background()
	f, c := newFakeServer(t, settingsRoutes(
		[]apiSetting{
			{Key: "same", Value: "1"},
			{Key: "changed", Value: "old"},
			{Key: "stale-b", Value: "x"},
			{Key: "stale-a", Value: "x"},
			{Key: "inherited", Value: "x", Inherited: true},
		},
		nil, ok(nil), ok(nil)))
	p := &providerData{client: c, locks: newKeyedMutex()}

	same, changed, added := settingBlockOf("same"), settingBlockOf("changed"), settingBlockOf("added")
	same.Value, changed.Value = types.StringValue("1"), types.StringValue("new")
	added.Values = stringList([]string{"x", "y"})

	var d diag.Diagnostics
	syncComponentSettings(ctx, p, "proj", []settingModel{same, changed, added}, nil, &d)
	if d.HasError() {
		t.Fatal(d)
	}
	var set []string
	for _, call := range f.callsTo("POST", "/api/settings/set") {
		set = append(set, call.form["key"][0])
		if call.form["component"][0] != "proj" {
			t.Errorf("set without the component: %v", call.form)
		}
	}
	if want := []string{"changed", "added"}; !slices.Equal(set, want) {
		t.Errorf("set %v, want %v (unchanged settings are not rewritten)", set, want)
	}
	resets := f.callsTo("POST", "/api/settings/reset")
	if len(resets) != 1 || resets[0].form.Get("keys") != "stale-a,stale-b" {
		t.Errorf("reset calls = %+v, want one for stale-a,stale-b", resets)
	}
}

// Without any stale setting, no reset call is made at all.
func TestSyncComponentSettingsNoReset(t *testing.T) {
	f, c := newFakeServer(t, settingsRoutes([]apiSetting{{Key: "k", Value: "v"}}, nil, ok(nil), ok(nil)))
	b := settingBlockOf("k")
	b.Value = types.StringValue("v")
	var d diag.Diagnostics
	syncComponentSettings(context.Background(), &providerData{client: c, locks: newKeyedMutex()}, "p", []settingModel{b}, nil, &d)
	if d.HasError() || len(f.calls) != 2 { // two reads, nothing written
		t.Errorf("calls = %+v, diags %v", f.calls, d)
	}
}

func TestSetSettingPrecedence(t *testing.T) {
	f, c := newFakeServer(t, map[string]func(*http.Request) (int, any){"POST /api/settings/set": ok(nil)})
	ctx := context.Background()
	if err := setSetting(ctx, c, "", "k", "v", []string{"ignored"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := setSetting(ctx, c, "", "k", "", []string{"a", "b"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := setSetting(ctx, c, "", "k", "", nil, []map[string]string{{"x": "1"}, {"x": "2"}}); err != nil {
		t.Fatal(err)
	}
	calls := f.callsTo("POST", "/api/settings/set")
	if calls[0].form.Get("value") != "v" || calls[0].form["values"] != nil {
		t.Errorf("value call = %v", calls[0].form)
	}
	if !slices.Equal(calls[1].form["values"], []string{"a", "b"}) || calls[1].form.Get("value") != "" {
		t.Errorf("values call = %v", calls[1].form)
	}
	if !slices.Equal(calls[2].form["fieldValues"], []string{`{"x":"1"}`, `{"x":"2"}`}) {
		t.Errorf("fieldValues call = %v", calls[2].form)
	}
}

func TestResetSettings(t *testing.T) {
	ctx := context.Background()
	f, c := newFakeServer(t, map[string]func(*http.Request) (int, any){
		"POST /api/settings/reset": func(*http.Request) (int, any) { return 404, `{"errors":[{"msg":"Component key 'p' not found"}]}` },
	})
	if err := resetSettings(ctx, c, "p", nil); err != nil || len(f.calls) != 0 {
		t.Errorf("no keys: err %v, %d calls", err, len(f.calls))
	}
	// A deleted component has no settings left to reset.
	if err := resetSettings(ctx, c, "p", []string{"k"}); err != nil {
		t.Errorf("deleted component: %v", err)
	}
	// A global 404 is still an error.
	if err := resetSettings(ctx, c, "", []string{"k"}); err == nil {
		t.Error("global 404 swallowed")
	}
}

// ---------------------------------------------------------------------------
// Quality profiles

func TestDescendantsWithoutRule(t *testing.T) {
	ctx := context.Background()
	rulesShown := 0
	_, c := newFakeServer(t, map[string]func(*http.Request) (int, any){
		"GET /api/qualityprofiles/search": ok(map[string]any{"profiles": []map[string]string{
			{"key": "root"},
			{"key": "child1", "parentKey": "root"},
			{"key": "child2", "parentKey": "root"},
			{"key": "grandchild", "parentKey": "child1"},
			{"key": "other", "parentKey": "elsewhere"},
		}}),
		"GET /api/rules/show": func(*http.Request) (int, any) {
			rulesShown++
			return 200, map[string]any{"rule": map[string]string{"key": "java:S1"}, "actives": []map[string]string{{"qProfile": "root"}, {"qProfile": "child2"}}}
		},
	})
	got, err := descendantsWithoutRule(ctx, c, "root", "java:S1")
	if err != nil || !slices.Equal(got, []string{"child1", "grandchild"}) {
		t.Errorf("descendantsWithoutRule = %v, %v", got, err)
	}
	// A cycle in the parent keys ends the walk instead of looping forever.
	_, cyc := newFakeServer(t, map[string]func(*http.Request) (int, any){
		"GET /api/qualityprofiles/search": ok(map[string]any{"profiles": []map[string]string{
			{"key": "root", "parentKey": "b"},
			{"key": "a", "parentKey": "root"},
			{"key": "b", "parentKey": "a"},
		}}),
		"GET /api/rules/show": ok(map[string]any{"rule": map[string]string{"key": "java:S1"}}),
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		got, err := descendantsWithoutRule(ctx, cyc, "root", "java:S1")
		if err != nil || !slices.Equal(got, []string{"a", "b"}) {
			t.Errorf("cyclic descendantsWithoutRule = %v, %v", got, err)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("descendantsWithoutRule loops on a parent cycle")
	}

	// A leaf profile has no descendants; the rule is not even looked up.
	rulesShown = 0
	if got, err := descendantsWithoutRule(ctx, c, "grandchild", "java:S1"); err != nil || got != nil || rulesShown != 0 {
		t.Errorf("leaf: %v, %v, %d rule lookups", got, err, rulesShown)
	}
}

// ---------------------------------------------------------------------------
// State upgrade from jdamata

type upgradeOnlyResource struct{ resource.Resource }

func (upgradeOnlyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{Attributes: map[string]schema.Attribute{
		"name": schema.StringAttribute{Required: true},
		"opts": schema.SingleNestedAttribute{Optional: true, Attributes: map[string]schema.Attribute{
			"mode": schema.StringAttribute{Optional: true},
		}},
	}}
}

func TestUpgradeFromJdamataErrors(t *testing.T) {
	ctx := context.Background()
	up := upgradeFromJdamata(upgradeOnlyResource{})[0].StateUpgrader
	for name, raw := range map[string]*tfprotov6.RawState{
		"nil raw state": nil,
		"empty JSON":    {JSON: nil},
		"invalid JSON":  {JSON: []byte("{")},
	} {
		var resp resource.UpgradeStateResponse
		up(ctx, resource.UpgradeStateRequest{RawState: raw}, &resp)
		if !resp.Diagnostics.HasError() {
			t.Errorf("%s: no error", name)
		}
	}

	var resp resource.UpgradeStateResponse
	up(ctx, resource.UpgradeStateRequest{RawState: &tfprotov6.RawState{JSON: []byte(`{"name":"","opts":{"mode":""}}`)}}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	// A required string keeps "", a nested optional one becomes null.
	if got := string(resp.DynamicValue.JSON); got != `{"name":"","opts":{"mode":null}}` {
		t.Errorf("upgraded state = %s", got)
	}
}

func TestNormalizeObjectNestedBlocks(t *testing.T) {
	blocks := map[string]schema.Block{
		"outer": schema.SetNestedBlock{NestedObject: schema.NestedBlockObject{
			Attributes: map[string]schema.Attribute{"v": schema.StringAttribute{Optional: true}},
			Blocks: map[string]schema.Block{
				"inner": schema.ListNestedBlock{NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{"w": schema.StringAttribute{Optional: true}},
				}},
			},
		}},
	}
	obj := map[string]any{"outer": []any{
		map[string]any{"v": "", "inner": []any{map[string]any{"w": "", "dropped": 1}}},
		map[string]any{"v": "kept"},
		"not an object",
	}}
	normalizeObject(obj, nil, blocks)
	b, _ := json.Marshal(obj)
	if got, want := string(b), `{"outer":[{"inner":[{"w":null}],"v":null},{"inner":[],"v":"kept"},"not an object"]}`; got != want {
		t.Errorf("normalizeObject = %s, want %s", got, want)
	}
}

// ---------------------------------------------------------------------------
// Resource identity

func TestIdentitySpec(t *testing.T) {
	single := identitySpec{attrs: []identityAttr{{name: "key", state: "key"}}}
	if single.id([]string{"p"}) != "p" {
		t.Error("single-value identity is its own import ID")
	}
	pair := identitySpec{
		attrs:    []identityAttr{{name: "gate", state: "gate"}, {name: "project", state: "project"}},
		importID: func(v []string) string { return strings.Join(v, "/") },
	}
	if pair.id([]string{"g", "p"}) != "g/p" {
		t.Error("composite import ID")
	}

	ctx := context.Background()
	sch := schema.Schema{Attributes: map[string]schema.Attribute{
		"gate":    schema.StringAttribute{Optional: true},
		"project": schema.StringAttribute{Optional: true},
	}}
	stateOf := func(gate, project tftypes.Value) tfsdk.State {
		return tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), map[string]tftypes.Value{"gate": gate, "project": project})}
	}
	str := func(s string) tftypes.Value { return tftypes.NewValue(tftypes.String, s) }

	vals, ok, d := pair.identityOf(ctx, stateOf(str("g"), str("p")))
	if !ok || d.HasError() || !slices.Equal(vals, []string{"g", "p"}) {
		t.Errorf("identityOf = %v, %v, %v", vals, ok, d)
	}
	for name, st := range map[string]tfsdk.State{
		"null":    stateOf(str("g"), tftypes.NewValue(tftypes.String, nil)),
		"unknown": stateOf(tftypes.NewValue(tftypes.String, tftypes.UnknownValue), str("p")),
	} {
		if _, ok, _ := pair.identityOf(ctx, st); ok {
			t.Errorf("%s value yields an identity", name)
		}
	}
	// No identity to set: a nil identity, or a removed resource.
	if d := pair.setIdentity(ctx, stateOf(str("g"), str("p")), nil); d.HasError() {
		t.Error(d)
	}
	removed := tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)}
	if d := pair.setIdentity(ctx, removed, &tfsdk.ResourceIdentity{}); d.HasError() {
		t.Error(d)
	}
}
