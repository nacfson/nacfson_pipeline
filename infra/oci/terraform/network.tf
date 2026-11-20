resource "oci_core_vcn" "kafka" {
  compartment_id = var.compartment_ocid
  cidr_blocks    = [local.vcn_cidr]
  display_name   = "kafka"
  dns_label      = "kafka"
}

resource "oci_core_internet_gateway" "kafka" {
  compartment_id = var.compartment_ocid
  vcn_id         = oci_core_vcn.kafka.id
  display_name   = "kafka"
  enabled        = true
}

resource "oci_core_route_table" "kafka" {
  compartment_id = var.compartment_ocid
  vcn_id         = oci_core_vcn.kafka.id
  display_name   = "kafka"

  route_rules {
    destination       = "0.0.0.0/0"
    destination_type  = "CIDR_BLOCK"
    network_entity_id = oci_core_internet_gateway.kafka.id
  }
}

resource "oci_core_security_list" "kafka" {
  compartment_id = var.compartment_ocid
  vcn_id         = oci_core_vcn.kafka.id
  display_name   = "kafka"

  egress_security_rules {
    destination = "0.0.0.0/0"
    protocol    = "all"
  }

  ingress_security_rules {
    source   = "0.0.0.0/0"
    protocol = "17"
    udp_options {
      min = local.wireguard_port
      max = local.wireguard_port
    }
  }

  ingress_security_rules {
    source   = local.subnet_cidr
    protocol = "6"
    tcp_options {
      min = 9092
      max = 9093
    }
  }

  ingress_security_rules {
    source   = local.subnet_cidr
    protocol = "6"
    tcp_options {
      min = 22
      max = 22
    }
  }
}

resource "oci_core_subnet" "kafka" {
  compartment_id             = var.compartment_ocid
  vcn_id                     = oci_core_vcn.kafka.id
  cidr_block                 = local.subnet_cidr
  display_name               = "kafka"
  dns_label                  = "nodes"
  route_table_id             = oci_core_route_table.kafka.id
  security_list_ids          = [oci_core_security_list.kafka.id]
  prohibit_public_ip_on_vnic = false
}
