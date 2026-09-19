output "policy" {
  description = "Desired firewall policy for provider and/or Ansible host firewall."
  value       = local.policy
}

output "admin_cidrs" {
  value = var.admin_cidrs
}

output "public_tcp_ports" {
  value = local.public_ports
}

output "summary" {
  value = {
    admin_cidrs               = var.admin_cidrs
    public_tcp_ports          = local.public_ports
    ssh_port                  = var.ssh_port
    kubernetes_api_port           = var.kubernetes_api_port
    enforce_provider_firewall = var.enforce_provider_firewall
    rule_count                = length(local.rules)
  }
}