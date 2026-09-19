output "base_domain" {
  value = var.base_domain
}

output "records" {
  description = "Desired DNS records. External backend does not call a DNS API."
  value       = local.normalized
}

output "auth_hostname" {
  value = "auth.${var.base_domain}"
}

output "issuer_url" {
  value = "https://auth.${var.base_domain}/realms/platform"
}

output "manager_hostname" {
  value = "manager.${var.base_domain}"
}

output "contract" {
  value = {
    base_domain          = var.base_domain
    manage_with_provider = var.manage_with_provider
    records              = local.normalized
  }
}