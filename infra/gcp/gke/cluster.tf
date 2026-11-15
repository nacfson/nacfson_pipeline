resource "google_container_cluster" "this" {
  name     = local.shared.cluster_name
  location = local.shared.zone

  network    = google_compute_network.gke.id
  subnetwork = google_compute_subnetwork.gke.id

  ip_allocation_policy {
    cluster_secondary_range_name  = google_compute_subnetwork.gke.secondary_ip_range[0].range_name
    services_secondary_range_name = google_compute_subnetwork.gke.secondary_ip_range[1].range_name
  }

  remove_default_node_pool = true
  initial_node_count       = 1

  deletion_protection = true

  disable_l4_lb_firewall_reconciliation = true

  datapath_provider  = "ADVANCED_DATAPATH"
  logging_service    = "none"
  monitoring_service = "none"

  workload_identity_config {
    workload_pool = local.shared.workload_pool
  }

  release_channel {
    channel = "REGULAR"
  }

  maintenance_policy {
    recurring_window {
      start_time = "2026-01-01T18:00:00Z"
      end_time   = "2026-01-01T22:00:00Z"
      recurrence = "FREQ=DAILY"
    }
  }


  lifecycle {
    prevent_destroy = true
  }
}
