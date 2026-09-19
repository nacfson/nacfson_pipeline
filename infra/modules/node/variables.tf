variable "name" {
  description = "Logical node name used in inventory and handoff artifacts."
  type        = string
}

variable "role" {
  description = "Node role. application hosts k3s/Flux; registry is a separate failure domain."
  type        = string
  validation {
    condition     = contains(["application", "registry"], var.role)
    error_message = "role must be application or registry."
  }
}

variable "ipv4_address" {
  description = "Primary IPv4 address reachable for SSH and public ingress (or LAN VIP)."
  type        = string
  validation {
    condition     = can(regex("^((25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\\.){3}(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)$", var.ipv4_address))
    error_message = "ipv4_address must be a valid IPv4 address."
  }
}

variable "ssh_host" {
  description = "SSH target host or IP. Defaults to ipv4_address when empty."
  type        = string
  default     = ""
}

variable "ssh_user" {
  description = "SSH user Ansible will use for host bootstrap."
  type        = string
  default     = "ubuntu"
}

variable "ssh_port" {
  description = "SSH port."
  type        = number
  default     = 22
  validation {
    condition     = var.ssh_port > 0 && var.ssh_port < 65536
    error_message = "ssh_port must be between 1 and 65535."
  }
}

variable "architecture" {
  description = "CPU architecture capability for scheduling contracts."
  type        = string
  default     = "amd64"
  validation {
    condition     = var.architecture == "amd64"
    error_message = "First production target requires amd64."
  }
}

variable "vcpus" {
  description = "Declared vCPU capacity for planning budgets."
  type        = number
  default     = 4
}

variable "memory_gb" {
  description = "Declared RAM in GiB for planning budgets."
  type        = number
  default     = 16
}

variable "root_disk_gb" {
  description = "Root disk size in GiB."
  type        = number
  default     = 100
}

variable "data_disk" {
  description = "Optional extra data disk for retained local-path volumes."
  type = object({
    enabled      = bool
    size_gb      = number
    device_hint  = string
    mount_path   = string
  })
  default = {
    enabled     = true
    size_gb     = 200
    device_hint = "/dev/sdb"
    mount_path  = "/var/lib/rancher/k3s/storage"
  }
}

variable "labels" {
  description = "Capability labels only. Never put provider IDs or Kubernetes nodeName pins here."
  type        = map(string)
  default     = {}
  validation {
    condition = alltrue([
      for k, v in var.labels : !contains(["nodeName", "provider_id", "instance_id", "hetzner_server_id"], k)
    ])
    error_message = "labels must not include provider or node identity keys."
  }
}

variable "provider_backend" {
  description = "Backend identifier for this node implementation (external, proxmox, cloud, ...)."
  type        = string
  default     = "external"
}