output "broker_public_ips" {
  value = { for name, ip in oci_core_public_ip.broker : name => ip.ip_address }
}

output "private_ips" {
  value = { for name, node in oci_core_instance.node : name => node.private_ip }
}

output "instance_ids" {
  value = { for name, node in oci_core_instance.node : name => node.id }
}

output "image_ids" {
  value = { for arch, images in data.oci_core_images.ubuntu : arch => images.images[0].id }
}

output "bastion_id" {
  value = oci_bastion_bastion.kafka.id
}
