terraform {
  required_version = ">= 1.6.0"
}

locals {
  active = var.enabled && var.kind != "none"

  contract = {
    enabled                     = local.active
    kind                        = var.kind
    endpoint                    = var.endpoint
    bucket_or_path              = var.bucket_or_path
    region                      = var.region
    retention_days              = var.retention_days
    encryption                  = var.encryption
    same_failure_domain_as_node = var.same_failure_domain_as_node
    consumers = [
      "cnpg-base-backups-and-wal",
      "k3s-etcd-snapshots",
      "forgejo-and-authoritative-volumes",
    ]
  }
}

resource "terraform_data" "backup_target" {
  input = local.contract

  lifecycle {
    precondition {
      condition     = !local.active || (trimspace(var.endpoint) != "" && trimspace(var.bucket_or_path) != "")
      error_message = "enabled backup targets require endpoint and bucket_or_path."
    }
  }
}