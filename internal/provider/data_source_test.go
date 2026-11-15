package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccDataSources_identity(t *testing.T) {
	group, login := rname("dsg"), rname("dsu")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "sonarqube_group" "g" {
  name        = %[1]q
  description = "data source test"
}
resource "sonarqube_user" "u" {
  login_name   = %[2]q
  name         = "DS User"
  email        = "ds@example.com"
  password     = "Secret-password-123"
  scm_accounts = ["ds-scm"]
}
resource "sonarqube_group_member" "m" {
  name       = sonarqube_group.g.name
  login_name = sonarqube_user.u.login_name
}
resource "sonarqube_user_token" "t" {
  login_name      = sonarqube_user.u.login_name
  name            = "ds-token"
  expiration_date = "2099-01-01"
}

data "sonarqube_group" "g" {
  name       = sonarqube_group.g.name
  depends_on = [sonarqube_group.g]
}
data "sonarqube_groups" "g" {
  search     = %[1]q
  depends_on = [sonarqube_group.g]
}
data "sonarqube_group_members" "all" {
  group      = sonarqube_group.g.name
  depends_on = [sonarqube_group_member.m]
}
data "sonarqube_group_members" "one" {
  group      = sonarqube_group.g.name
  login_name = "nobody-%[2]s"
  depends_on = [sonarqube_group_member.m]
}
data "sonarqube_group_members" "missing" {
  group          = "no-such-group-%[1]s"
  ignore_missing = true
}
data "sonarqube_user" "u" {
  login_name = sonarqube_user.u.login_name
}
data "sonarqube_users" "u" {
  search     = %[2]q
  depends_on = [sonarqube_user.u]
}
data "sonarqube_user_tokens" "t" {
  login_name = sonarqube_user.u.login_name
  depends_on = [sonarqube_user_token.t]
}
data "sonarqube_user_tokens" "missing" {
  login_name     = "no-such-user-%[2]s"
  ignore_missing = true
}
`, group, login),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrPair("data.sonarqube_group.g", "id", "sonarqube_group.g", "id"),
				resource.TestCheckResourceAttr("data.sonarqube_group.g", "description", "data source test"),
				resource.TestCheckResourceAttr("data.sonarqube_group.g", "managed", "false"),
				resource.TestCheckResourceAttr("data.sonarqube_groups.g", "groups.#", "1"),
				resource.TestCheckResourceAttr("data.sonarqube_groups.g", "groups.0.name", group),
				resource.TestCheckResourceAttr("data.sonarqube_group_members.all", "members.#", "1"),
				resource.TestCheckResourceAttr("data.sonarqube_group_members.all", "members.0.login_name", login),
				resource.TestCheckResourceAttr("data.sonarqube_group_members.all", "members.0.name", "DS User"),
				resource.TestCheckResourceAttr("data.sonarqube_group_members.one", "members.#", "0"),
				resource.TestCheckResourceAttr("data.sonarqube_group_members.missing", "members.#", "0"),
				resource.TestCheckResourceAttr("data.sonarqube_user.u", "name", "DS User"),
				resource.TestCheckResourceAttr("data.sonarqube_user.u", "email", "ds@example.com"),
				resource.TestCheckResourceAttr("data.sonarqube_user.u", "is_local", "true"),
				resource.TestCheckResourceAttr("data.sonarqube_user.u", "scm_accounts.#", "1"),
				resource.TestCheckResourceAttrPair("data.sonarqube_user.u", "uuid", "sonarqube_user.u", "uuid"),
				resource.TestCheckResourceAttr("data.sonarqube_users.u", "users.#", "1"),
				resource.TestCheckResourceAttr("data.sonarqube_user_tokens.t", "user_tokens.#", "1"),
				resource.TestCheckResourceAttr("data.sonarqube_user_tokens.t", "user_tokens.0.id", login+"/ds-token"),
				resource.TestCheckResourceAttr("data.sonarqube_user_tokens.t", "user_tokens.0.type", "USER_TOKEN"),
				resource.TestCheckResourceAttr("data.sonarqube_user_tokens.t", "user_tokens.0.expiration_date", "2099-01-01"),
				resource.TestCheckResourceAttrSet("data.sonarqube_user_tokens.t", "user_tokens.0.created_at"),
				resource.TestCheckNoResourceAttr("data.sonarqube_user_tokens.t", "user_tokens.0.project_key"),
				resource.TestCheckResourceAttr("data.sonarqube_user_tokens.missing", "user_tokens.#", "0"),
			),
		}},
	})
}

func TestAccDataSources_notFound(t *testing.T) {
	missing := rname("missing")
	cases := map[string]string{
		`data "sonarqube_group" "x" { name = %q }`:                                                "Group not found",
		`data "sonarqube_group_members" "x" { group = %q }`:                                       "ignore_missing",
		`data "sonarqube_user" "x" { login_name = %q }`:                                           "User not found",
		`data "sonarqube_qualitygate" "x" { name = %q }`:                                          "Quality gate not found",
		`data "sonarqube_qualitygates" "x" { name = %q }`:                                         "ignore_missing",
		`data "sonarqube_qualityprofile" "x" { key = %q }`:                                        "Quality profile not found",
		`data "sonarqube_qualityprofile_active_rules" "x" { key = %q }`:                           "Quality profile not found",
		`data "sonarqube_rule" "x" { key = "java:%s" }`:                                           "Rule not found",
		`data "sonarqube_project" "x" { project = %q }`:                                           "Project not found",
		"data \"sonarqube_measures\" \"x\" {\n  component   = %q\n  metric_keys = [\"ncloc\"]\n}": "Component not found",
		`data "sonarqube_alm_gitlab" "x" { key = %q }`:                                            "GitLab setting not found",
	}
	var steps []resource.TestStep
	for cfg, want := range cases {
		steps = append(steps, resource.TestStep{
			Config:      fmt.Sprintf(cfg, missing),
			ExpectError: regexp.MustCompile(want),
		})
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps:                    steps,
	})
}

func TestAccDataSources_validation(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{
				Config:      `data "sonarqube_qualityprofile" "x" { name = "Sonar way" }`,
				ExpectError: regexp.MustCompile("Incomplete lookup"),
			},
			{
				Config: `
data "sonarqube_qualityprofile" "x" {
  key      = "k"
  language = "java"
  name     = "n"
}`,
				ExpectError: regexp.MustCompile("Conflicting lookup"),
			},
		},
	})
}

func TestAccDataSources_quality(t *testing.T) {
	gate, profile := rname("dsqg"), rname("dsqp")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "sonarqube_qualitygate" "g" {
  name = %[1]q
  condition {
    metric    = "new_coverage"
    op        = "LT"
    threshold = "80"
  }
  condition {
    metric    = "new_duplicated_lines_density"
    op        = "GT"
    threshold = "3"
  }
}
resource "sonarqube_qualityprofile" "base" {
  name     = "%[2]s-base"
  language = "java"
}
resource "sonarqube_qualityprofile_activate_rule" "r" {
  key  = sonarqube_qualityprofile.base.key
  rule = "java:S1144"
}
resource "sonarqube_qualityprofile" "p" {
  name     = %[2]q
  language = "java"
  parent   = sonarqube_qualityprofile.base.name
}

data "sonarqube_qualitygate" "g" {
  name       = sonarqube_qualitygate.g.name
  depends_on = [sonarqube_qualitygate.g]
}
data "sonarqube_qualitygates" "one" {
  name       = sonarqube_qualitygate.g.name
  depends_on = [sonarqube_qualitygate.g]
}
data "sonarqube_qualitygates" "all" {
  depends_on = [sonarqube_qualitygate.g]
}
data "sonarqube_qualitygates" "missing" {
  name           = "no-such-gate-%[1]s"
  ignore_missing = true
}
data "sonarqube_qualityprofile" "by_name" {
  name       = sonarqube_qualityprofile.p.name
  language   = "java"
  depends_on = [sonarqube_qualityprofile.p]
}
data "sonarqube_qualityprofile" "by_key" {
  key        = sonarqube_qualityprofile.p.key
  depends_on = [sonarqube_qualityprofile_activate_rule.r]
}
data "sonarqube_qualityprofiles" "java" {
  language   = "java"
  depends_on = [sonarqube_qualityprofile.p]
}
data "sonarqube_qualityprofiles" "named" {
  name       = sonarqube_qualityprofile.p.name
  depends_on = [sonarqube_qualityprofile.p]
}
data "sonarqube_qualityprofile_active_rules" "p" {
  key        = sonarqube_qualityprofile.p.key
  depends_on = [sonarqube_qualityprofile_activate_rule.r]
}
data "sonarqube_qualityprofile_deactivated_rules" "p" {
  key = sonarqube_qualityprofile.p.key
}
data "sonarqube_rule" "r" {
  key = "java:S1144"
}
data "sonarqube_rule" "template" {
  key = "java:S124"
}
data "sonarqube_rules" "r" {
  languages  = ["java"]
  types      = ["BUG"]
  severities = ["BLOCKER", "CRITICAL"]
}
data "sonarqube_rules" "templates" {
  languages   = ["java"]
  is_template = true
}
data "sonarqube_languages" "all" {}
data "sonarqube_languages" "java" {
  search = "java"
}
`, gate, profile),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.sonarqube_qualitygate.g", "is_default", "false"),
				resource.TestCheckResourceAttr("data.sonarqube_qualitygate.g", "is_built_in", "false"),
				resource.TestCheckResourceAttr("data.sonarqube_qualitygate.g", "condition.#", "2"),
				// sorted by metric
				resource.TestCheckResourceAttr("data.sonarqube_qualitygate.g", "condition.0.metric", "new_coverage"),
				resource.TestCheckResourceAttr("data.sonarqube_qualitygate.g", "condition.0.threshold", "80"),
				resource.TestCheckResourceAttr("data.sonarqube_qualitygate.g", "condition.1.op", "GT"),
				resource.TestCheckResourceAttr("data.sonarqube_qualitygates.one", "quality_gates.#", "1"),
				resource.TestCheckResourceAttr("data.sonarqube_qualitygates.one", "quality_gates.0.condition.#", "2"),
				resource.TestCheckResourceAttr("data.sonarqube_qualitygates.missing", "quality_gates.#", "0"),
				resource.TestCheckTypeSetElemNestedAttrs("data.sonarqube_qualitygates.all", "quality_gates.*", map[string]string{"name": gate}),
				resource.TestCheckTypeSetElemNestedAttrs("data.sonarqube_qualitygates.all", "quality_gates.*", map[string]string{"is_built_in": "true"}),
				resource.TestCheckResourceAttrPair("data.sonarqube_qualityprofile.by_name", "key", "sonarqube_qualityprofile.p", "key"),
				resource.TestCheckResourceAttr("data.sonarqube_qualityprofile.by_name", "parent", profile+"-base"),
				resource.TestCheckResourceAttr("data.sonarqube_qualityprofile.by_key", "name", profile),
				resource.TestCheckResourceAttr("data.sonarqube_qualityprofile.by_key", "language", "java"),
				resource.TestCheckTypeSetElemNestedAttrs("data.sonarqube_qualityprofiles.java", "quality_profiles.*", map[string]string{"name": profile}),
				resource.TestCheckTypeSetElemNestedAttrs("data.sonarqube_qualityprofiles.java", "quality_profiles.*", map[string]string{"is_built_in": "true"}),
				resource.TestCheckResourceAttr("data.sonarqube_qualityprofiles.named", "quality_profiles.#", "1"),
				// The child inherits the parent's one rule.
				resource.TestCheckResourceAttr("data.sonarqube_qualityprofile_active_rules.p", "rules.#", "1"),
				resource.TestCheckResourceAttr("data.sonarqube_qualityprofile_active_rules.p", "rules.0.key", "java:S1144"),
				resource.TestCheckResourceAttr("data.sonarqube_qualityprofile.by_key", "active_rule_count", "1"),
				resource.TestCheckResourceAttrSet("data.sonarqube_qualityprofile_deactivated_rules.p", "rules.0.key"),
				resource.TestCheckResourceAttr("data.sonarqube_rule.r", "repo", "java"),
				resource.TestCheckResourceAttr("data.sonarqube_rule.r", "language", "java"),
				resource.TestCheckResourceAttr("data.sonarqube_rule.r", "is_template", "false"),
				resource.TestCheckResourceAttr("data.sonarqube_rule.template", "is_template", "true"),
				resource.TestCheckResourceAttrSet("data.sonarqube_rule.template", "params.%"),
				resource.TestCheckResourceAttrSet("data.sonarqube_rules.r", "rules.0.key"),
				resource.TestCheckTypeSetElemNestedAttrs("data.sonarqube_rules.templates", "rules.*", map[string]string{"key": "java:S124"}),
				resource.TestCheckTypeSetElemNestedAttrs("data.sonarqube_languages.all", "languages.*", map[string]string{"key": "java", "name": "Java"}),
				resource.TestCheckTypeSetElemNestedAttrs("data.sonarqube_languages.java", "languages.*", map[string]string{"key": "java"}),
			),
		}},
	})
}

