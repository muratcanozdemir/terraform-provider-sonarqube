package provider

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// Tests of resource logic against the fake SonarQube in edge_test.go: the
// code paths that otherwise only the acceptance tests reach.

// ---------------------------------------------------------------------------
// sonarqube_permissions

func permModel() permissionsModel {
	n := types.StringNull()
	return permissionsModel{ID: n, LoginName: n, GroupName: n, SpecialGroupName: n, ProjectKey: n, TemplateID: n, TemplateName: n,
		Permissions: types.SetNull(types.StringType)}
}

func TestPermissionsCurrent(t *testing.T) {
	ctx := context.Background()

	t.Run("group on a project, across pages, case-insensitive", func(t *testing.T) {
		f, c := newFakeServer(t, map[string]func(*http.Request) (int, any){
			"GET /api/permissions/groups": func(r *http.Request) (int, any) {
				page, _ := strconv.Atoi(r.URL.Query().Get("p"))
				size, _ := strconv.Atoi(r.URL.Query().Get("ps"))
				var groups []principalPerms
				if page == 1 {
					for i := range size {
						groups = append(groups, principalPerms{Name: fmt.Sprintf("devs-%d", i), Permissions: []string{"user"}})
					}
				} else {
					groups = []principalPerms{{Name: "DEVS", Permissions: []string{"admin", "issueadmin"}}}
				}
				return 200, map[string]any{"groups": groups, "paging": map[string]int{"pageIndex": page, "pageSize": size, "total": size + 1}}
			},
		})
		r := &permissionsResource{resourceBase{p: &providerData{client: c}}}
		m := permModel()
		m.GroupName, m.ProjectKey = types.StringValue("devs"), types.StringValue("proj")
		got, err := r.current(ctx, &m)
		if err != nil || !slices.Equal(got, []string{"admin", "issueadmin"}) {
			t.Errorf("current = %v, %v", got, err)
		}
		calls := f.callsTo("GET", "/api/permissions/groups")
		if len(calls) != 2 || calls[0].query.Get("projectKey") != "proj" || calls[0].query.Get("q") != "devs" {
			t.Errorf("calls = %+v", calls)
		}
	})

	t.Run("user matches the exact login only", func(t *testing.T) {
		_, c := newFakeServer(t, map[string]func(*http.Request) (int, any){
			"GET /api/permissions/users": ok(map[string]any{
				"users":  []principalPerms{{Login: "ALICE", Permissions: []string{"admin"}}, {Login: "alice2", Permissions: []string{"scan"}}},
				"paging": map[string]int{"total": 2},
			}),
		})
		r := &permissionsResource{resourceBase{p: &providerData{client: c}}}
		m := permModel()
		m.LoginName = types.StringValue("alice")
		if got, err := r.current(ctx, &m); err != nil || got != nil {
			t.Errorf("current = %v, %v; logins are case-sensitive", got, err)
		}
	})

	t.Run("template ID wins over name; short names are not searched", func(t *testing.T) {
		f, c := newFakeServer(t, map[string]func(*http.Request) (int, any){
			"GET /api/permissions/template_groups": ok(map[string]any{"groups": []principalPerms{{Name: "qa", Permissions: []string{"user"}}}}),
		})
		r := &permissionsResource{resourceBase{p: &providerData{client: c}}}
		m := permModel()
		m.GroupName, m.TemplateID, m.TemplateName = types.StringValue("qa"), types.StringValue("AX1"), types.StringValue("ignored")
		got, err := r.current(ctx, &m)
		if err != nil || !slices.Equal(got, []string{"user"}) {
			t.Errorf("current = %v, %v", got, err)
		}
		q := f.callsTo("GET", "/api/permissions/template_groups")[0].query
		if q.Get("templateId") != "AX1" || q.Has("templateName") || q.Has("q") {
			t.Errorf("query = %v", q)
		}
	})

	t.Run("project creator reads the template", func(t *testing.T) {
		_, c := newFakeServer(t, map[string]func(*http.Request) (int, any){
			"GET /api/permissions/search_templates": ok(map[string]any{"permissionTemplates": []map[string]any{
				{"id": "AX1", "name": "Default", "permissions": []map[string]any{
					{"key": "admin", "withProjectCreator": true},
					{"key": "user", "withProjectCreator": false},
					{"key": "scan", "withProjectCreator": true},
				}},
			}}),
		})
		r := &permissionsResource{resourceBase{p: &providerData{client: c}}}
		m := permModel()
		m.SpecialGroupName, m.TemplateName = types.StringValue("project_creator"), types.StringValue("Default")
		if got, err := r.current(ctx, &m); err != nil || !slices.Equal(got, []string{"admin", "scan"}) {
			t.Errorf("current = %v, %v", got, err)
		}
		m.TemplateName = types.StringValue("Missing")
		if got, err := r.current(ctx, &m); err != nil || got != nil {
			t.Errorf("missing template: %v, %v", got, err)
		}
	})
}

