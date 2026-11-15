# Keep the release branch when it is no longer analyzed. The branch must
# already exist: it is created by its first analysis.
resource "sonarqube_project_branch_retention" "release" {
  project = "my-project"
  branch  = "release/1.x"
}

# To keep branches created from now on whose name matches a pattern, extend
# the list setting; it replaces the default list, so repeat the defaults.
# Patterns do not apply to branches that already exist.
resource "sonarqube_setting" "keep_release_branches" {
  key    = "sonar.dbcleaner.branchesToKeepWhenInactive"
  values = ["main", "master", "develop", "trunk", "release/.*"]
}
