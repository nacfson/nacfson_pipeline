# Names and meanings are fixed by specs/015-gke-terraform-infra/contracts/terraform-outputs.md.

output "cluster_name" {
  description = "GKE cluster name."
  value       = google_container_cluster.this.name
}

output "cluster_zone" {
  description = "Zone of the cluster."
  value       = google_container_cluster.this.location
}

output "cluster_endpoint" {
  description = "Public IP of the Kubernetes API (server: in kubeconfigs)."
  value       = google_container_cluster.this.endpoint
}

output "cluster_ca_certificate" {
  description = "Base64 cluster CA certificate (certificate-authority-data: in kubeconfigs). Public key material, hidden by default."
  value       = google_container_cluster.this.master_auth[0].cluster_ca_certificate
  sensitive   = true
}

output "get_credentials_command" {
  description = "Command that writes ~/.kube/config for this cluster."
  value       = "gcloud container clusters get-credentials ${google_container_cluster.this.name} --zone ${google_container_cluster.this.location} --project ${local.shared.project_id}"
}

output "ingress_address_name" {
  description = "Name of the reserved address. Must equal the Traefik Service annotation."
  value       = google_compute_address.ingress.name
}

output "ingress_ip" {
  description = "Public IP of the web load balancer. DNS A records point here at cutover."
  value       = google_compute_address.ingress.address
}

output "kms_key_id" {
  description = "Full resource ID of the OpenBao seal key."
  value       = google_kms_crypto_key.openbao_unseal.id
}

output "openbao_gsa_email" {
  description = "Google service account OpenBao uses through Workload Identity."
  value       = google_service_account.openbao_kms.email
}
