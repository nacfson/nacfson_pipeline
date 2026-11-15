locals {
  # Node pools keyed by name, so each pool is created with for_each from shared-values.json.
  # Pool names are never written here as literals (contracts/shared-values.md check 9).
  node_pools = { for p in local.shared.node_pools : p.name => p }

  # "vault/openbao" -> namespace "vault", name "openbao".
  kms_ksa_namespace = split("/", local.shared.kms.ksa)[0]
  kms_ksa_name      = split("/", local.shared.kms.ksa)[1]
}
