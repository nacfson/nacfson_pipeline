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