func TestPermissionsEffectiveOnPublicProject(t *testing.T) {
	ctx := context.Background()
	for _, visibility := range []string{"public", "private"} {
		_, c := newFakeServer(t, map[string]func(*http.Request) (int, any){
			"GET /api/permissions/groups": ok(map[string]any{"groups": []principalPerms{{Name: "devs", Permissions: []string{"issueadmin"}}}}),
			"GET /api/components/show":    ok(map[string]any{"component": map[string]string{"key": "p", "visibility": visibility}}),
		})
		r := &permissionsResource{resourceBase{p: &providerData{client: c}}}
		m := permModel()
		m.GroupName, m.ProjectKey = types.StringValue("devs"), types.StringValue("p")
		got, err := r.effective(ctx, &m, []string{"user", "issueadmin"})
		if err != nil {
			t.Fatal(err)
		}
		// Only implicit permissions the configuration wants are added.
		want := []string{"issueadmin"}
		if visibility == "public" {
			want = append(want, "user")
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s: effective = %v, want %v", visibility, got, want)
		}
	}
}

func TestPermissionsApply(t *testing.T) {
	ctx := context.Background()
	routes := map[string]func(*http.Request) (int, any){}
	for _, p := range []string{"add_group", "remove_group", "add_user", "remove_user", "add_user_to_template", "remove_group_from_template",
		"add_project_creator_to_template", "remove_project_creator_from_template"} {
		routes["POST /api/permissions/"+p] = ok(nil)
	}
	routes["POST /api/permissions/remove_user"] = func(*http.Request) (int, any) {
		return 400, `{"errors":[{"msg":"Last administrator"}]}`
	}
	f, c := newFakeServer(t, routes)
	r := &permissionsResource{resourceBase{p: &providerData{client: c}}}

	group := permModel()
	group.GroupName, group.ProjectKey = types.StringValue("devs"), types.StringValue("p")
	var d diag.Diagnostics
	r.apply(ctx, &group, []string{"admin", "scan"}, []string{"user"}, &d)
	if d.HasError() || len(f.callsTo("POST", "/api/permissions/add_group")) != 2 || len(f.callsTo("POST", "/api/permissions/remove_group")) != 1 {
		t.Errorf("group apply: %v, calls %+v", d, f.calls)
	}
	if form := f.callsTo("POST", "/api/permissions/remove_group")[0].form; form.Get("groupName") != "devs" || form.Get("projectKey") != "p" || form.Get("permission") != "user" {
		t.Errorf("remove_group form = %v", form)
	}

	tpl := permModel()
	tpl.LoginName, tpl.TemplateName = types.StringValue("alice"), types.StringValue("T")
	r.apply(ctx, &tpl, []string{"admin"}, nil, &d)
	if form := f.callsTo("POST", "/api/permissions/add_user_to_template"); len(form) != 1 || form[0].form.Get("templateName") != "T" || form[0].form.Get("login") != "alice" {
		t.Errorf("add_user_to_template = %+v", form)
	}

	creator := permModel()
	creator.SpecialGroupName, creator.TemplateID = types.StringValue("project_creator"), types.StringValue("AX1")
	r.apply(ctx, &creator, []string{"admin"}, []string{"scan"}, &d)
	if len(f.callsTo("POST", "/api/permissions/add_project_creator_to_template")) != 1 ||
		len(f.callsTo("POST", "/api/permissions/remove_project_creator_from_template")) != 1 {
		t.Error("project creator endpoints not used")
	}
	if d.HasError() {
		t.Fatal(d)
	}

	// A failed revoke names the verb and permission.
	user := permModel()
	user.LoginName = types.StringValue("admin")
	r.apply(ctx, &user, nil, []string{"admin"}, &d)
	if !d.HasError() || !strings.Contains(d.Errors()[0].Detail(), "revoke admin") || !strings.Contains(d.Errors()[0].Detail(), "Last administrator") {
		t.Errorf("failed revoke: %v", d)
	}
}

