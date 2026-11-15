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
  shared = jsondecode(file("${path.module}/../shared-values.json"))
}

provider "google" {
  project = local.shared.project_id
  region  = local.shared.region
}
