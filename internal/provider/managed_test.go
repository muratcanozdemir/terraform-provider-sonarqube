package provider

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

// Users, groups and permissions on an instance whose users and groups are
// provisioned by GitHub (or GitLab, or SCIM). The fake responses reproduce
// those of SonarQube 2026.5 with GitHub AUTO_PROVISIONING enabled.

const managedRefusal = `{"message":"Operation not allowed when the instance is externally managed."}`

func managedRoutes(managed bool) map[string]func(*http.Request) (int, any) {
	return map[string]func(*http.Request) (int, any){
		"GET /api/v2/authorizations/groups": func(r *http.Request) (int, any) {
			if r.URL.Query().Get("managed") != "" && !managed {
				return 400, `{"message":"The 'managed' parameter is only available for managed instances."}`
			}
			return 200, map[string]any{"groups": []any{}, "page": map[string]int{"pageIndex": 1, "pageSize": 1, "total": 0}}
		},
	}
}

// managedProvider returns provider data whose instance is, or is not,
// externally managed.
func managedProvider(t *testing.T, managed bool) (*fakeServer, *providerData) {
	f, c := newFakeServer(t, managedRoutes(managed))
	return f, &providerData{client: c, locks: newKeyedMutex(), managed: &managedCache{client: c}}
}

func TestManagedCache(t *testing.T) {
	ctx := context.Background()
	for _, managed := range []bool{true, false} {
		f, p := managedProvider(t, managed)
		for range 3 {
			got, err := p.managed.get(ctx)
			if err != nil || got != managed {
				t.Fatalf("managed=%v: get = %v, %v", managed, got, err)
			}
		}
		if n := len(f.callsTo("GET", "/api/v2/authorizations/groups")); n != 1 {
			t.Errorf("managed=%v: detected %d times, want once", managed, n)
		}
	}

	// A server without the v2 API is not managed.
	_, c := newFakeServer(t, nil)
	if got, err := (&managedCache{client: c}).get(ctx); err != nil || got {
		t.Errorf("no v2 API: %v, %v", got, err)
	}

	// Another error is returned and retried later, not cached.
	var fail atomic.Bool
	fail.Store(true)
	_, c = newFakeServer(t, map[string]func(*http.Request) (int, any){
		"GET /api/v2/authorizations/groups": func(*http.Request) (int, any) {
			if fail.Load() {
				return 500, `{"message":"boom"}`
			}
			return 200, map[string]any{"groups": []any{}}
		},
	})
	cache := &managedCache{client: c}
	if _, err := cache.get(ctx); err == nil {
		t.Fatal("500 not returned")
	}
	fail.Store(false)
	if got, err := cache.get(ctx); err != nil || !got {
		t.Errorf("after recovery: %v, %v", got, err)
	}
	// Another 400 is not mistaken for "not managed".
	_, c = newFakeServer(t, map[string]func(*http.Request) (int, any){
		"GET /api/v2/authorizations/groups": func(*http.Request) (int, any) { return 400, `{"message":"pageSize must be positive"}` },
	})
	if _, err := (&managedCache{client: c}).get(ctx); err == nil {
		t.Error("unrelated 400 treated as an answer")
	}
}

func TestRefuseIfManaged(t *testing.T) {
	ctx := context.Background()
	var d diag.Diagnostics
	(*providerData)(nil).refuseIfManaged(ctx, &d, "x")
	(&providerData{}).refuseIfManaged(ctx, &d, "x")
	_, unmanaged := managedProvider(t, false)
	unmanaged.refuseIfManaged(ctx, &d, "x")
	if len(d) != 0 {
		t.Errorf("unmanaged or undetectable: %v", d)
	}
	_, broken := newFakeServer(t, map[string]func(*http.Request) (int, any){
		"GET /api/v2/authorizations/groups": func(*http.Request) (int, any) { return 503, nil },
	})
	(&providerData{managed: &managedCache{client: broken}}).refuseIfManaged(ctx, &d, "x")
	if len(d) != 0 {
		t.Errorf("failed detection must not block the plan: %v", d)
	}
	_, managed := managedProvider(t, true)
	managed.refuseIfManaged(ctx, &d, `Creating group "devs"`)
	if !d.HasError() || d.Errors()[0].Summary() != `Creating group "devs" is not allowed: SonarQube is externally managed` ||
		!strings.Contains(d.Errors()[0].Detail(), "removed") {
		t.Errorf("managed: %v", d)
	}
}

