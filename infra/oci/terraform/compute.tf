data "oci_core_images" "ubuntu" {
  for_each       = var.image_display_names
  compartment_id = var.compartment_ocid
  display_name   = each.value
}

resource "oci_core_instance" "node" {
  for_each            = local.nodes
  compartment_id      = var.compartment_ocid
  availability_domain = local.availability_zone
  fault_domain        = each.value.fault_domain
  display_name        = "kafka-${each.key}"
  shape               = each.value.shape

  dynamic "shape_config" {
    for_each = each.value.ocpus == null ? [] : [each.value]
    content {
      ocpus         = shape_config.value.ocpus
      memory_in_gbs = shape_config.value.memory_gbs
    }
  }

  create_vnic_details {
    subnet_id        = oci_core_subnet.kafka.id
    private_ip       = each.value.address
    assign_public_ip = !contains(keys(local.brokers), each.key)
    hostname_label   = each.key
  }

  source_details {
    source_type             = "image"
    source_id               = data.oci_core_images.ubuntu[each.value.arch].images[0].id
    boot_volume_size_in_gbs = local.boot_volume_gbs
  }

  metadata = {
    ssh_authorized_keys = join("\n", var.ssh_authorized_keys)
  }

  preserve_boot_volume = false
}
