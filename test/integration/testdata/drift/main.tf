# Fixture for the drift scenario. Applied once to populate state, then the
# test rewrites this file in a temp copy to a different trigger value so
# `terraform plan -target=null_resource.drifty` reports pending changes.

terraform {
  required_providers {
    null = {
      source  = "hashicorp/null"
      version = "~> 3.2"
    }
  }
}

resource "null_resource" "drifty" {
  triggers = {
    label = "applied-v1"
  }
}
