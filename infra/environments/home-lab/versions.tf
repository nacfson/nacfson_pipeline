terraform {
  required_version = ">= 1.6.0"

  required_providers {
    local = {
      source  = "hashicorp/local"
      version = ">= 2.4.0"
    }
  }

  # Local state by default for home-lab bootstrap. Replace with remote backend before shared use.
  backend "local" {
    path = "terraform.tfstate"
  }
}

# Intentionally no kubernetes / helm / kubectl providers.
# OpenTofu owns external infrastructure contracts only.