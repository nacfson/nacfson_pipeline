variable "region" {
  type    = string
  default = "ap-seoul-1"
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
  type = string
}
