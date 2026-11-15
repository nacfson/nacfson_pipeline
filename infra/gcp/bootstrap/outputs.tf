output "state_bucket" {
  description = "GCS bucket that holds Terraform state for the project/ and gke/ roots."
  value       = google_storage_bucket.tfstate.name
}