// ---------------------------------------------------------------------------
// sonarqube_qualitygate

func cond(metric, op, threshold string) conditionModel {
	return conditionModel{ID: types.StringNull(), Metric: types.StringValue(metric), Op: types.StringValue(op), Threshold: types.StringValue(threshold)}
}

func TestSyncConditions(t *testing.T) {
	ctx := context.Background()
	f, c := newFakeServer(t, map[string]func(*http.Request) (int, any){
		"GET /api/qualitygates/show": ok(map[string]any{"name": "G", "conditions": []map[string]string{
			{"id": "1", "metric": "coverage", "op": "LT", "error": "80"},
			{"id": "2", "metric": "duplicated_lines_density", "op": "GT", "error": "3"},
			{"id": "3", "metric": "gone_metric", "op": "GT", "error": "0"},
			{"id": "4", "metric": "stale", "op": "GT", "error": "0"},
		}}),
		"POST /api/qualitygates/create_condition": ok(map[string]string{"id": "9"}),
		"POST /api/qualitygates/update_condition": ok(nil),
		"POST /api/qualitygates/delete_condition": func(r *http.Request) (int, any) {
			if r.PostForm.Get("id") == "3" {
				return 404, `{"errors":[{"msg":"No quality gate condition with id '3'"}]}`
			}
			return 204, nil
		},
	})
	r := &qualityGateResource{resourceBase{p: &providerData{client: c, locks: newKeyedMutex()}}}
	var d diag.Diagnostics
	r.syncConditions(ctx, "G", []conditionModel{
		cond("coverage", "LT", "80"),                // unchanged
		cond("duplicated_lines_density", "GT", "5"), // threshold changed
		cond("new_bugs", "GT", "0"),                 // new
	}, &d)
	if d.HasError() {
		t.Fatal(d)
	}
	if calls := f.callsTo("POST", "/api/qualitygates/create_condition"); len(calls) != 1 || calls[0].form.Get("metric") != "new_bugs" || calls[0].form.Get("gateName") != "G" {
		t.Errorf("create_condition = %+v", calls)
	}
	if calls := f.callsTo("POST", "/api/qualitygates/update_condition"); len(calls) != 1 || calls[0].form.Get("id") != "2" || calls[0].form.Get("error") != "5" {
		t.Errorf("update_condition = %+v", calls)
	}
	var deleted []string
	for _, c := range f.callsTo("POST", "/api/qualitygates/delete_condition") {
		deleted = append(deleted, c.form.Get("id"))
	}
	slices.Sort(deleted)
	// A condition already deleted elsewhere (404) is not an error.
	if !slices.Equal(deleted, []string{"3", "4"}) {
		t.Errorf("deleted = %v", deleted)
	}
}

func TestSyncConditionsStopsOnError(t *testing.T) {
	f, c := newFakeServer(t, map[string]func(*http.Request) (int, any){
		"GET /api/qualitygates/show": ok(map[string]any{"name": "G", "conditions": []map[string]string{{"id": "1", "metric": "stale", "op": "GT", "error": "0"}}}),
		"POST /api/qualitygates/create_condition": func(*http.Request) (int, any) {
			return 400, `{"errors":[{"msg":"Metric 'nope' does not exist"}]}`
		},
	})
	r := &qualityGateResource{resourceBase{p: &providerData{client: c, locks: newKeyedMutex()}}}
	var d diag.Diagnostics
	r.syncConditions(context.Background(), "G", []conditionModel{cond("nope", "GT", "0")}, &d)
	if !d.HasError() || !strings.Contains(d.Errors()[0].Summary(), "nope") {
		t.Errorf("diags = %v", d)
	}
	// Nothing is deleted once a write has failed.
	if n := len(f.callsTo("POST", "/api/qualitygates/delete_condition")); n != 0 {
		t.Errorf("%d deletes after a failed create", n)
	}
}

