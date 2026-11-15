resource "google_kms_key_ring" "vault" {
  name     = local.shared.kms.key_ring
  location = local.shared.region

  lifecycle {
    prevent_destroy = true
  }
}

resource "google_kms_crypto_key" "openbao_unseal" {
  name     = local.shared.kms.crypto_key
  key_ring = google_kms_key_ring.vault.id
  purpose  = "ENCRYPT_DECRYPT"

  lifecycle {
    prevent_destroy = true
  }
}

resource "google_service_account" "openbao_kms" {
  account_id   = local.shared.kms.gsa_account_id
  display_name = "OpenBao Cloud KMS seal"
}

resource "google_kms_crypto_key_iam_member" "openbao_encrypt_decrypt" {
  crypto_key_id = google_kms_crypto_key.openbao_unseal.id
  role          = "roles/cloudkms.cryptoKeyEncrypterDecrypter"
  member        = "serviceAccount:${google_service_account.openbao_kms.email}"
}

resource "google_kms_crypto_key_iam_member" "openbao_viewer" {
  crypto_key_id = google_kms_crypto_key.openbao_unseal.id
  role          = "roles/cloudkms.viewer"
  member        = "serviceAccount:${google_service_account.openbao_kms.email}"
}

resource "google_service_account_iam_member" "openbao_workload_identity" {
  service_account_id = google_service_account.openbao_kms.name
  role               = "roles/iam.workloadIdentityUser"
  member             = "serviceAccount:${local.shared.workload_pool}[${local.kms_ksa_namespace}/${local.kms_ksa_name}]"

  depends_on = [google_container_cluster.this]
}
