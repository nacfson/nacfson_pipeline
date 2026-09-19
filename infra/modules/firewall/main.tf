terraform {
  required_version = ">= 1.6.0"
}

locals {
  public_ports = var.public_http ? concat([80, 443], var.extra_public_tcp_ports) : var.extra_public_tcp_ports

  rules = concat(
    [
      for cidr in var.admin_cidrs : {
        name        = "ssh-admin-${replace(cidr, "/", "-")}"
        direction   = "ingress"
        protocol    = "tcp"
        port        = var.ssh_port
        source_cidr = cidr
        action      = "allow"
        purpose     = "bootstrap-and-recovery-ssh"
      }
    ],
    [
      for cidr in var.admin_cidrs : {
        name        = "k8s-api-admin-${replace(cidr, "/", "-")}"
        direction   = "ingress"
        protocol    = "tcp"
        port        = var.kubernetes_api_port
        source_cidr = cidr
        action      = "allow"
        purpose     = "break-glass-kubernetes-api"
      }
    ],
    [
      for port in local.public_ports : {
        name        = "public-tcp-${port}"
        direction   = "ingress"
        protocol    = "tcp"
        port        = port
        source_cidr = "0.0.0.0/0"
        action      = "allow"
        purpose     = "ingress-nginx"
      }
    ],
  )

  policy = {
    enforce_provider_firewall = var.enforce_provider_firewall
    default_inbound           = "deny"
    rules                     = local.rules
    notes = [
      "Provider/host firewalls are complementary to Kubernetes NetworkPolicy.",
      "NodePort, kubelet, etcd, and overlay ports must remain non-public.",
      "Ansible applies host firewall; OpenTofu may apply provider firewall when enforce_provider_firewall is true.",
    ]
  }
}

resource "terraform_data" "firewall_policy" {
  input = local.policy
}