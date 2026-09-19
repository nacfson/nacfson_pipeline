output "application_node_ipv4" {
  value = module.application_node.ipv4_address
}

output "ssh_host" {
  value = module.application_node.ssh_host
}

output "base_domain" {
  value = module.dns.base_domain
}

output "auth_hostname" {
  value = module.dns.auth_hostname
}

output "issuer_url" {
  value = module.dns.issuer_url
}

output "firewall_summary" {
  value = module.firewall.summary
}

output "backup_endpoint" {
  value = module.backup_target.endpoint
}

output "handoff_files" {
  description = "Artifacts consumed by Ansible and operators."
  value = {
    json      = local_file.handoff_json.filename
    inventory = local_file.ansible_inventory_yaml.filename
    dns       = local_file.dns_records_yaml.filename
  }
}

output "next_owner" {
  value = "Ansible host bootstrap (k3s + Flux), then Flux reconciles platform-config"
}