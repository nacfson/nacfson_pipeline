terraform {
  required_version = ">= 1.9.0, < 2.0.0"

  required_providers {
    oci = {
      source  = "oracle/oci"
      version = "~> 9.11.0"
    }
  }
}

provider "oci" {
  region = var.region
}
