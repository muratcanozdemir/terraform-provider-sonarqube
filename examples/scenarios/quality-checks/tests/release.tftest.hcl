variables {
  release = {
    version    = "1.4.0"
    components = ["tftest-qc-api", "tftest-qc-web"]
  }
}

run "setup" {
  module {
    source = "./tests/setup"
  }

  variables {
    keys = var.release.components
  }
}

# Never-analyzed components have no green gate, so the release is blocked.
run "blocks_unanalyzed_release" {
  command = plan

  expect_failures = [terraform_data.release_gate]
}

run "reports" {
  command = plan

  variables {
    release = {
      version    = "1.4.0"
      components = ["tftest-qc-api"]
    }
  }

  # Still blocked, but the data sources answer.
  expect_failures = [terraform_data.release_gate]

  assert {
    condition     = output.gates["tftest-qc-api"] == "NONE"
    error_message = "a project without analysis has no gate status"
  }
}