func TestQualityGateSetDefault(t *testing.T) {
	ctx := context.Background()
	f, c := newFakeServer(t, map[string]func(*http.Request) (int, any){
		"GET /api/qualitygates/list": ok(map[string]any{"qualitygates": []map[string]any{
			{"name": "Mine", "isBuiltIn": false}, {"name": "Sonar way", "isBuiltIn": true},
		}}),
		"POST /api/qualitygates/set_as_default": ok(nil),
	})
	r := &qualityGateResource{resourceBase{p: &providerData{client: c}}}
	if err := r.setDefault(ctx, "Mine", true); err != nil {
		t.Fatal(err)
	}
	// Turning default off hands it back to the built-in gate.
	if err := r.setDefault(ctx, "Mine", false); err != nil {
		t.Fatal(err)
	}
	calls := f.callsTo("POST", "/api/qualitygates/set_as_default")
	if len(calls) != 2 || calls[0].form.Get("name") != "Mine" || calls[1].form.Get("name") != "Sonar way" {
		t.Errorf("set_as_default = %+v", calls)
	}

	_, c = newFakeServer(t, map[string]func(*http.Request) (int, any){
		"GET /api/qualitygates/list": ok(map[string]any{"qualitygates": []map[string]any{{"name": "Mine"}}}),
	})
	if _, err := builtInGate(ctx, c); err == nil {
		t.Error("builtInGate without a built-in gate")
	}
}

// ---------------------------------------------------------------------------
// sonarqube_plugin

func TestPluginStatus(t *testing.T) {
	_, c := newFakeServer(t, map[string]func(*http.Request) (int, any){
		"GET /api/plugins/installed": ok(map[string]any{"plugins": []map[string]string{{"key": "installed"}, {"key": "updating"}, {"key": "removing"}}}),
		"GET /api/plugins/pending": ok(map[string]any{
			"installing": []map[string]string{{"key": "installing"}},
			"updating":   []map[string]string{{"key": "updating"}},
			"removing":   []map[string]string{{"key": "removing"}},
		}),
	})
	r := &pluginResource{resourceBase{p: &providerData{client: c}}}
	for key, want := range map[string][2]bool{
		"installed":  {true, false},
		"installing": {true, true},
		"updating":   {true, true},
		"removing":   {false, true},
		"absent":     {false, false},
	} {
		present, pending, err := r.status(context.Background(), key)
		if err != nil || present != want[0] || pending != want[1] {
			t.Errorf("status(%s) = present %v, pending %v, %v; want %v", key, present, pending, err, want)
		}
	}
	p := &pendingPlugins{Installing: []struct{ Key string }{{"a"}}}
	if !p.installing("a") || p.installing("b") {
		t.Error("installing")
	}
}

// ---------------------------------------------------------------------------
// sonarqube_user_token plan-time rotation

func tokenPlanFixture(t *testing.T, state, plan userTokenModel) (*userTokenResource, resource.ModifyPlanRequest, *resource.ModifyPlanResponse) {
	t.Helper()
	ctx := context.Background()
	r := &userTokenResource{}
	var sr resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	empty := tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)
	st := tfsdk.State{Schema: sr.Schema, Raw: empty}
	pl := tfsdk.Plan{Schema: sr.Schema, Raw: empty}
	if d := st.Set(ctx, &state); d.HasError() {
		t.Fatal(d)
	}
	if d := pl.Set(ctx, &plan); d.HasError() {
		t.Fatal(d)
	}
	return r, resource.ModifyPlanRequest{State: st, Plan: pl}, &resource.ModifyPlanResponse{Plan: pl}
}

func tokenModel(expires string, days int64, metadataOnly bool) userTokenModel {
	m := userTokenModel{
		ID: types.StringValue("id"), Name: types.StringValue("ci"), LoginName: types.StringValue("alice"),
		ExpirationDate: types.StringValue(expires), ExpirationDays: types.Int64Null(), RotateBefore: types.Int64Value(7),
		MetadataOnly: types.BoolValue(metadataOnly), Token: types.StringValue("secret"),
		Type: types.StringValue("USER_TOKEN"), ProjectKey: types.StringNull(),
	}
	if days > 0 {
		m.ExpirationDays = types.Int64Value(days)
	}
	return m
}

