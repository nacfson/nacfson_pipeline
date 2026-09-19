terraform {
  required_version = ">= 1.6.0"

  backend "local" {
    path = "terraform.tfstate"
  }
}

# Cloud environment stub for the same portable production target.
# Replace provider/backend and module implementations when a VPS API is chosen.
# Do not fork platform-config for cloud — only this environment should change.

variable "base_domain" {
  type = string
}

variable "admin_cidrs" {
  type = list(string)
}

output "status" {
  value = "cloud environment stub — select provider and wire modules/node|dns|firewall|backup_target implementations"
}

output "required_modules" {
  value = [
    "modules/node",
    "modules/dns",
    "modules/firewall",
    "modules/backup_target",
  ]
}

output "non_goals" {
  value = [
    "No Kubernetes resources in OpenTofu",
    "No k3s install",
    "No Flux/app manifests",
  ]
}