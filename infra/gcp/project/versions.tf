terraform {
  required_version = ">= 1.9.0, < 2.0.0"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 8.0"
    }
  }
}

locals {
  # Single source for values shared with clusters/gke/ (Spec 015, contracts/shared-values.md).
  shared = jsondecode(file("${path.module}/../shared-values.json"))
}

provider "google" {
  project = local.shared.project_id
  region  = local.shared.region

  # The Billing Budgets API needs a quota project when called with user credentials (Cloud Shell).
  user_project_override = true
  billing_project       = local.shared.project_id
}