func TestExternallyManagedError(t *testing.T) {
	refused := &sonarqube.ErrorResponse{StatusCode: 400, Method: "POST", URL: "http://sq/api/v2/authorizations/groups",
		Messages: []string{"Operation not allowed when the instance is externally managed."}}
	if !isExternallyManaged(refused) || isExternallyManaged(apiErr(400, "Group already exists")) || isExternallyManaged(apiErr(403, "externally managed")) {
		t.Error("isExternallyManaged")
	}
	if got := explain(refused); !strings.Contains(got, "probably enabled") || !strings.Contains(got, "identity provider") {
		t.Errorf("explain = %q", got)
	}
	if strings.Contains(explain(apiErr(400, "Group already exists")), "identity provider") {
		t.Error("hint on an unrelated error")
	}
}

// ---------------------------------------------------------------------------
// Plan fixtures

// planFor builds a ModifyPlan request for r from state and plan models; a
// nil model is a null object (create or destroy).
func planFor(t *testing.T, r resource.Resource, state, plan any) (resource.ModifyPlanRequest, *resource.ModifyPlanResponse) {
	t.Helper()
	ctx := context.Background()
	var sr resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	null := tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)
	st := tfsdk.State{Schema: sr.Schema, Raw: null}
	pl := tfsdk.Plan{Schema: sr.Schema, Raw: null}
	if state != nil {
		if d := st.Set(ctx, state); d.HasError() {
			t.Fatal(d)
		}
	}
	if plan != nil {
		if d := pl.Set(ctx, plan); d.HasError() {
			t.Fatal(d)
		}
	}
	return resource.ModifyPlanRequest{State: st, Plan: pl}, &resource.ModifyPlanResponse{Plan: pl}
}

func TestPlanChange(t *testing.T) {
	g := &groupResource{}
	m := &groupModel{ID: types.StringValue("u"), Name: types.StringValue("devs"), Description: types.StringNull()}
	req, _ := planFor(t, g, nil, m)
	if c, u := planChange(&g.resourceBase, req); c || u {
		t.Error("unconfigured provider reports a change")
	}
	g.p = &providerData{}
	for name, c := range map[string]struct {
		state, plan any
		create, upd bool
	}{
		"create":    {nil, m, true, false},
		"destroy":   {m, nil, false, false},
		"no change": {m, m, false, false},
		"update":    {m, &groupModel{ID: m.ID, Name: types.StringValue("ops"), Description: types.StringNull()}, false, true},
	} {
		req, _ := planFor(t, g, c.state, c.plan)
		if cr, up := planChange(&g.resourceBase, req); cr != c.create || up != c.upd {
			t.Errorf("%s: planChange = %v, %v", name, cr, up)
		}
	}
}

// ---------------------------------------------------------------------------
// sonarqube_group and sonarqube_group_member

func TestGroupModifyPlanManaged(t *testing.T) {
	ctx := context.Background()
	devs := &groupModel{ID: types.StringValue("u"), Name: types.StringValue("devs"), Description: types.StringNull()}
	renamed := &groupModel{ID: types.StringValue("u"), Name: types.StringValue("ops"), Description: types.StringNull()}
	for _, c := range []struct {
		name        string
		managed     bool
		state, plan any
		wantErr     string
	}{
		{"create on managed", true, nil, devs, `Creating group "devs"`},
		{"rename on managed", true, devs, renamed, `Changing group "ops"`},
		// A group created before provisioning was enabled keeps planning cleanly.
		{"unchanged on managed", true, devs, devs, ""},
		// SonarQube allows deleting groups on a managed instance.
		{"destroy on managed", true, devs, nil, ""},
		{"create on unmanaged", false, nil, devs, ""},
	} {
		_, p := managedProvider(t, c.managed)
		r := &groupResource{resourceBase{p: p}}
		req, resp := planFor(t, r, c.state, c.plan)
		r.ModifyPlan(ctx, req, resp)
		switch {
		case c.wantErr == "" && resp.Diagnostics.HasError():
			t.Errorf("%s: unexpected %v", c.name, resp.Diagnostics)
		case c.wantErr != "" && (!resp.Diagnostics.HasError() || !strings.HasPrefix(resp.Diagnostics.Errors()[0].Summary(), c.wantErr)):
			t.Errorf("%s: diags %v, want %q", c.name, resp.Diagnostics, c.wantErr)
		}
	}
}

