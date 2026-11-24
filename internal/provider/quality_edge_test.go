package provider

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

// Edge cases of quality gates, quality profiles and portfolios. The fake
// responses reproduce SonarQube 26.11 (Community) and 2026.5 (Enterprise).

// memPrivate is an in-memory private state.
type memPrivate map[string][]byte

func (m memPrivate) GetKey(_ context.Context, k string) ([]byte, diag.Diagnostics) { return m[k], nil }
func (m memPrivate) SetKey(_ context.Context, k string, v []byte) diag.Diagnostics {
	if len(v) == 0 {
		delete(m, k)
	} else {
		m[k] = v
	}
	return nil
}

func TestPreviousDefault(t *testing.T) {
	ctx := context.Background()
	p := memPrivate{}
	if previousDefault(ctx, p) != "" || previousDefault(ctx, nil) != "" {
		t.Error("nothing remembered")
	}
	rememberDefault(ctx, p, `Team "gate"`)
	if got := previousDefault(ctx, p); got != `Team "gate"` {
		t.Errorf("remembered = %q", got)
	}
	rememberDefault(ctx, p, "")
	if previousDefault(ctx, p) != "" {
		t.Error("forgetting failed")
	}
	p[previousDefaultKey] = []byte("not json")
	if previousDefault(ctx, p) != "" {
		t.Error("garbage must read as nothing")
	}
	if rememberDefault(ctx, nil, "x") != nil {
		t.Error("nil private state")
	}

	fallback := func() (string, error) { return "built-in", nil }
	exists := func(n string) bool { return n == "Team" || n == "self" }
	for _, c := range []struct{ remembered, want string }{
		{"Team", "Team"},
		{"", "built-in"},
		{"Deleted", "built-in"}, // the remembered default no longer exists
		{"self", "built-in"},    // never restore the gate that is being released
	} {
		if got, _ := restoreTarget(c.remembered, "self", exists, fallback); got != c.want {
			t.Errorf("restoreTarget(%q) = %q, want %q", c.remembered, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Quality gates

func gateList(gates ...map[string]any) func(*http.Request) (int, any) {
	return ok(map[string]any{"qualitygates": gates})
}

func TestBuiltInGate(t *testing.T) {
	gate := func(name string, builtIn bool) *sonarqube.QualitygatesListResponseQualitygate {
		return &sonarqube.QualitygatesListResponseQualitygate{Name: name, IsBuiltIn: builtIn}
	}
	for _, c := range []struct {
		name  string
		gates []*sonarqube.QualitygatesListResponseQualitygate
		want  string
	}{
		// Enterprise ships two built-in gates; "Sonar way" wins whatever the order.
		{"enterprise", []*sonarqube.QualitygatesListResponseQualitygate{gate("Sonar way for Agentic AI", true), gate("Mine", false), gate("Sonar way", true)}, "Sonar way"},
		{"renamed", []*sonarqube.QualitygatesListResponseQualitygate{gate("Mine", false), gate("Other built-in", true)}, "Other built-in"},
		{"none", []*sonarqube.QualitygatesListResponseQualitygate{gate("Mine", false)}, ""},
	} {
		got, err := builtInGate(c.gates)
		if got != c.want || (c.want == "") != (err != nil) {
			t.Errorf("%s: builtInGate = %q, %v", c.name, got, err)
		}
	}
}

func gateDefaults(t *testing.T, gates ...map[string]any) (*fakeServer, *qualityGateResource) {
	f, c := newFakeServer(t, map[string]func(*http.Request) (int, any){
		"GET /api/qualitygates/list":            gateList(gates...),
		"POST /api/qualitygates/set_as_default": ok(nil),
	})
	return f, &qualityGateResource{resourceBase{p: &providerData{client: c}}}
}

func defaultsSet(f *fakeServer, path string) []string {
	var out []string
	for _, c := range f.callsTo("POST", path) {
		out = append(out, c.form.Get("name")+c.form.Get("qualityProfile"))
	}
	return out
}

func TestQualityGateDefaults(t *testing.T) {
	ctx := context.Background()
	gates := []map[string]any{
		{"name": "Sonar way", "isBuiltIn": true},
		{"name": "Team gate", "isDefault": true},
		{"name": "Mine"},
	}
	f, r := gateDefaults(t, gates...)
	if prev, err := r.makeDefault(ctx, "Mine"); err != nil || prev != "Team gate" {
		t.Errorf("makeDefault = %q, %v; want the replaced default", prev, err)
	}
	if prev, _ := r.makeDefault(ctx, "Team gate"); prev != "" {
		t.Errorf("already default: prev = %q", prev)
	}
	if got := defaultsSet(f, "/api/qualitygates/set_as_default"); strings.Join(got, ",") != "Mine,Team gate" {
		t.Errorf("set_as_default calls = %v", got)
	}

	// Releasing restores the remembered default...
	f, r = gateDefaults(t, map[string]any{"name": "Sonar way", "isBuiltIn": true}, map[string]any{"name": "Team gate"},
		map[string]any{"name": "Mine", "isDefault": true})
	if err := r.releaseDefault(ctx, "Mine", "Team gate"); err != nil {
		t.Fatal(err)
	}
	// ...or the built-in gate when it is gone or unknown.
	if err := r.releaseDefault(ctx, "Mine", "Deleted gate"); err != nil {
		t.Fatal(err)
	}
	if err := r.releaseDefault(ctx, "Mine", ""); err != nil {
		t.Fatal(err)
	}
	if got := defaultsSet(f, "/api/qualitygates/set_as_default"); strings.Join(got, ",") != "Team gate,Sonar way,Sonar way" {
		t.Errorf("set_as_default calls = %v", got)
	}

	// Not the default (any more): nothing to release.
	f, r = gateDefaults(t, map[string]any{"name": "Sonar way", "isBuiltIn": true, "isDefault": true}, map[string]any{"name": "Mine"})
	if err := r.releaseDefault(ctx, "Mine", "Team gate"); err != nil || len(f.callsTo("POST", "/api/qualitygates/set_as_default")) != 0 {
		t.Errorf("release of a non-default gate: %v, %d calls", err, len(f.callsTo("POST", "/api/qualitygates/set_as_default")))
	}
}

func importGate(t *testing.T, show map[string]any) diag.Diagnostics {
	t.Helper()
	ctx := context.Background()
	_, c := newFakeServer(t, map[string]func(*http.Request) (int, any){"GET /api/qualitygates/show": ok(show)})
	r := &qualityGateResource{resourceBase{p: &providerData{client: c}}}
	var sr resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	st := tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}
	resp := resource.ImportStateResponse{State: st}
	r.ImportState(ctx, resource.ImportStateRequest{ID: show["name"].(string)}, &resp)
	return resp.Diagnostics
}

func TestQualityGateImport(t *testing.T) {
	if d := importGate(t, map[string]any{"name": "Sonar way", "isBuiltIn": true}); !d.HasError() ||
		!strings.Contains(d.Errors()[0].Detail(), "copy_from") {
		t.Errorf("built-in gate: %v", d)
	}
	if d := importGate(t, map[string]any{"name": "Mine", "conditions": []map[string]string{
		{"id": "2", "metric": "new_violations", "op": "GT", "error": "0"},
		{"id": "1", "metric": "coverage", "op": "LT", "error": "80.0"},
	}}); d.HasError() {
		t.Errorf("own gate: %v", d)
	}
}

// ---------------------------------------------------------------------------
// Quality profiles

func TestBuiltInDefaultProfile(t *testing.T) {
	profile := func(name, lang string, builtIn bool) *sonarqube.QualityprofilesSearchResponseProfile {
		return &sonarqube.QualityprofilesSearchResponseProfile{Name: name, Language: lang, IsBuiltIn: builtIn}
	}
	for _, c := range []struct {
		name     string
		profiles []*sonarqube.QualityprofilesSearchResponseProfile
		want     string
	}{
		// SonarQube 2026: no "Sonar way"; the first built-in is the install default.
		{"2026", []*sonarqube.QualityprofilesSearchResponseProfile{
			profile("Mine", "java", false), profile("Sonar way core", "java", true),
			profile("Sonar way extended", "java", true), profile("Sonar way comprehensive", "java", true),
		}, "Sonar way core"},
		{"legacy", []*sonarqube.QualityprofilesSearchResponseProfile{profile("FindBugs", "java", true), profile("Sonar way", "java", true)}, "Sonar way"},
		{"other language only", []*sonarqube.QualityprofilesSearchResponseProfile{profile("Sonar way", "js", true)}, ""},
	} {
		got, err := builtInDefaultProfile(c.profiles, "java")
		if got != c.want || (c.want == "") != (err != nil) {
			t.Errorf("%s: builtInDefaultProfile = %q, %v", c.name, got, err)
		}
	}
}

func TestQualityProfileDefaults(t *testing.T) {
	ctx := context.Background()
	profiles := func(defaultName string) func(*http.Request) (int, any) {
		var list []map[string]any
		for _, p := range []struct {
			key, name string
			builtIn   bool
		}{{"k-core", "Sonar way core", true}, {"k-comp", "Sonar way comprehensive", true}, {"k-team", "Team", false}, {"k-mine", "Mine", false}} {
			list = append(list, map[string]any{"key": p.key, "name": p.name, "language": "java", "isBuiltIn": p.builtIn, "isDefault": p.name == defaultName})
		}
		return ok(map[string]any{"profiles": list})
	}
	setup := func(defaultName string) (*fakeServer, *qualityProfileResource) {
		f, c := newFakeServer(t, map[string]func(*http.Request) (int, any){
			"GET /api/qualityprofiles/search":       profiles(defaultName),
			"POST /api/qualityprofiles/set_default": ok(nil),
		})
		return f, &qualityProfileResource{resourceBase{p: &providerData{client: c}}}
	}
	mine := &qualityProfileModel{ID: types.StringValue("k-mine"), Name: types.StringValue("Mine"), Language: types.StringValue("java")}

	f, r := setup("Team")
	if prev, err := r.makeDefault(ctx, mine); err != nil || prev != "Team" {
		t.Errorf("makeDefault = %q, %v", prev, err)
	}
	if form := f.callsTo("POST", "/api/qualityprofiles/set_default")[0].form; form.Get("qualityProfile") != "Mine" || form.Get("language") != "java" {
		t.Errorf("set_default form = %v", form)
	}

	f, r = setup("Mine")
	for _, remembered := range []string{"Team", "Gone", ""} {
		if err := r.releaseDefault(ctx, mine, remembered); err != nil {
			t.Fatal(err)
		}
	}
	if got := defaultsSet(f, "/api/qualityprofiles/set_default"); strings.Join(got, ",") != "Team,Sonar way core,Sonar way core" {
		t.Errorf("restored defaults = %v", got)
	}

	f, r = setup("Sonar way comprehensive")
	if err := r.releaseDefault(ctx, mine, "Team"); err != nil || len(f.callsTo("POST", "/api/qualityprofiles/set_default")) != 0 {
		t.Errorf("release of a non-default profile: %v", err)
	}
}

func TestQualityProfileImportBuiltIn(t *testing.T) {
	ctx := context.Background()
	_, c := newFakeServer(t, map[string]func(*http.Request) (int, any){
		"GET /api/qualityprofiles/search": ok(map[string]any{"profiles": []map[string]any{
			{"key": "k-core", "name": "Sonar way core", "language": "java", "isBuiltIn": true},
			{"key": "k-mine", "name": "Mine", "language": "java"},
		}}),
	})
	r := &qualityProfileResource{resourceBase{p: &providerData{client: c}}}
	var sr resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	imp := func(id string) diag.Diagnostics {
		st := tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}
		resp := resource.ImportStateResponse{State: st}
		r.ImportState(ctx, resource.ImportStateRequest{ID: id}, &resp)
		return resp.Diagnostics
	}
	for _, id := range []string{"java/Sonar way core", "k-core"} {
		if d := imp(id); !d.HasError() || !strings.Contains(d.Errors()[0].Summary(), "built-in") {
			t.Errorf("import %s: %v", id, d)
		}
	}
	if d := imp("java/Mine"); d.HasError() {
		t.Errorf("own profile: %v", d)
	}
}

// ---------------------------------------------------------------------------
// Rule parameters

func TestParamsProblems(t *testing.T) {
	for in, want := range map[string]string{
		"":                       "",
		"max=120":                "",
		"max=120;":               "", // a trailing separator is harmless
		"format=^[a-z]+=$;max=3": "",
		"max":                    "no '='",
		"=120":                   "no key",
		"max = 120":              "spaces",
		"max=120 ":               "spaces",
		" max=120":               "spaces",
		"max=":                   "empty value",
		"max=1;max=2":            "more than once",
	} {
		got := strings.Join(paramsProblems(in), " | ")
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("paramsProblems(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckParams(t *testing.T) {
	ctx := context.Background()
	_, c := newFakeServer(t, map[string]func(*http.Request) (int, any){
		"GET /api/rules/show": ok(map[string]any{
			"rule": map[string]any{"key": "java:S138", "params": []map[string]string{{"key": "max", "defaultValue": "75"}}},
			"actives": []map[string]any{
				{"qProfile": "other", "params": []map[string]string{{"key": "max", "value": "200"}}},
				{"qProfile": "prof", "params": []map[string]string{{"key": "max", "value": "120"}}},
			},
		}),
	})
	r := &activateRuleResource{resourceBase{p: &providerData{client: c}}}
	m := func(params string) *activateRuleModel {
		return &activateRuleModel{Key: types.StringValue("prof"), Rule: types.StringValue("java:S138"), Params: types.StringValue(params)}
	}
	if err := r.checkParams(ctx, m("max=120")); err != nil {
		t.Errorf("applied: %v", err)
	}
	if err := r.checkParams(ctx, &activateRuleModel{Key: types.StringValue("prof"), Rule: types.StringValue("x"), Params: types.StringNull()}); err != nil {
		t.Errorf("no params: %v", err)
	}
	// SonarQube answers 204 to an unknown key and ignores it.
	if err := r.checkParams(ctx, m("max=120;Max=3")); err == nil || !strings.Contains(err.Error(), `no parameter "Max" (its parameters: max)`) {
		t.Errorf("unknown key: %v", err)
	}
	if err := r.checkParams(ctx, m("max=121")); err == nil || !strings.Contains(err.Error(), `stored max="120", not "121"`) {
		t.Errorf("other value: %v", err)
	}
}
