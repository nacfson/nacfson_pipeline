terraform {
  backend "gcs" {
    bucket = "nacfson-pipeline-tfstate"
    prefix = "project"
  }
}
