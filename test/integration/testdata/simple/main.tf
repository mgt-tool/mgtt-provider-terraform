# Minimal hermetic fixture for integration tests. Uses hashicorp/null so
# no cloud credentials or network calls are required — `terraform apply`
# runs in seconds and produces a local state file.

terraform {
  required_providers {
    null = {
      source  = "hashicorp/null"
      version = "~> 3.2"
    }
  }
}

resource "null_resource" "alpha" {
  triggers = {
    label = "alpha-v1"
  }
}

resource "null_resource" "beta" {
  triggers = {
    label = "beta-v1"
  }
}

resource "null_resource" "gamma" {
  triggers = {
    # Beta is declared explicitly as a dependency so the dependents_count
    # probe has something to find.
    label      = "gamma-v1"
    depends_on = null_resource.beta.id
  }
}
