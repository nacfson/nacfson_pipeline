resource "google_compute_address" "ingress" {
  name         = local.shared.ingress.address_name
  region       = local.shared.region
  address_type = "EXTERNAL"
  network_tier = "PREMIUM"
}

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
