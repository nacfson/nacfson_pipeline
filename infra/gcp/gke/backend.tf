# Backend blocks cannot use variables or locals, so the bucket name is written here.
# It must match infra/gcp/bootstrap/main.tf. check-gcp-shared-values.py exempts this file from check 9.
terraform {
  backend "gcs" {
    bucket = "nacfson-pipeline-tfstate"
    prefix = "gke"
  }
}
