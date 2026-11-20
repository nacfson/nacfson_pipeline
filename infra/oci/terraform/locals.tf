locals {
  kafka = yamldecode(file("${path.module}/../../kafka/cluster.yaml"))

  placement = {
    "a1-1"    = { shape = "VM.Standard.A1.Flex", ocpus = 1, memory_gbs = 6, fault_domain = "FAULT-DOMAIN-1", arch = "arm64" }
    "a1-2"    = { shape = "VM.Standard.A1.Flex", ocpus = 1, memory_gbs = 6, fault_domain = "FAULT-DOMAIN-2", arch = "arm64" }
    "micro-1" = { shape = "VM.Standard.E2.1.Micro", ocpus = null, memory_gbs = null, fault_domain = "FAULT-DOMAIN-3", arch = "amd64" }
    "micro-2" = { shape = "VM.Standard.E2.1.Micro", ocpus = null, memory_gbs = null, fault_domain = "FAULT-DOMAIN-3", arch = "amd64" }
  }

  nodes   = { for n in local.kafka.nodes : n.name => merge(n, local.placement[n.name]) }
  brokers = { for name, n in local.nodes : name => n if contains(n.roles, "broker") }

  vcn_cidr          = "10.200.0.0/16"
  subnet_cidr       = "10.200.0.0/24"
  wireguard_port    = 51820
  boot_volume_gbs   = 50
  bastion_ssh_user  = "ubuntu"
  availability_zone = data.oci_identity_availability_domains.this.availability_domains[0].name
}

data "oci_identity_availability_domains" "this" {
  compartment_id = var.tenancy_ocid
}
