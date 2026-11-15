resource "google_compute_network" "gke" {
  name                    = local.shared.network.vpc
  auto_create_subnetworks = false
}

resource "google_compute_subnetwork" "gke" {
  name          = local.shared.network.subnet
  region        = local.shared.region
  network       = google_compute_network.gke.id
  ip_cidr_range = local.shared.network.nodes_cidr

  private_ip_google_access = true

  secondary_ip_range {
    range_name    = "pods"
    ip_cidr_range = local.shared.network.pods_cidr
  }

  secondary_ip_range {
    range_name    = "services"
    ip_cidr_range = local.shared.network.services_cidr
  }
}
