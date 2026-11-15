# Dedicated VPC for the cluster (research R13).
# The only internet firewall opening is the web rule in ingress.tf: there is no SSH, RDP or ICMP access.
# GKE adds its own rules for the control plane and for the load balancer's health checks.
resource "google_compute_network" "gke" {
  name                    = local.shared.network.vpc
  auto_create_subnetworks = false
}

resource "google_compute_subnetwork" "gke" {
  name          = local.shared.network.subnet
  region        = local.shared.region
  network       = google_compute_network.gke.id
  ip_cidr_range = local.shared.network.nodes_cidr

  # OpenBao reaches Cloud KMS through private.googleapis.com (clusters/gke/layers.yaml).
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
