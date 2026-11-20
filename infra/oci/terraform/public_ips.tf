data "oci_core_private_ips" "broker" {
  for_each   = local.brokers
  ip_address = oci_core_instance.node[each.key].private_ip
  subnet_id  = oci_core_subnet.kafka.id
}

resource "oci_core_public_ip" "broker" {
  for_each       = local.brokers
  compartment_id = var.compartment_ocid
  lifetime       = "RESERVED"
  display_name   = "kafka-${each.key}"
  private_ip_id  = data.oci_core_private_ips.broker[each.key].private_ips[0].id

  lifecycle {
    prevent_destroy = true
  }
}