func TestGroupMemberModifyPlanManaged(t *testing.T) {
	ctx := context.Background()
	m := &groupMemberModel{ID: types.StringValue("devs[alice]"), Name: types.StringValue("devs"), LoginName: types.StringValue("alice")}
	other := &groupMemberModel{ID: types.StringValue("devs[alice]"), Name: types.StringValue("ops"), LoginName: types.StringValue("alice")}
	_, p := managedProvider(t, true)
	r := &groupMemberResource{resourceBase{p: p}}
	for name, c := range map[string]struct {
		state, plan any
		err         bool
	}{
		"add":       {nil, m, true},
		"replace":   {m, other, true},
		"unchanged": {m, m, false},
		"remove":    {m, nil, false},
	} {
		req, resp := planFor(t, r, c.state, c.plan)
		r.ModifyPlan(ctx, req, resp)
		if resp.Diagnostics.HasError() != c.err {
			t.Errorf("%s: %v", name, resp.Diagnostics)
		}
	}
}

func groupMemberRoutes(deleteStatus int, deleteBody string) map[string]func(*http.Request) (int, any) {
	return map[string]func(*http.Request) (int, any){
		"GET /api/v2/authorizations/groups": ok(map[string]any{"groups": []map[string]any{
			{"id": "g-other", "name": "devs-extra"}, {"id": "g1", "name": "devs"},
		}, "page": map[string]int{"total": 2}}),
		"GET /api/v2/users-management/users": ok(map[string]any{"users": []map[string]any{
			{"id": "u-other", "login": "alice2", "active": true}, {"id": "u1", "login": "alice", "active": true},
		}, "page": map[string]int{"total": 2}}),
		"GET /api/v2/authorizations/group-memberships": ok(map[string]any{"groupMemberships": []map[string]string{
			{"id": "m1", "groupId": "g1", "userId": "u1"},
		}}),
		"DELETE /api/v2/authorizations/group-memberships/m1": func(*http.Request) (int, any) { return deleteStatus, deleteBody },
	}
}

func deleteMember(t *testing.T, routes map[string]func(*http.Request) (int, any)) (*fakeServer, resource.DeleteResponse) {
	t.Helper()
	ctx := context.Background()
	f, c := newFakeServer(t, routes)
	r := &groupMemberResource{resourceBase{p: &providerData{client: c}}}
	var sr resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	st := tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}
	st.Set(ctx, &groupMemberModel{ID: types.StringValue("devs[alice]"), Name: types.StringValue("devs"), LoginName: types.StringValue("alice")})
	resp := resource.DeleteResponse{State: st}
	r.Delete(ctx, resource.DeleteRequest{State: st}, &resp)
	return f, resp
}

func TestGroupMemberDelete(t *testing.T) {
	// Normal instance: the exact group and user are resolved among partial
	// matches, and the membership is deleted.
	f, resp := deleteMember(t, groupMemberRoutes(204, ""))
	if resp.Diagnostics.HasError() || len(f.callsTo("DELETE", "/api/v2/authorizations/group-memberships/m1")) != 1 {
		t.Errorf("delete: %v", resp.Diagnostics)
	}
	// Managed instance: SonarQube refuses; the destroy succeeds with a warning.
	_, resp = deleteMember(t, groupMemberRoutes(400, managedRefusal))
	if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 1 ||
		!strings.Contains(resp.Diagnostics.Warnings()[0].Detail(), "state only") {
		t.Errorf("managed delete: %v", resp.Diagnostics)
	}
	// Any other refusal is still an error.
	_, resp = deleteMember(t, groupMemberRoutes(403, `{"message":"Insufficient privileges"}`))
	if !resp.Diagnostics.HasError() {
		t.Error("403 swallowed")
	}
	// Already gone: nothing to do.
	routes := groupMemberRoutes(204, "")
	routes["GET /api/v2/authorizations/group-memberships"] = ok(map[string]any{"groupMemberships": []any{}})
	f, resp = deleteMember(t, routes)
	if resp.Diagnostics.HasError() || len(f.callsTo("DELETE", "/api/v2/authorizations/group-memberships/m1")) != 0 {
		t.Errorf("absent membership: %v", resp.Diagnostics)
	}
}