func TestAccDataSources_project(t *testing.T) {
	project, gl := rname("dsp"), rname("dsgl")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "sonarqube_project" "p" {
  project    = %[1]q
  name       = "DS Project"
  visibility = "private"
  tags       = ["b", "a"]
}
resource "sonarqube_permission_template" "t" {
  name                = %[1]q
  description         = "ds"
  project_key_pattern = "ds-.*"
}
resource "sonarqube_alm_gitlab" "gl" {
  key                   = %[2]q
  url                   = "https://gitlab.example.com/api/v4"
  personal_access_token = "glpat-x"
}

data "sonarqube_project" "p" {
  project = sonarqube_project.p.project
}
data "sonarqube_project" "no_badge" {
  project              = sonarqube_project.p.project
  retrieve_badge_token = false
}
data "sonarqube_measures" "m" {
  component   = sonarqube_project.p.project
  metric_keys = ["ncloc", "coverage"]
}
data "sonarqube_permission_templates" "t" {
  search     = %[1]q
  depends_on = [sonarqube_permission_template.t]
}
data "sonarqube_alm_gitlab" "gl" {
  key = sonarqube_alm_gitlab.gl.key
}
`, project, gl),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.sonarqube_project.p", "name", "DS Project"),
				resource.TestCheckResourceAttr("data.sonarqube_project.p", "visibility", "private"),
				resource.TestCheckResourceAttr("data.sonarqube_project.p", "tags.#", "2"),
				resource.TestCheckResourceAttrPair("data.sonarqube_project.p", "badge_token", "sonarqube_project.p", "badge_token"),
				resource.TestCheckNoResourceAttr("data.sonarqube_project.no_badge", "badge_token"),
				// Never analysed: no measures, but no error.
				resource.TestCheckResourceAttr("data.sonarqube_measures.m", "measures.#", "0"),
				resource.TestCheckResourceAttr("data.sonarqube_measures.m", "values.%", "0"),
				resource.TestCheckResourceAttr("data.sonarqube_permission_templates.t", "permission_templates.#", "1"),
				resource.TestCheckResourceAttr("data.sonarqube_permission_templates.t", "permission_templates.0.project_key_pattern", "ds-.*"),
				resource.TestCheckResourceAttrPair("data.sonarqube_permission_templates.t", "permission_templates.0.id", "sonarqube_permission_template.t", "id"),
				resource.TestCheckResourceAttr("data.sonarqube_alm_gitlab.gl", "url", "https://gitlab.example.com/api/v4"),
			),
		}},
	})
}

func TestAccDataSources_portfolio(t *testing.T) {
	requireEdition(t, "enterprise")
	pf, project := rname("dspf"), rname("dspfp")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "sonarqube_project" "p" {
  project = %[2]q
  name    = "pf project"
}
resource "sonarqube_portfolio" "pf" {
  key            = %[1]q
  name           = "DS Portfolio"
  description    = "ds"
  selection_mode = "MANUAL"
  selected_projects = [{ project_key = sonarqube_project.p.project }]
  portfolios        = [sonarqube_portfolio.tags.key]
}
resource "sonarqube_portfolio" "tags" {
  key            = "%[1]s-tags"
  name           = "DS Tags"
  selection_mode = "TAGS"
  tags           = ["x", "y"]
}
data "sonarqube_portfolio" "pf" {
  key        = sonarqube_portfolio.pf.key
  depends_on = [sonarqube_portfolio.pf]
}
data "sonarqube_portfolio" "tags" {
  key        = sonarqube_portfolio.tags.key
  depends_on = [sonarqube_portfolio.tags]
}
`, pf, project),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.sonarqube_portfolio.pf", "name", "DS Portfolio"),
				resource.TestCheckResourceAttr("data.sonarqube_portfolio.pf", "description", "ds"),
				resource.TestCheckResourceAttr("data.sonarqube_portfolio.pf", "qualifier", "VW"),
				resource.TestCheckResourceAttr("data.sonarqube_portfolio.pf", "selection_mode", "MANUAL"),
				resource.TestCheckResourceAttr("data.sonarqube_portfolio.pf", "selected_projects.#", "1"),
				resource.TestCheckResourceAttr("data.sonarqube_portfolio.pf", "tags.#", "0"),
				resource.TestCheckResourceAttr("data.sonarqube_portfolio.pf", "applications.#", "0"),
				resource.TestCheckTypeSetElemAttrPair("data.sonarqube_portfolio.pf", "portfolios.*", "sonarqube_portfolio.tags", "key"),
				resource.TestCheckResourceAttr("data.sonarqube_portfolio.pf", "application_branches.%", "0"),
				resource.TestCheckResourceAttr("data.sonarqube_portfolio.tags", "portfolios.#", "0"),
				resource.TestCheckNoResourceAttr("data.sonarqube_portfolio.pf", "regexp"),
				resource.TestCheckResourceAttr("data.sonarqube_portfolio.tags", "selection_mode", "TAGS"),
				resource.TestCheckResourceAttr("data.sonarqube_portfolio.tags", "tags.#", "2"),
				resource.TestCheckNoResourceAttr("data.sonarqube_portfolio.tags", "description"),
			),
		}},
	})
}

// change_parent may attach a similarly named profile when the requested one
// does not exist; the resource must refuse instead.
func TestAccQualityProfile_parentMustExist(t *testing.T) {
	name := rname("qpbad")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "sonarqube_qualityprofile" "p" {
  name     = %q
  language = "java"
  parent   = "Sonar"
}`, name),
			ExpectError: regexp.MustCompile(`no java quality profile is named "Sonar"`),
		}},
	})
}
