package provider

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

func defaultGate(t *testing.T, c *sonarqube.Client) string {
	t.Helper()
	l, _, err := c.Qualitygates.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range l.Qualitygates {
		if g.IsDefault {
			return g.Name
		}
	}
	return ""
}

func expectDefaultGate(t *testing.T, c *sonarqube.Client, want string) func(*terraform.State) error {
	return func(*terraform.State) error {
		if got := defaultGate(t, c); got != want {
			return fmt.Errorf("default gate is %q, want %q", got, want)
		}
		return nil
	}
}

// TestAccQualityGate_restoresPreviousDefault: a gate that stops being the
// default hands it back to the gate that was default before, not to the
// built-in one.
func TestAccQualityGate_restoresPreviousDefault(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()
	pre, g := rname("gpre"), rname("gdef")
	if _, _, err := c.Qualitygates.Create(ctx, &sonarqube.QualitygatesCreateOptions{Name: pre}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = c.Qualitygates.SetDefault(ctx, &sonarqube.QualitygatesSetDefaultOptions{Name: "Sonar way"})
		_, _ = c.Qualitygates.Delete(ctx, &sonarqube.QualitygatesDeleteOptions{Name: pre})
	})
	if _, err := c.Qualitygates.SetDefault(ctx, &sonarqube.QualitygatesSetDefaultOptions{Name: pre}); err != nil {
		t.Fatal(err)
	}
	cfg := func(isDefault bool) string {
		return fmt.Sprintf(`
resource "sonarqube_qualitygate" "g" {
  name       = %q
  is_default = %t
  condition {
    metric    = "new_coverage"
    op        = "LT"
    threshold = "80.0"
  }
}`, g, isDefault)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		// Destroyed while default: the previous default comes back.
		CheckDestroy: expectDefaultGate(t, c, pre),
		Steps: []resource.TestStep{
			// "80.0" is kept as written: no diff after apply.
			{Config: cfg(true), Check: resource.ComposeAggregateTestCheckFunc(
				expectDefaultGate(t, c, g),
				resource.TestCheckResourceAttr("sonarqube_qualitygate.g", "condition.0.threshold", "80.0"),
			)},
			{Config: cfg(false), Check: expectDefaultGate(t, c, pre)},
			{Config: cfg(true), Check: expectDefaultGate(t, c, g)},
		},
	})
}

func TestAccQualityGate_edgeValidation(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{
				// SonarQube would trim it, leaving a permanent diff.
				Config: `
resource "sonarqube_qualitygate" "g" {
  name = "tf-acc-edge"
  condition {
    metric    = "new_coverage"
    op        = "LT"
    threshold = " 80"
  }
}`,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`must not start or end with whitespace`),
			},
			{
				// Every change to a built-in gate is forbidden, so it cannot be imported.
				Config: `
resource "sonarqube_qualitygate" "b" {
  name = "Sonar way"
  condition {
    metric    = "new_coverage"
    op        = "LT"
    threshold = "80"
  }
}`,
				ResourceName:  "sonarqube_qualitygate.b",
				ImportState:   true,
				ImportStateId: "Sonar way",
				ExpectError:   regexp.MustCompile(`Cannot manage a built-in quality gate`),
			},
		},
	})
}

func defaultProfile(t *testing.T, c *sonarqube.Client, lang string) string {
	t.Helper()
	res, _, err := c.Qualityprofiles.Search(context.Background(), &sonarqube.QualityprofilesSearchOptions{Language: lang, Defaults: new(true)})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range res.Profiles {
		if p.IsDefault && p.Language == lang {
			return p.Name
		}
	}
	return ""
}

func expectDefaultProfile(t *testing.T, c *sonarqube.Client, lang, want string) func(*terraform.State) error {
	return func(*terraform.State) error {
		if got := defaultProfile(t, c, lang); got != want {
			return fmt.Errorf("default %s profile is %q, want %q", lang, got, want)
		}
		return nil
	}
}

