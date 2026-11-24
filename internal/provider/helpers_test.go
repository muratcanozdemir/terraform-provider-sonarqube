package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

func apiErr(status int, msgs ...string) error {
	return fmt.Errorf("wrapped: %w", &sonarqube.ErrorResponse{StatusCode: status, Method: "POST", URL: "http://sq/api/x", Messages: msgs})
}

func TestParseParams(t *testing.T) {
	for in, want := range map[string]map[string]string{
		"":                      {},
		"Threshold=10":          {"Threshold": "10"},
		"a=1; b=2":              {"a": "1", "b": "2"},
		"regex=a=b;message=x y": {"regex": "a=b", "message": "x y"},
		"novalue;=orphan;k=":    {"k": ""},
	} {
		got := parseParams(in)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("parseParams(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestSplitID(t *testing.T) {
	if got, err := splitID("key/java:S1", "/", 2, "a/b"); err != nil || got[0] != "key" || got[1] != "java:S1" {
		t.Errorf("splitID = %v, %v", got, err)
	}
	// The last part keeps further separators.
	if got, err := splitID("p/a/b", "/", 2, "a/b"); err != nil || got[1] != "a/b" {
		t.Errorf("splitID = %v, %v", got, err)
	}
	for _, bad := range []string{"", "nosep", "/x", "x/"} {
		if _, err := splitID(bad, "/", 2, "a/b"); err == nil || !strings.Contains(err.Error(), `"a/b"`) {
			t.Errorf("splitID(%q) error = %v, want the expected format", bad, err)
		}
	}
}

func TestIDs(t *testing.T) {
	if got := groupMemberID("devs", "alice"); got != "devs[alice]" {
		t.Errorf("groupMemberID = %q", got)
	}
	if got := settingID("sonar.k", ""); got != "sonar.k" {
		t.Errorf("settingID global = %q", got)
	}
	if got := settingID("sonar.k", "proj"); got != "sonar.k:proj" {
		t.Errorf("settingID component = %q", got)
	}
}

func TestDiffStrings(t *testing.T) {
	add, remove := diffStrings([]string{"a", "b", "c"}, []string{"b", "c", "d"})
	if fmt.Sprint(add) != "[d]" || fmt.Sprint(remove) != "[a]" {
		t.Errorf("diffStrings = %v, %v", add, remove)
	}
	if add, remove := diffStrings(nil, nil); add != nil || remove != nil {
		t.Errorf("diffStrings(nil, nil) = %v, %v", add, remove)
	}
	if !sameElements([]string{"b", "a"}, []string{"a", "b"}) || sameElements([]string{"a"}, []string{"a", "a"}) {
		t.Error("sameElements")
	}
	in := []string{"b", "a"}
	sameElements(in, []string{"a", "b"})
	if in[0] != "b" {
		t.Error("sameElements sorted its argument in place")
	}
}

func TestStringValues(t *testing.T) {
	if !keepNullIfEmpty(types.StringNull(), "").IsNull() {
		t.Error("keepNullIfEmpty: null prior and empty value should stay null")
	}
	if v := keepNullIfEmpty(types.StringValue("x"), ""); v.IsNull() || v.ValueString() != "" {
		t.Error("keepNullIfEmpty: a set prior should become the empty string")
	}
	if !emptyToNull(types.StringValue("")).IsNull() || emptyToNull(types.StringValue("a")).ValueString() != "a" {
		t.Error("emptyToNull")
	}
	ctx := context.Background()
	var diags diag.Diagnostics
	set := stringSet([]string{"x", "y"})
	if got := stringsOf(ctx, set, &diags); len(got) != 2 || diags.HasError() {
		t.Errorf("stringsOf = %v, %v", got, diags)
	}
	if got := stringsOf(ctx, types.SetNull(types.StringType), &diags); got != nil {
		t.Errorf("stringsOf(null) = %v", got)
	}
	if got := stringsOf(ctx, types.SetUnknown(types.StringType), &diags); got != nil {
		t.Errorf("stringsOf(unknown) = %v", got)
	}
}

func TestTokenDate(t *testing.T) {
	for in, want := range map[string]string{
		"2027-12-31T23:59:59+0100": "2027-12-31",
		"2027-12-31":               "2027-12-31",
		"2027-12-31T00:00:00Z":     "2027-12-31",
		"short":                    "short",
	} {
		if got := tokenDate(in); got != want {
			t.Errorf("tokenDate(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRotationDue(t *testing.T) {
	day := func(s string) time.Time { d, _ := time.Parse("2006-01-02", s); return d }
	for _, c := range []struct {
		expires string
		before  int64
		now     string
		want    bool
	}{
		{"2027-01-31", 7, "2027-01-23", false}, // 8 days left
		{"2027-01-31", 7, "2027-01-24", true},  // 7 days left
		{"2027-01-31", 0, "2027-01-30", false},
		{"2027-01-31", 0, "2027-01-31", true}, // its expiration date, the last usable day
		{"2027-01-31", 0, "2027-03-01", true},
		{"not a date", 7, "2027-01-31", false},
	} {
		if got := rotationDue(c.expires, c.before, day(c.now)); got != c.want {
			t.Errorf("rotationDue(%s, %d, %s) = %v, want %v", c.expires, c.before, c.now, got, c.want)
		}
	}
}

func TestTokenExpired(t *testing.T) {
	day := func(s string) time.Time { d, _ := time.Parse("2006-01-02", s); return d }
	for now, want := range map[string]bool{"2027-01-30": false, "2027-01-31": false, "2027-02-01": true} {
		if got := tokenExpired("2027-01-31", day(now)); got != want {
			t.Errorf("tokenExpired(2027-01-31, %s) = %v, want %v", now, got, want)
		}
	}
	if tokenExpired("not a date", day("2027-01-31")) {
		t.Error("invalid date reported expired")
	}
}

func TestSearchQ(t *testing.T) {
	// SonarQube rejects search terms under 3 characters, and "anyone" is not a
	// real group to search for.
	for in, want := range map[string]string{"ab": "", "Anyone": "", "devs": "devs"} {
		if got := searchQ(in); got != want {
			t.Errorf("searchQ(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnvOr(t *testing.T) {
	t.Setenv("TF_SQ_A", "")
	t.Setenv("TF_SQ_B", "from-b")
	if got := envOr(types.StringValue("cfg"), "TF_SQ_B"); got != "cfg" {
		t.Errorf("config should win, got %q", got)
	}
	if got := envOr(types.StringNull(), "TF_SQ_A", "TF_SQ_B"); got != "from-b" {
		t.Errorf("first non-empty variable should win, got %q", got)
	}
	if got := envOr(types.StringUnknown(), "TF_SQ_NONE"); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestErrorClassification(t *testing.T) {
	notFound := apiErr(404, "Project not found")
	unknownURL := apiErr(404, "Unknown url : /api/x")
	if !isGone(notFound) || isGone(unknownURL) || isGone(errors.New("x")) {
		t.Error("isGone: an unknown URL is a missing API, not a missing object")
	}
	if !isUnknownURL(unknownURL) || isUnknownURL(notFound) {
		t.Error("isUnknownURL")
	}
	if !hasMessage(apiErr(400, "Rule is INHERITED"), "inherited") || hasMessage(errors.New("inherited"), "inherited") {
		t.Error("hasMessage: case-insensitive, API errors only")
	}
	if !alreadyExists(apiErr(400, "Group 'x' already exists")) ||
		!alreadyExists(apiErr(400, `Could not create Project with key: "k". A similar key already exists: "k"`)) ||
		alreadyExists(apiErr(409, "already exists")) {
		t.Error("alreadyExists")
	}
}

func TestExplain(t *testing.T) {
	for _, c := range []struct {
		err  error
		want []string
	}{
		{apiErr(401), []string{"HTTP 401", "Authentication failed"}},
		{apiErr(403, "Insufficient privileges"), []string{"Insufficient privileges", "lack the permission"}},
		{apiErr(404, "Unknown url : /api/x"), []string{"does not provide this API"}},
		{apiErr(400, "a", "b"), []string{"POST http://sq/api/x returned HTTP 400: a; b"}},
		{fmt.Errorf("x: %w", context.DeadlineExceeded), []string{"request_timeout"}},
		{errors.New("plain"), []string{"plain"}},
	} {
		got := explain(c.err)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("explain(%v) = %q, want it to contain %q", c.err, got, w)
			}
		}
	}
}

func TestSameSetting(t *testing.T) {
	for _, c := range []struct {
		want, have apiSetting
		same       bool
	}{
		{apiSetting{Value: "a"}, apiSetting{Value: "a"}, true},
		{apiSetting{Value: "a"}, apiSetting{Value: "b"}, false},
		// A single value written to a multi-value setting reads back as values.
		{apiSetting{Value: "a,b"}, apiSetting{Values: []string{"a", "b"}}, true},
		{apiSetting{Values: []string{"a", "b"}}, apiSetting{Values: []string{"a", "b"}}, true},
		{apiSetting{Values: []string{"a", "b"}}, apiSetting{Values: []string{"b", "a"}}, false},
		{apiSetting{Values: []string{"a", "b"}}, apiSetting{Value: "a,b"}, true},
		{apiSetting{FieldValues: []map[string]string{{"k": "v"}}}, apiSetting{FieldValues: []map[string]string{{"k": "v"}}}, true},
		{apiSetting{FieldValues: []map[string]string{{"k": "v"}}}, apiSetting{FieldValues: []map[string]string{{"k": "w"}}}, false},
	} {
		if got := sameSetting(c.want, c.have); got != c.same {
			t.Errorf("sameSetting(%+v, %+v) = %v", c.want, c.have, got)
		}
	}
}

func TestValidateSettingBlocks(t *testing.T) {
	ctx := context.Background()
	block := func(key string, value bool, values bool) settingModel {
		m := settingModel{Key: types.StringValue(key), Value: types.StringNull(),
			Values: types.ListNull(types.StringType), FieldValues: types.ListNull(types.MapType{ElemType: types.StringType})}
		if value {
			m.Value = types.StringValue("v")
		}
		if values {
			m.Values = types.ListValueMust(types.StringType, nil)
		}
		return m
	}
	var ok diag.Diagnostics
	validateSettingBlocks(ctx, []settingModel{block("a", true, false), block("b", false, true)}, &ok)
	if ok.HasError() {
		t.Errorf("valid blocks: %v", ok)
	}
	for name, blocks := range map[string][]settingModel{
		"duplicate key": {block("a", true, false), block("a", true, false)},
		"none set":      {block("a", false, false)},
		"two set":       {block("a", true, true)},
		"secured key":   {block("sonar.auth.github.clientSecret.secured", true, false)},
	} {
		var d diag.Diagnostics
		validateSettingBlocks(ctx, blocks, &d)
		if !d.HasError() {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestEditionRank(t *testing.T) {
	ce, dev, ee := &serverInfo{Edition: "community"}, &serverInfo{Edition: "developer"}, &serverInfo{Edition: "enterprise"}
	if ce.hasEdition("developer") || !dev.hasEdition("developer") || !ee.hasEdition("developer") || dev.hasEdition("enterprise") {
		t.Error("hasEdition")
	}
	if !(&serverInfo{Edition: "datacenter"}).hasEdition("enterprise") {
		t.Error("datacenter includes enterprise features")
	}
}

// The keyed mutex serializes holders of one key, lets other keys run in
// parallel, and forgets keys nobody holds.
func TestKeyedMutex(t *testing.T) {
	k := newKeyedMutex()
	var inA, maxA atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			unlock := k.lock("a")
			defer unlock()
			n := inA.Add(1)
			for {
				m := maxA.Load()
				if n <= m || maxA.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			inA.Add(-1)
		})
	}
	// A different key is not blocked by "a".
	unlockA := k.lock("a")
	done := make(chan struct{})
	go func() { k.lock("b")(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal(`lock("b") blocked behind lock("a")`)
	}
	unlockA()
	wg.Wait()
	if maxA.Load() != 1 {
		t.Errorf("%d holders of key a at once", maxA.Load())
	}
	k.mu.Lock()
	n := len(k.m)
	k.mu.Unlock()
	if n != 0 {
		t.Errorf("%d keys retained after release", n)
	}
}

func TestWaitFor(t *testing.T) {
	ctx := context.Background()
	calls := 0
	err := waitFor(ctx, 5*time.Second, func(context.Context) (bool, error) { calls++; return calls == 3, nil })
	if err != nil || calls != 3 {
		t.Errorf("waitFor = %v after %d calls", err, calls)
	}
	boom := errors.New("boom")
	if err := waitFor(ctx, time.Second, func(context.Context) (bool, error) { return false, boom }); !errors.Is(err, boom) {
		t.Errorf("waitFor should return the check's error, got %v", err)
	}
	if err := waitFor(ctx, 300*time.Millisecond, func(context.Context) (bool, error) { return false, nil }); !errors.Is(err, errNotVisible) {
		t.Errorf("waitFor should time out with errNotVisible, got %v", err)
	}
}