func TestGroupMemberImportID(t *testing.T) {
	ctx := context.Background()
	r := &groupMemberResource{}
	var sr resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	importID := func(id string) (group, login string, ok bool) {
		st := tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}
		resp := resource.ImportStateResponse{State: st}
		r.ImportState(ctx, resource.ImportStateRequest{ID: id}, &resp)
		if resp.Diagnostics.HasError() {
			return "", "", false
		}
		var m groupMemberModel
		resp.State.Get(ctx, &m)
		return m.Name.ValueString(), m.LoginName.ValueString(), true
	}
	for id, want := range map[string][2]string{
		"devs[alice]":            {"devs", "alice"},
		"my-org/team[octocat]":   {"my-org/team", "octocat"}, // GitHub team groups are named org/team
		"legacy[ops][bob]":       {"legacy[ops]", "bob"},
		"devs[alice.smith@x.io]": {"devs", "alice.smith@x.io"},
	} {
		if g, l, ok := importID(id); !ok || g != want[0] || l != want[1] {
			t.Errorf("import %q = %q, %q, %v; want %v", id, g, l, ok, want)
		}
	}
	for _, bad := range []string{"", "devs", "devs[]", "[alice]", "devs[alice", "devs]alice["} {
		if _, _, ok := importID(bad); ok {
			t.Errorf("import %q accepted", bad)
		}
	}
}

func TestFindGroup(t *testing.T) {
	ctx := context.Background()
	routes := map[string]func(*http.Request) (int, any){
		"GET /api/v2/authorizations/groups": ok(map[string]any{"groups": []map[string]any{
			{"id": "g1", "name": "Devs"}, {"id": "g2", "name": "devs-team"},
		}, "page": map[string]int{"total": 2}}),
		"GET /api/v2/authorizations/groups/g1": ok(map[string]any{"id": "g1", "name": "Devs"}),
	}
	// Names are looked up exactly, case included.
	_, c := newFakeServer(t, routes)
	if g, err := findGroupByName(ctx, c, "devs"); err != nil || g != nil {
		t.Errorf("findGroupByName(devs) = %+v, %v; names are case-sensitive", g, err)
	}
	if g, err := findGroupByName(ctx, c, "Devs"); err != nil || g == nil || g.ID != "g1" {
		t.Errorf("findGroupByName(Devs) = %+v, %v", g, err)
	}
	// findGroup takes a UUID first, then a name (on 404 or 400 for a non-UUID).
	if g, err := findGroup(ctx, c, "g1"); err != nil || g == nil || g.ID != "g1" {
		t.Errorf("findGroup(uuid) = %+v, %v", g, err)
	}
	routes["GET /api/v2/authorizations/groups/Devs"] = func(*http.Request) (int, any) { return 400, `{"message":"invalid UUID"}` }
	if g, err := findGroup(ctx, c, "Devs"); err != nil || g == nil || g.ID != "g1" {
		t.Errorf("findGroup(name) = %+v, %v", g, err)
	}
	routes["GET /api/v2/authorizations/groups/x"] = func(*http.Request) (int, any) { return 403, `{"message":"Insufficient privileges"}` }
	if _, err := findGroup(ctx, c, "x"); err == nil {
		t.Error("403 on the UUID lookup swallowed")
	}
}

// ---------------------------------------------------------------------------
// sonarqube_user and sonarqube_user_external_identity

func userState() *userModel {
	return &userModel{ID: types.StringValue("alice"), UUID: types.StringValue("u1"), LoginName: types.StringValue("alice"),
		Name: types.StringValue("Alice"), Email: types.StringNull(), Password: types.StringValue("old"), PasswordWO: types.StringNull(),
		PasswordWOVersion: types.StringNull(), IsLocal: types.BoolValue(true), SCMAccounts: stringSet(nil)}
}

func TestUserProfileChanges(t *testing.T) {
	s := userState()
	p := *s
	p.Password = types.StringValue("new")
	p.PasswordWOVersion = types.StringValue("2")
	if got := userProfileChanges(s, &p); got != nil {
		t.Errorf("password only: %v", got)
	}
	p.Name, p.Email, p.SCMAccounts = types.StringValue("A"), types.StringValue("a@x.io"), stringSet([]string{"gh-alice"})
	if got := userProfileChanges(s, &p); !slices.Equal(got, []string{"name", "email", "scm_accounts"}) {
		t.Errorf("profile: %v", got)
	}
	p = *s
	p.SCMAccounts = types.SetUnknown(types.StringType)
	if got := userProfileChanges(s, &p); got != nil {
		t.Errorf("unknown scm_accounts is not a change: %v", got)
	}
}

