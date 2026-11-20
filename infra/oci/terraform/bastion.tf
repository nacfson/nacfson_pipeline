resource "oci_bastion_bastion" "kafka" {
  compartment_id               = var.compartment_ocid
  bastion_type                 = "STANDARD"
  name                         = "kafka"
  target_subnet_id             = oci_core_subnet.kafka.id
  client_cidr_block_allow_list = var.bastion_client_cidrs
  max_session_ttl_in_seconds   = 10800
}
