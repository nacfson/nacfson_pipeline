variable "enabled" {
  description = "Whether an off-server backup destination is declared."
  type        = bool
  default     = true
}

variable "kind" {
  description = "Backup destination kind."
  type        = string
  default     = "s3_compatible"
  validation {
    condition     = contains(["s3_compatible", "b2", "nfs_offnode", "none"], var.kind)
    error_message = "kind must be s3_compatible, b2, nfs_offnode, or none."
  }
}

variable "endpoint" {
  description = "API endpoint or NFS export host. Required unless kind is none or enabled is false."
  type        = string
  default     = ""
}

variable "bucket_or_path" {
  description = "Bucket name or NFS export path."
  type        = string
  default     = ""
}

variable "region" {
  description = "Optional region for S3-compatible targets."
  type        = string
  default     = "auto"
}

variable "retention_days" {
  description = "Declared retention for operational policy alignment."
  type        = number
  default     = 30
  validation {
    condition     = var.retention_days >= 7
    error_message = "retention_days must be at least 7."
  }
}

variable "encryption" {
  description = "Backup encryption mode expected at the destination or client."
  type        = string
  default     = "client_side"
  validation {
    condition     = contains(["client_side", "server_side", "both"], var.encryption)
    error_message = "encryption must be client_side, server_side, or both."
  }
}

variable "same_failure_domain_as_node" {
  description = "Must be false. Backups cannot live only on the application node."
  type        = bool
  default     = false
  validation {
    condition     = var.same_failure_domain_as_node == false
    error_message = "Backup destination must be outside the application node failure domain."
  }
}