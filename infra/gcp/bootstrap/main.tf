# State bucket for the project/ and gke/ roots (research R3).
# This root keeps its own state locally (terraform.tfstate is gitignored). Apply it once.
resource "google_storage_bucket" "tfstate" {
  # Must match the bucket written in infra/gcp/{project,gke}/backend.tf.
  name     = "${local.shared.project_id}-tfstate"
  location = local.shared.region

  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"

  versioning {
    enabled = true
  }

  # Keep the 10 most recent old versions of each state file for recovery; delete older ones.
  lifecycle_rule {
    condition {
      num_newer_versions = 10
      with_state         = "ARCHIVED"
    }
    action {
      type = "Delete"
    }
  }

  lifecycle {
    prevent_destroy = true
  }
}
