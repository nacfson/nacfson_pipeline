# Zonal GKE Standard cluster (research R5).
resource "google_container_cluster" "this" {
  name     = local.shared.cluster_name
  location = local.shared.zone

  network    = google_compute_network.gke.id
  subnetwork = google_compute_subnetwork.gke.id

  ip_allocation_policy {
    cluster_secondary_range_name  = google_compute_subnetwork.gke.secondary_ip_range[0].range_name
    services_secondary_range_name = google_compute_subnetwork.gke.secondary_ip_range[1].range_name
  }

  # Node pools are managed separately in nodepools.tf.
  # Provider 8.x warns that remove_default_node_pool is deprecated for clusters on
  # 1.36.4-gke.1469000+ (initial_node_count = 0 replaces it there). It still works on every
  # version, so it stays until the REGULAR channel default is past that version.
  remove_default_node_pool = true
  initial_node_count       = 1

  deletion_protection = true

  # Terraform, not GKE, owns the web firewall rule (ingress.tf). GKE still creates the rule that lets
  # Google's health checkers reach the load balancer's health-check port. Needs GKE 1.31.3-gke.105600+.
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

  # Daily 18:00-22:00 UTC (03:00-07:00 KST). A weekly window does not give GKE
  # the 48 hours per 32 days it requires.
  maintenance_policy {
    recurring_window {
      start_time = "2026-01-01T18:00:00Z"
      end_time   = "2026-01-01T22:00:00Z"
      recurrence = "FREQ=DAILY"
    }
  }

  # Control plane: public IP endpoint with no authorized networks (FR-025).
  # Access needs Google sign-in (IAM) and then Kubernetes RBAC
  # (Constitution Principle III, Clarification 2.3.1).
  # Switching to the DNS-only endpoint later is an in-place change; see research R5.

  lifecycle {
    prevent_destroy = true
  }
}
