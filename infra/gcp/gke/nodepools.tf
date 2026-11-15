resource "google_service_account" "gke_nodes" {
  account_id   = "gke-nodes"
  display_name = "GKE node VMs"
}

resource "google_project_iam_member" "gke_nodes_default" {
  project = local.shared.project_id
  role    = "roles/container.defaultNodeServiceAccount"
  member  = "serviceAccount:${google_service_account.gke_nodes.email}"
}

locals {
  taint_effects = {
    NoSchedule       = "NO_SCHEDULE"
    PreferNoSchedule = "PREFER_NO_SCHEDULE"
    NoExecute        = "NO_EXECUTE"
  }
}

resource "google_container_node_pool" "pools" {
  for_each = local.node_pools

  name       = each.key
  cluster    = google_container_cluster.this.id
  location   = local.shared.zone
  node_count = each.value.node_count

  node_config {
    machine_type    = each.value.machine_type
    spot            = each.value.spot
    disk_size_gb    = 30
    disk_type       = "pd-balanced"
    service_account = google_service_account.gke_nodes.email
    oauth_scopes    = ["https://www.googleapis.com/auth/cloud-platform"]

    workload_metadata_config {
      mode = "GKE_METADATA"
    }

    dynamic "taint" {
      for_each = each.value.taints
      content {
        key    = regex("^([^=]+)=([^:]*):(.+)$", taint.value)[0]
        value  = regex("^([^=]+)=([^:]*):(.+)$", taint.value)[1]
        effect = local.taint_effects[regex("^([^=]+)=([^:]*):(.+)$", taint.value)[2]]
      }
    }
  }

  management {
    auto_repair  = true
    auto_upgrade = true
  }

  upgrade_settings {
    strategy        = "SURGE"
    max_surge       = 1
    max_unavailable = 0
  }

  depends_on = [google_project_iam_member.gke_nodes_default]
}
