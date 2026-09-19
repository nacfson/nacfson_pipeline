terraform {
  required_version = ">= 1.6.0"
}

locals {
  default_hosts = {
    "auth"     = {}
    "manager"  = {}
    "git"      = {}
    "registry" = {}
  }

  merged = merge(local.default_hosts, var.records)

  normalized = {
    for name, spec in local.merged :
    endswith(name, ".${var.base_domain}") ? name : "${name}.${var.base_domain}" => {
      type  = upper(try(spec.type, "A"))
      value = coalesce(try(spec.value, null), var.target_ipv4)
      ttl   = try(spec.ttl, 300)
    }
  }
}

resource "terraform_data" "dns_desired_state" {
  input = {
    base_domain           = var.base_domain
    manage_with_provider  = var.manage_with_provider
    records               = local.normalized
  }
}