func TestTokenModifyPlanRotation(t *testing.T) {
	ctx := context.Background()
	orig := today
	t.Cleanup(func() { today = orig })
	today = func() time.Time { return time.Date(2027, 1, 25, 0, 0, 0, 0, time.UTC) }

	plannedToken := func(resp *resource.ModifyPlanResponse) types.String {
		var v types.String
		resp.Plan.GetAttribute(ctx, path.Root("token"), &v)
		return v
	}

	// Renewable and within rotate_before_days: replaced, values unknown.
	r, req, resp := tokenPlanFixture(t, tokenModel("2027-01-31", 30, false), tokenModel("2027-01-31", 30, false))
	r.ModifyPlan(ctx, req, resp)
	if resp.Diagnostics.HasError() || len(resp.RequiresReplace) != 1 || !plannedToken(resp).IsUnknown() {
		t.Errorf("due rotation: replace %v, token %v, %v", resp.RequiresReplace, plannedToken(resp), resp.Diagnostics)
	}

	// Renewable but not yet due: no change.
	r, req, resp = tokenPlanFixture(t, tokenModel("2027-03-01", 30, false), tokenModel("2027-03-01", 30, false))
	r.ModifyPlan(ctx, req, resp)
	if len(resp.RequiresReplace) != 0 || plannedToken(resp).ValueString() != "secret" || len(resp.Diagnostics) != 0 {
		t.Errorf("not due: replace %v, %v", resp.RequiresReplace, resp.Diagnostics)
	}

	// Not renewable (no expiration_days) and expired: a warning, no replacement.
	r, req, resp = tokenPlanFixture(t, tokenModel("2027-01-20", 0, false), tokenModel("2027-01-20", 0, false))
	r.ModifyPlan(ctx, req, resp)
	if len(resp.RequiresReplace) != 0 || resp.Diagnostics.WarningsCount() != 1 || resp.Diagnostics.HasError() {
		t.Errorf("expired, not renewable: replace %v, %v", resp.RequiresReplace, resp.Diagnostics)
	}

	// An adopted (metadata_only) token is never rotated, even with
	// expiration_days, since the provider cannot reissue it.
	r, req, resp = tokenPlanFixture(t, tokenModel("2027-01-31", 30, true), tokenModel("2027-01-31", 30, true))
	r.ModifyPlan(ctx, req, resp)
	if len(resp.RequiresReplace) != 0 || resp.Diagnostics.HasError() {
		t.Errorf("adopted token: replace %v, %v", resp.RequiresReplace, resp.Diagnostics)
	}
}

func TestTokenModifyPlanAdopted(t *testing.T) {
	ctx := context.Background()
	orig := today
	t.Cleanup(func() { today = orig })
	today = func() time.Time { return time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC) }

	// Renaming an adopted token would revoke it: refused.
	state, plan := tokenModel("2027-06-01", 0, true), tokenModel("2027-06-01", 0, true)
	plan.Name = types.StringValue("renamed")
	r, req, resp := tokenPlanFixture(t, state, plan)
	r.ModifyPlan(ctx, req, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "metadata_only") {
		t.Errorf("renamed adopted token: %v", resp.Diagnostics)
	}

	// Releasing metadata_only makes the stored token unknown for dependents.
	state, plan = tokenModel("2027-06-01", 0, true), tokenModel("2027-06-01", 0, false)
	r, req, resp = tokenPlanFixture(t, state, plan)
	r.ModifyPlan(ctx, req, resp)
	var tok types.String
	resp.Plan.GetAttribute(ctx, path.Root("token"), &tok)
	if !tok.IsUnknown() {
		t.Errorf("token after metadata_only change = %v, want unknown", tok)
	}

	// Create and destroy plans are left alone.
	r, req, resp = tokenPlanFixture(t, state, plan)
	req.State.Raw = tftypes.NewValue(req.State.Schema.Type().TerraformType(ctx), nil)
	r.ModifyPlan(ctx, req, resp)
	if len(resp.Diagnostics) != 0 || len(resp.RequiresReplace) != 0 {
		t.Errorf("create plan: %v", resp.Diagnostics)
	}
}
