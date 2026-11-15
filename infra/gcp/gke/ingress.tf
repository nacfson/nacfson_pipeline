# Reserved regional address for the single L4 pass-through load balancer (FR-007, FR-007a).
# Service/platform/traefik refers to it by name through the
# networking.gke.io/load-balancer-ip-addresses annotation. GKE creates the load balancer itself.
# No prevent_destroy: removing the environment (User Story 4) must stop this address from billing.
resource "google_compute_address" "ingress" {
  name         = local.shared.ingress.address_name
  region       = local.shared.region
  address_type = "EXTERNAL"
  network_tier = "PREMIUM"
}

# The only internet opening in the VPC: TCP 80/443 to the load balancer's address, on node VMs only.
# GKE's own load balancer rule is turned off (cluster.tf), so this rule is what lets visitors in.
# A pass-through load balancer keeps the destination address, so packets reach the node addressed to
# this IP; node addresses and node ports stay closed.
resource "google_compute_firewall" "web_ingress" {
  name      = "${local.shared.network.vpc}-allow-web"
  network   = google_compute_network.gke.id
  direction = "INGRESS"
  priority  = 1000

  source_ranges           = ["0.0.0.0/0"]
  destination_ranges      = ["${google_compute_address.ingress.address}/32"]
  target_service_accounts = [google_service_account.gke_nodes.email]

  allow {
    protocol = "tcp"
    ports    = ["80", "443"]
  }
}
