resource "oci_identity_group" "kafka_runner" {
  compartment_id = var.tenancy_ocid
  name           = "kafka-runner"
  description    = "Control-center runner: managed SSH sessions on the Kafka Bastion only"
}

resource "oci_identity_user" "kafka_runner" {
  compartment_id = var.tenancy_ocid
  name           = "kafka-runner"
  description    = "Control-center runner for Kafka Ansible runs"
}

resource "oci_identity_user_group_membership" "kafka_runner" {
  group_id = oci_identity_group.kafka_runner.id
  user_id  = oci_identity_user.kafka_runner.id
}

resource "oci_identity_api_key" "kafka_runner" {
  user_id   = oci_identity_user.kafka_runner.id
  key_value = var.kafka_runner_api_public_key
}

resource "oci_identity_policy" "kafka_runner" {
  compartment_id = var.compartment_ocid
  name           = "kafka-runner-bastion-sessions"
  description    = "Managed SSH sessions on the Kafka Bastion as ubuntu, nothing else"
  statements = [
    "Allow group kafka-runner to use bastion in compartment id ${var.compartment_ocid} where target.bastion.ocid = '${oci_bastion_bastion.kafka.id}'",
    "Allow group kafka-runner to manage bastion-session in compartment id ${var.compartment_ocid} where ALL {target.bastion.ocid = '${oci_bastion_bastion.kafka.id}', target.bastion-session.username = '${local.bastion_ssh_user}'}",
    "Allow group kafka-runner to read instances in compartment id ${var.compartment_ocid}",
    "Allow group kafka-runner to read vcn in compartment id ${var.compartment_ocid}",
    "Allow group kafka-runner to read subnets in compartment id ${var.compartment_ocid}",
    "Allow group kafka-runner to read instance-agent-plugins in compartment id ${var.compartment_ocid}",
    "Allow group kafka-runner to read vnic-attachments in compartment id ${var.compartment_ocid}",
    "Allow group kafka-runner to read vnics in compartment id ${var.compartment_ocid}",
  ]
}
