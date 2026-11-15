terraform {
  backend "gcs" {
    bucket = "nacfson-pipeline-tfstate"
    prefix = "gke"
  }
}