// TestAccQualityProfile_restoresPreviousDefault: as for gates. Guessing the
// built-in default instead is unreliable on SonarQube 2026, which ships
// several built-in profiles per language.
func TestAccQualityProfile_restoresPreviousDefault(t *testing.T) {
	c := testClient(t)
	ctx := context.Background()
	const lang = "py"
	original := defaultProfile(t, c, lang)
	pre, p := rname("ppre"), rname("pdef")
	if _, _, err := c.Qualityprofiles.Create(ctx, &sonarqube.QualityprofilesCreateOptions{Name: pre, Language: lang}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = c.Qualityprofiles.SetDefault(ctx, &sonarqube.QualityprofilesSetDefaultOptions{Language: lang, QualityProfile: original})
		_, _ = c.Qualityprofiles.Delete(ctx, &sonarqube.QualityprofilesDeleteOptions{Language: lang, QualityProfile: pre})
	})
	if _, err := c.Qualityprofiles.SetDefault(ctx, &sonarqube.QualityprofilesSetDefaultOptions{Language: lang, QualityProfile: pre}); err != nil {
		t.Fatal(err)
	}
	cfg := func(isDefault bool) string {
		return fmt.Sprintf(`
resource "sonarqube_qualityprofile" "p" {
  name       = %q
  language   = %q
  is_default = %t
}`, p, lang, isDefault)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		CheckDestroy:             expectDefaultProfile(t, c, lang, pre),
		Steps: []resource.TestStep{
			{Config: cfg(true), Check: expectDefaultProfile(t, c, lang, p)},
			{Config: cfg(false), Check: expectDefaultProfile(t, c, lang, pre)},
			{Config: cfg(true), Check: expectDefaultProfile(t, c, lang, p)},
			// Built-in profiles are read-only in SonarQube.
			{
				Config:        cfg(true),
				ResourceName:  "sonarqube_qualityprofile.p",
				ImportState:   true,
				ImportStateId: lang + "/" + original,
				ExpectError:   regexp.MustCompile(`Cannot manage a built-in quality profile`),
			},
		},
	})
}

// TestAccQualityProfileActivateRule_params: SonarQube answers 204 to a
// parameter the rule does not have and ignores it; the provider reports it.
func TestAccQualityProfileActivateRule_params(t *testing.T) {
	p := rname("prm")
	cfg := func(params string) string {
		return fmt.Sprintf(`
resource "sonarqube_qualityprofile" "p" {
  name     = %q
  language = "java"
}
resource "sonarqube_qualityprofile_activate_rule" "r" {
  key    = sonarqube_qualityprofile.p.key
  rule   = "java:S138"
  params = %q
}`, p, params)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{Config: cfg("max = 120"), PlanOnly: true, ExpectError: regexp.MustCompile(`spaces around its key or value`)},
			{Config: cfg("max="), PlanOnly: true, ExpectError: regexp.MustCompile(`empty value`)},
			{Config: cfg("max=120")},
			{Config: cfg("max=120;maximum=3"), ExpectError: regexp.MustCompile(`(?s)no parameter\s+"maximum"\s+\(its\s+parameters:\s+max\)`)},
			{Config: cfg("max=130")},
		},
	})
}

func TestAccPortfolio_edgeValidation(t *testing.T) {
	cfg := func(attrs string) string {
		return fmt.Sprintf(`
resource "sonarqube_portfolio" "p" {
  key  = "tf-acc-edge"
  name = "edge"
  %s
}`, attrs)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{Config: cfg(`selection_mode = "TAGS"
  tags = []`), PlanOnly: true, ExpectError: regexp.MustCompile(`at least 1`)},
			{Config: cfg(`selection_mode = "TAGS"
  tags = ["back end"]`), PlanOnly: true, ExpectError: regexp.MustCompile(`non-empty tag without spaces`)},
			{Config: cfg(`selection_mode = "REGEXP"
  regexp = " tf-.* "`), PlanOnly: true, ExpectError: regexp.MustCompile(`must not start or end with whitespace`)},
			{Config: `
resource "sonarqube_portfolio" "p" {
  key  = "tf-acc-edge"
  name = " edge"
}`, PlanOnly: true, ExpectError: regexp.MustCompile(`must not start or end with whitespace`)},
		},
	})
}