func TestUserModifyPlanManaged(t *testing.T) {
	ctx := context.Background()
	_, p := managedProvider(t, true)
	r := &userResource{resourceBase{p: p}}

	req, resp := planFor(t, r, nil, userState())
	r.ModifyPlan(ctx, req, resp)
	if !resp.Diagnostics.HasError() || !strings.HasPrefix(resp.Diagnostics.Errors()[0].Summary(), `Creating user "alice"`) {
		t.Errorf("create: %v", resp.Diagnostics)
	}

	// SonarQube still accepts a password change for a local user.
	plan := userState()
	plan.Password = types.StringValue("new")
	req, resp = planFor(t, r, userState(), plan)
	r.ModifyPlan(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("password change: %v", resp.Diagnostics)
	}

	plan.Email = types.StringValue("a@x.io")
	req, resp = planFor(t, r, userState(), plan)
	r.ModifyPlan(ctx, req, resp)
	if !resp.Diagnostics.HasError() || !strings.HasPrefix(resp.Diagnostics.Errors()[0].Summary(), `Changing email of user "alice"`) {
		t.Errorf("email change: %v", resp.Diagnostics)
	}

	// Deactivation (destroy) is allowed.
	req, resp = planFor(t, r, userState(), nil)
	r.ModifyPlan(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("destroy: %v", resp.Diagnostics)
	}

	// Unmanaged: no detection call is wasted on a password-only change, and
	// nothing is refused.
	f, up := managedProvider(t, false)
	r = &userResource{resourceBase{p: up}}
	plan = userState()
	plan.Password = types.StringValue("new")
	req, resp = planFor(t, r, userState(), plan)
	r.ModifyPlan(ctx, req, resp)
	if resp.Diagnostics.HasError() || len(f.calls) != 0 {
		t.Errorf("unmanaged password change: %v, %d calls", resp.Diagnostics, len(f.calls))
	}
}

func TestUserExternalIdentityModifyPlanManaged(t *testing.T) {
	ctx := context.Background()
	_, p := managedProvider(t, true)
	r := &userExternalIdentityResource{resourceBase{p: p}}
	m := &userExternalIdentityModel{ID: types.StringValue("alice"), LoginName: types.StringValue("alice"),
		ExternalIdentity: types.StringValue("alice-gh"), ExternalProvider: types.StringValue("github")}
	req, resp := planFor(t, r, nil, m)
	r.ModifyPlan(ctx, req, resp)
	if !resp.Diagnostics.HasError() {
		t.Error("setting an identity on a managed instance should fail the plan")
	}
	req, resp = planFor(t, r, m, m)
	r.ModifyPlan(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("unchanged: %v", resp.Diagnostics)
	}
}

