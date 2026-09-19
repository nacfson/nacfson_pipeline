variable "admin_cidrs" {
  description = "CIDRs allowed to reach SSH and the Kubernetes API. Must not be 0.0.0.0/0 for production."
  type        = list(string)
  validation {
    condition     = length(var.admin_cidrs) > 0
    error_message = "admin_cidrs must contain at least one CIDR."
  }
}

variable "public_http" {
  description = "Expose TCP 80/443 publicly for ingress-nginx HTTP-01 and HTTPS."
  type        = bool
  default     = true
}

variable "extra_public_tcp_ports" {
  description = "Additional public TCP ports. Prefer empty for the single-node baseline."
  type        = list(number)
  default     = []
}

variable "kubernetes_api_port" {
  description = "Kubernetes API port to restrict to admin_cidrs."
  type        = number
  default     = 6443
}

variable "ssh_port" {
  description = "SSH port to restrict to admin_cidrs."
  type        = number
  default     = 22
}

variable "enforce_provider_firewall" {
  description = "When true, a cloud/proxmox backend may create provider firewall objects. External backend emits policy only."
  type        = bool
  default     = false
}