locals {
  # Safe defaults so module arguments remain evaluable when count = 0.
  registry = var.registry_node == null ? {
    name         = "unused-registry"
    ipv4_address = "127.0.0.1"
    ssh_host     = ""
    ssh_user     = "ubuntu"
    ssh_port     = 22
    vcpus        = 2
    memory_gb    = 4
    root_disk_gb = 100
    data_disk = {
      enabled     = true
      size_gb     = 200
      device_hint = "/dev/sdb"
      mount_path  = "/var/lib/registry"
    }
    labels = {}
  } : var.registry_node
}

module "application_node" {
  source = "../../modules/node"

  name             = var.application_node.name
  role             = "application"
  provider_backend = "external"
  ipv4_address     = var.application_node.ipv4_address
  ssh_host         = var.application_node.ssh_host
  ssh_user         = var.application_node.ssh_user
  ssh_port         = var.application_node.ssh_port
  vcpus            = var.application_node.vcpus
  memory_gb        = var.application_node.memory_gb
  root_disk_gb     = var.application_node.root_disk_gb
  data_disk        = var.application_node.data_disk
  labels           = var.application_node.labels
}

module "registry_node" {
  source = "../../modules/node"
  count  = var.registry_node == null ? 0 : 1

  name             = local.registry.name
  role             = "registry"
  provider_backend = "external"
  ipv4_address     = local.registry.ipv4_address
  ssh_host         = local.registry.ssh_host
  ssh_user         = local.registry.ssh_user
  ssh_port         = local.registry.ssh_port
  vcpus            = local.registry.vcpus
  memory_gb        = local.registry.memory_gb
  root_disk_gb     = local.registry.root_disk_gb
  data_disk        = local.registry.data_disk
  labels           = local.registry.labels
}

locals {
  registry_ipv4 = var.registry_node == null ? module.application_node.ipv4_address : module.registry_node[0].ipv4_address

  dns_records = merge(
    {
      auth     = { value = module.application_node.ipv4_address }
      manager  = { value = module.application_node.ipv4_address }
      git      = { value = module.application_node.ipv4_address }
      registry = { value = local.registry_ipv4 }
    },
    var.dns_records,
  )
}

module "dns" {
  source = "../../modules/dns"

  base_domain          = var.base_domain
  target_ipv4          = module.application_node.ipv4_address
  records              = local.dns_records
  manage_with_provider = false
}

module "firewall" {
  source = "../../modules/firewall"

  admin_cidrs               = var.admin_cidrs
  ssh_port                  = module.application_node.ssh_port
  enforce_provider_firewall = false
}

module "backup_target" {
  source = "../../modules/backup_target"

  enabled        = var.backup.enabled
  kind           = var.backup.kind
  endpoint       = var.backup.endpoint
  bucket_or_path = var.backup.bucket_or_path
  region         = var.backup.region
  retention_days = var.backup.retention_days
  encryption     = var.backup.encryption
}

locals {
  handoff = {
    schema_version = 1
    environment    = var.environment
    backend        = "external-home-lab"
    ownership = {
      opentofu = [
        "node-contract",
        "dns-desired-state",
        "firewall-policy",
        "backup-destination",
      ]
      ansible = [
        "host-hardening",
        "disk-mounts",
        "k3s-install",
        "flux-bootstrap",
        "host-firewall-enforcement",
      ]
      flux = [
        "platform-config-kubernetes-resources",
      ]
      processmanager = [
        "git-desired-state-mutations",
        "read-only-observation",
      ]
    }
    dns                = module.dns.contract
    firewall           = module.firewall.policy
    backup             = module.backup_target.contract
    application_node   = module.application_node.contract
    registry_node      = try(module.registry_node[0].contract, null)
    ansible_inventory = {
      all = {
        children = {
          application = {
            hosts = {
              (module.application_node.name) = module.application_node.ansible_host
            }
          }
          registry = {
            hosts = var.registry_node == null ? {} : {
              (module.registry_node[0].name) = module.registry_node[0].ansible_host
            }
          }
        }
      }
    }
    next_steps = [
      "Review infra/handoff/home-lab.json",
      "Create Ansible inventory from ansible_inventory",
      "Apply host firewall from firewall.policy via Ansible",
      "Publish DNS records from dns.records (manual or provider)",
      "Run Ansible: harden host, mount data disk, install pinned k3s, bootstrap Flux",
      "Point Flux at platform-config and resume live tasks from 4.2",
    ]
  }
}

resource "local_file" "handoff_json" {
  filename = "${path.module}/${var.handoff_dir}/home-lab.json"
  content  = jsonencode(local.handoff)
}

resource "local_file" "ansible_inventory_yaml" {
  filename = "${path.module}/${var.handoff_dir}/home-lab.inventory.yaml"
  content  = yamlencode(local.handoff.ansible_inventory)
}

resource "local_file" "dns_records_yaml" {
  filename = "${path.module}/${var.handoff_dir}/home-lab.dns.yaml"
  content = yamlencode({
    base_domain = module.dns.base_domain
    records     = module.dns.records
  })
}