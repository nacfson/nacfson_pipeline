terraform {
  required_version = ">= 1.6.0"
}

locals {
  ssh_host = trimspace(var.ssh_host) != "" ? var.ssh_host : var.ipv4_address

  # Capability-only labels. Ansible/Flux may consume these; apps must not pin nodeName.
  capability_labels = merge(
    {
      "platform.processmanager.dev/arch" = var.architecture
      "platform.processmanager.dev/role" = var.role
    },
    var.labels,
  )

  node = {
    name            = var.name
    role            = var.role
    provider_backend = var.provider_backend
    ipv4_address    = var.ipv4_address
    ssh = {
      host = local.ssh_host
      user = var.ssh_user
      port = var.ssh_port
    }
    capacity = {
      architecture = var.architecture
      vcpus        = var.vcpus
      memory_gb    = var.memory_gb
      root_disk_gb = var.root_disk_gb
    }
    data_disk = var.data_disk
    labels    = local.capability_labels
  }
}

# External/home-lab backend records the intended node contract in state without calling a
# hypervisor API. Proxmox/cloud backends can replace this module body later while keeping outputs.
resource "terraform_data" "node_contract" {
  input = local.node

  lifecycle {
    precondition {
      condition     = var.role != "application" || (var.vcpus >= 4 && var.memory_gb >= 16)
      error_message = "application role requires at least 4 vCPUs and 16 GiB RAM."
    }
    precondition {
      condition     = var.role != "registry" || (var.vcpus >= 2 && var.memory_gb >= 4)
      error_message = "registry role requires at least 2 vCPUs and 4 GiB RAM."
    }
  }
}