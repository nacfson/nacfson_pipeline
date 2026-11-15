locals {
  node_pools = { for p in local.shared.node_pools : p.name => p }

  kms_ksa_namespace = split("/", local.shared.kms.ksa)[0]
  kms_ksa_name      = split("/", local.shared.kms.ksa)[1]
}
