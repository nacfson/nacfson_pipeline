variable "base_domain" {
  description = "Public DNS base domain, e.g. example.com."
  type        = string
  validation {
    condition     = can(regex("^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$", var.base_domain))
    error_message = "base_domain must look like a DNS domain."
  }
}

variable "target_ipv4" {
  description = "IPv4 address all platform A records should target unless overridden."
  type        = string
}

variable "records" {
  description = "Map of short hostname (or FQDN) to record spec. type defaults to A."
  type = map(object({
    type  = optional(string, "A")
    value = optional(string)
    ttl   = optional(number, 300)
  }))
  default = {}
}

variable "manage_with_provider" {
  description = "When true, a future DNS provider implementation may create records. External backend only emits desired state."
  type        = bool
  default     = false
}