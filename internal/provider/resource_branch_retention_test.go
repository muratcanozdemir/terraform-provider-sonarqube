package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

// analyzeBranch runs a branch analysis of project with the scanner CLI in
// Docker (image SONAR_SCANNER_IMAGE, default sonarsource/sonar-scanner-cli),
// and waits until the server has processed it. It skips the test when Docker
// is not available.
func analyzeBranch(t *testing.T, project, branch string) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is needed to analyze a branch")
	}
	image := os.Getenv("SONAR_SCANNER_IMAGE")
	if image == "" {
		image = "sonarsource/sonar-scanner-cli"
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "hello.py"), []byte("print('hello')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	// #nosec G204 -- test-only, arguments from the test environment
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "--network", "host",
		"-v", src+":/usr/src:ro",
		"-e", "SONAR_HOST_URL="+os.Getenv("SONAR_HOST"), "-e", "SONAR_TOKEN="+os.Getenv("SONAR_TOKEN"),
		image,
		"-Dsonar.projectKey="+project, "-Dsonar.branch.name="+branch, "-Dsonar.sources=.",
		"-Dsonar.scm.disabled=true", "-Dsonar.working.directory=/tmp/scannerwork")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("scanner: %v\n%s", err, out)
	}
	m := regexp.MustCompile(`api/ce/task\?id=([\w-]+)`).FindSubmatch(out)
	if m == nil {
		t.Fatalf("no analysis task in the scanner output:\n%s", out)
	}
	// Wait for the server to process the report.
	c := testClient(t)
	var status, msg string
	err = waitFor(ctx, 5*time.Minute, func(ctx context.Context) (bool, error) {
		_, raw, err := c.Ce.Task(ctx, &sonarqube.CeTaskOptions{ID: string(m[1])})
		if err != nil {
			return false, err
		}
		var task struct {
			Task struct{ Status, ErrorMessage string }
		}
		if err := json.Unmarshal(raw.Body, &task); err != nil {
			return false, err
		}
		status, msg = task.Task.Status, task.Task.ErrorMessage
		return status != "PENDING" && status != "IN_PROGRESS", nil
	})
	switch {
	case err != nil:
		t.Fatalf("analysis of branch %s of %s: %v", branch, project, err)
	case status == "SUCCESS":
	case strings.Contains(msg, "valid license"):
		t.Skipf("the server cannot process analyses: %s", msg)
	default:
		t.Fatalf("analysis of branch %s of %s: %s: %s", branch, project, status, msg)
	}
}

// keptBranch reports whether housekeeping is told to keep the branch.
func keptBranch(t *testing.T, project, branch string) bool {
	t.Helper()
	res, _, err := testClient(t).ProjectBranches.List(context.Background(), &sonarqube.ProjectBranchesListOptions{Project: project})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range res.Branches {
		if b.Name == branch {
			return b.ExcludedFromPurge
		}
	}
	t.Fatalf("project %s has no branch %s", project, branch)
	return false
}

func TestAccProjectBranchRetention(t *testing.T) {
	requireEdition(t, "developer")
	p, branch := rname("ret"), "release/1.x"
	project := fmt.Sprintf(`
resource "sonarqube_project" "p" {
  name    = "ret"
  project = %q
}`, p)
	retention := project + `
resource "sonarqube_project_branch_retention" "r" {
  project = sonarqube_project.p.project
  branch  = "release/1.x"
}`
	check := func(want bool) resource.TestCheckFunc {
		return func(*terraform.State) error {
			if got := keptBranch(t, p, branch); got != want {
				return fmt.Errorf("branch kept = %v, want %v", got, want)
			}
			return nil
		}
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{Config: project},
			{
				PreConfig: func() { analyzeBranch(t, p, branch) },
				Config:    retention,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sonarqube_project_branch_retention.r", "id", p+"/"+branch),
					check(true),
				),
			},
			{ResourceName: "sonarqube_project_branch_retention.r", ImportState: true, ImportStateId: p + "/" + branch, ImportStateVerify: true},
			{
				// No longer kept, changed outside Terraform: kept again.
				PreConfig: func() {
					no := false
					if _, err := testClient(t).ProjectBranches.SetAutomaticDeletionProtection(context.Background(),
						&sonarqube.ProjectBranchesSetAutomaticDeletionProtectionOptions{Project: p, Branch: branch, Value: &no}); err != nil {
						t.Fatal(err)
					}
				},
				Config: retention,
				Check:  check(true),
			},
			// Destroying the resource lets housekeeping delete the branch again.
			{Config: project, Check: check(false)},
		},
	})
}

// TestAccProjectBranchRetention_mainAndMissing needs no analysis, so it runs
// on every edition: the main branch is always kept, and a missing branch is
// an error.
func TestAccProjectBranchRetention_mainAndMissing(t *testing.T) {
	p := rname("retm")
	cfg := func(branch string) string {
		return fmt.Sprintf(`
resource "sonarqube_project" "p" {
  name    = "retm"
  project = %q
}
resource "sonarqube_project_branch_retention" "r" {
  project = sonarqube_project.p.project
  branch  = %q
}`, p, branch)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{Config: cfg("main"), Check: resource.TestCheckResourceAttr("sonarqube_project_branch_retention.r", "id", p+"/main")},
			{Config: cfg("no-such-branch"), ExpectError: regexp.MustCompile(`Branch not found`)},
		},
	})
}
