variable "billing_account" {
  description = "Billing account ID (XXXXXX-XXXXXX-XXXXXX) that pays for the project. Set it in Cloud Shell with `export TF_VAR_billing_account=...`. Never commit it."
  type        = string
  nullable    = false
}
