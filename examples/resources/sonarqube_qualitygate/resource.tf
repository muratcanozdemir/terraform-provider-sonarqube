resource "sonarqube_qualitygate" "strict" {
  name = "Strict"

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