func TestFindUser(t *testing.T) {
	ctx := context.Background()
	_, c := newFakeServer(t, map[string]func(*http.Request) (int, any){
		"GET /api/v2/users-management/users": func(r *http.Request) (int, any) {
			if r.URL.Query().Get("active") != "true" {
				return 400, `{"message":"expected active=true"}`
			}
			return 200, map[string]any{"users": []map[string]any{
				{"id": "u2", "login": "alice-gh", "active": true, "managed": true},
				{"id": "u1", "login": "alice", "active": true},
			}, "page": map[string]int{"total": 2}}
		},
		"GET /api/v2/users-management/users/u1":   ok(map[string]any{"id": "u1", "login": "alice", "active": true}),
		"GET /api/v2/users-management/users/gone": ok(map[string]any{"id": "gone", "login": "bob", "active": false}),
		"GET /api/v2/users-management/users/u404": func(*http.Request) (int, any) { return 404, `{"message":"User 'u404' not found"}` },
	})
	if u, err := findUser(ctx, c, "", "alice"); err != nil || u == nil || u.ID != "u1" {
		t.Errorf("by login among partial matches = %+v, %v", u, err)
	}
	if u, err := findUser(ctx, c, "", "Alice"); err != nil || u != nil {
		t.Errorf("logins are case-sensitive: %+v, %v", u, err)
	}
	if u, err := findUser(ctx, c, "u1", "ignored"); err != nil || u == nil || u.Login != "alice" {
		t.Errorf("by UUID = %+v, %v", u, err)
	}
	// A deactivated or deleted user is reported missing.
	for _, id := range []string{"gone", "u404"} {
		if u, err := findUser(ctx, c, id, ""); err != nil || u != nil {
			t.Errorf("findUser(%s) = %+v, %v", id, u, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Permissions and permission templates on a managed instance

func TestPermissionsValidateConfig(t *testing.T) {
	ctx := context.Background()
	r := &permissionsResource{}
	var sr resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	check := func(m permissionsModel) diag.Diagnostics {
		cfg := tfsdk.Config{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}
		st := tfsdk.State{Schema: sr.Schema, Raw: cfg.Raw}
		st.Set(ctx, &m)
		cfg.Raw = st.Raw
		var resp resource.ValidateConfigResponse
		r.ValidateConfig(ctx, resource.ValidateConfigRequest{Config: cfg}, &resp)
		return resp.Diagnostics
	}
	m := permModel()
	m.SpecialGroupName, m.ProjectKey = types.StringValue("project_creator"), types.StringValue("p")
	if !check(m).HasError() {
		t.Error("project_creator outside a template accepted")
	}
	m.ProjectKey, m.TemplateName = types.StringNull(), types.StringValue("T")
	if d := check(m); d.HasError() {
		t.Errorf("project_creator in a template: %v", d)
	}
}

func TestFindTemplate(t *testing.T) {
	ctx := context.Background()
	f, c := newFakeServer(t, map[string]func(*http.Request) (int, any){
		"GET /api/permissions/search_templates": ok(map[string]any{"permissionTemplates": []map[string]any{
			{"id": "t1", "name": "GitHub projects"},
			{"id": "t2", "name": "GitHub"},
		}}),
	})
	// The search is a partial match: only the exact name is taken.
	if tpl, err := findTemplate(ctx, c, "", "GitHub"); err != nil || tpl == nil || tpl.ID != "t2" {
		t.Errorf("by name = %+v, %v", tpl, err)
	}
	if q := f.callsTo("GET", "/api/permissions/search_templates")[0].query.Get("q"); q != "GitHub" {
		t.Errorf("q = %q", q)
	}
	if tpl, err := findTemplate(ctx, c, "", "github"); err != nil || tpl != nil {
		t.Errorf("names are case-sensitive: %+v, %v", tpl, err)
	}
	// An ID wins over a name, and an ID matches nothing else.
	if tpl, err := findTemplate(ctx, c, "t1", "GitHub"); err != nil || tpl == nil || tpl.ID != "t1" {
		t.Errorf("by ID = %+v, %v", tpl, err)
	}
	if tpl, err := findTemplate(ctx, c, "t9", "GitHub"); err != nil || tpl != nil {
		t.Errorf("unknown ID = %+v, %v", tpl, err)
	}
}

func TestBuiltInTemplate(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name      string
		templates []map[string]any
		want      string
	}{
		{"by name", []map[string]any{
			{"id": "a", "name": "Mine", "createdAt": "2020-01-01T00:00:00+0000"},
			{"id": "b", "name": "Default template", "createdAt": "2025-01-01T00:00:00+0000"},
		}, "b"},
		// Renamed: the oldest template is the built-in one.
		{"oldest", []map[string]any{
			{"id": "a", "name": "Newer", "createdAt": "2025-06-01T00:00:00+0000"},
			{"id": "b", "name": "Renamed default", "createdAt": "2020-01-01T00:00:00+0000"},
		}, "b"},
		{"none", nil, ""},
	} {
		_, cl := newFakeServer(t, map[string]func(*http.Request) (int, any){
			"GET /api/permissions/search_templates": ok(map[string]any{"permissionTemplates": c.templates}),
		})
		got, err := builtInTemplate(ctx, cl)
		if got != c.want || (c.want == "") != (err != nil) {
			t.Errorf("%s: builtInTemplate = %q, %v", c.name, got, err)
		}
	}
}

// On a GitHub-provisioned instance, permissions are granted to groups named
// after GitHub teams ("org/team"), and the name search must still match them.
func TestPermissionsCurrentForGitHubTeamGroup(t *testing.T) {
	ctx := context.Background()
	f, c := newFakeServer(t, map[string]func(*http.Request) (int, any){
		"GET /api/permissions/groups": ok(map[string]any{
			"groups": []principalPerms{{Name: "my-org/team-a", Permissions: []string{"user"}}, {Name: "my-org/team", Permissions: []string{"admin", "scan"}}},
			"paging": map[string]int{"total": 2},
		}),
	})
	r := &permissionsResource{resourceBase{p: &providerData{client: c}}}
	m := permModel()
	m.GroupName, m.ProjectKey = types.StringValue("my-org/team"), types.StringValue("p")
	got, err := r.current(ctx, &m)
	if err != nil || !slices.Equal(got, []string{"admin", "scan"}) {
		t.Errorf("current = %v, %v", got, err)
	}
	if q := f.callsTo("GET", "/api/permissions/groups")[0].query.Get("q"); q != "my-org/team" {
		t.Errorf("q = %q", q)
	}
}
