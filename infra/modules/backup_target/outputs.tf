output "endpoint" {
  value = local.active ? var.endpoint : null
}

output "contract" {
  description = "Off-server backup destination contract for Ansible/CNPG/agents."
  value       = local.contract
}

output "ansible_vars" {
  value = {
    backup_enabled        = local.active
    backup_kind           = var.kind
    backup_endpoint       = var.endpoint
    backup_bucket_or_path = var.bucket_or_path
    backup_region         = var.region
    backup_retention_days = var.retention_days
    backup_encryption     = var.encryption
  }
}