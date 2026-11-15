locals {
  gar_cleanup = jsondecode(file("${path.module}/../../../${local.shared.registry.cleanup_source}"))
  gar_actions = { Delete = "DELETE", Keep = "KEEP" }
}

resource "google_artifact_registry_repository" "public" {
  repository_id = local.shared.registry.repository_id
  location      = local.shared.region
  format        = "DOCKER"

  cleanup_policy_dry_run = false

  dynamic "cleanup_policies" {
    for_each = local.gar_cleanup
    content {
      id     = cleanup_policies.value.name
      action = local.gar_actions[cleanup_policies.value.action.type]

      dynamic "condition" {
        for_each = can(cleanup_policies.value.condition) ? [cleanup_policies.value.condition] : []
        content {
          tag_state  = try(condition.value.tagState, null)
          older_than = try(condition.value.olderThan, null)
        }
      }

      dynamic "most_recent_versions" {
        for_each = can(cleanup_policies.value.mostRecentVersions) ? [cleanup_policies.value.mostRecentVersions] : []
        content {
          keep_count = most_recent_versions.value.keepCount
        }
      }
    }
  }

  lifecycle {
    prevent_destroy = true
  }
}

resource "google_artifact_registry_repository_iam_member" "public_read" {
  repository = google_artifact_registry_repository.public.name
  location   = google_artifact_registry_repository.public.location
  role       = "roles/artifactregistry.reader"
  member     = "allUsers"
}
