# Applies the catalogue against a real server (SONAR_HOST, SONAR_TOKEN),
# checks the result, then destroys everything.

variables {
  prefix = "tftest-onb"
}

run "onboard" {
  assert {
    condition     = length(output.projects) == 3
    error_message = "every catalogue project is created"
  }
  assert {
    condition     = output.projects["payments-api"] == "tftest-onb-payments-api"
    error_message = "keys carry the prefix"
  }
  assert {
    condition     = module.project["payments-api"].analysis_token != null
    error_message = "each project gets a CI token"
  }
  assert {
    condition     = sonarqube_group.leads["payments"].name == "tftest-onb-payments-leads"
    error_message = "a leads group per team"
  }
}

run "company_profile" {
  command = plan

  assert {
    condition     = sonarqube_qualityprofile.js.parent != null
    error_message = "the company JS profile inherits the server default"
  }
}
