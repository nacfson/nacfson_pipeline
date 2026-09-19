output "name" {
  description = "Logical node name."
  value       = local.node.name
}

output "role" {
  description = "Node role."
  value       = local.node.role
}

output "ipv4_address" {
  description = "Primary IPv4 address."
  value       = local.node.ipv4_address
}

output "ssh_host" {
  description = "SSH host for Ansible inventory."
  value       = local.node.ssh.host
}

output "ssh_user" {
  description = "SSH user for Ansible."
  value       = local.node.ssh.user
}

output "ssh_port" {
  description = "SSH port for Ansible."
  value       = local.node.ssh.port
}

output "data_disk" {
  description = "Data disk contract for Ansible mounts."
  value       = local.node.data_disk
}

output "labels" {
  description = "Capability labels only."
  value       = local.node.labels
}

output "ansible_host" {
  description = "Ansible inventory host entry fragment."
  value = {
    ansible_host = local.node.ssh.host
    ansible_user = local.node.ssh.user
    ansible_port = local.node.ssh.port
    node_role    = local.node.role
    arch         = local.node.capacity.architecture
    data_disk_enabled = local.node.data_disk.enabled
    data_disk_device  = local.node.data_disk.device_hint
    data_disk_mount   = local.node.data_disk.mount_path
  }
}

output "contract" {
  description = "Full node contract for handoff JSON."
  value       = local.node
}