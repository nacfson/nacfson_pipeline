variable "region" {
  type    = string
  default = "ap-chuncheon-1"
}

variable "tenancy_ocid" {
  type = string
}

variable "compartment_ocid" {
  type = string
}

variable "image_display_names" {
  type        = map(string)
  description = "Exact Canonical Ubuntu 24.04 Minimal platform image display names, keyed by arm64 and amd64."
}

variable "bastion_client_cidrs" {
  type = list(string)
}

variable "budget_alert_email" {
  type      = string
  sensitive = true
}

variable "kafka_runner_api_public_key" {
  type        = string
  default     = null
  description = "The control center runner's public API key. Leave unset until the control center exists; no runner API key is created while it is null."
}

variable "kafka_runner_email" {
  type        = string
  default     = "kafka-runner@nacfson.cloud"
  description = "Primary email for the kafka-runner service user (required on OCI Identity Domains tenancies)."
}

variable "ssh_authorized_keys" {
  type        = list(string)
  description = "OpenSSH public keys installed for the ubuntu user on every Kafka VM for OCI Bastion SSH port forwarding sessions."

  validation {
    condition     = length(var.ssh_authorized_keys) > 0 && alltrue([for k in var.ssh_authorized_keys : length(trimspace(k)) > 0])
    error_message = "ssh_authorized_keys must contain at least one non-empty OpenSSH public key."
  }
}
