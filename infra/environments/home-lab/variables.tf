variable "environment" {
  description = "Environment name."
  type        = string
  default     = "home-lab"
}

variable "base_domain" {
  description = "Public DNS base domain."
  type        = string
}

variable "admin_cidrs" {
  description = "CIDRs allowed for SSH and Kubernetes API."
  type        = list(string)
}

variable "application_node" {
  description = "Existing or planned application node (external/home-lab backend)."
  type = object({
    name         = string
    ipv4_address = string
    ssh_host     = optional(string, "")
    ssh_user     = optional(string, "ubuntu")
    ssh_port     = optional(number, 22)
    vcpus        = optional(number, 4)
    memory_gb    = optional(number, 16)
    root_disk_gb = optional(number, 100)
    data_disk = optional(object({
      enabled     = bool
      size_gb     = number
      device_hint = string
      mount_path  = string
    }), {
      enabled     = true
      size_gb     = 200
      device_hint = "/dev/sdb"
      mount_path  = "/var/lib/rancher/k3s/storage"
    })
    labels = optional(map(string), {})
  })
}

variable "registry_node" {
  description = "Optional separate registry node. Null keeps registry DNS pointed at the application node temporarily."
  type = object({
    name         = string
    ipv4_address = string
    ssh_host     = optional(string, "")
    ssh_user     = optional(string, "ubuntu")
    ssh_port     = optional(number, 22)
    vcpus        = optional(number, 2)
    memory_gb    = optional(number, 4)
    root_disk_gb = optional(number, 100)
    data_disk = optional(object({
      enabled     = bool
      size_gb     = number
      device_hint = string
      mount_path  = string
    }), {
      enabled     = true
      size_gb     = 200
      device_hint = "/dev/sdb"
      mount_path  = "/var/lib/registry"
    })
    labels = optional(map(string), {})
  })
  default = null
}

variable "dns_records" {
  description = "Optional extra or override DNS records keyed by short name or FQDN."
  type = map(object({
    type  = optional(string, "A")
    value = optional(string)
    ttl   = optional(number, 300)
  }))
  default = {}
}

variable "backup" {
  description = "Off-server backup destination. Must not be the application node."
  type = object({
    enabled        = optional(bool, true)
    kind           = optional(string, "s3_compatible")
    endpoint       = string
    bucket_or_path = string
    region         = optional(string, "auto")
    retention_days = optional(number, 30)
    encryption     = optional(string, "client_side")
  })
}

variable "handoff_dir" {
  description = "Directory for Ansible/Flux handoff artifacts relative to this environment."
  type        = string
  default     = "../../handoff"
}