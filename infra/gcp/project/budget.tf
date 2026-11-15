data "google_project" "this" {
  project_id = local.shared.project_id
}

# Monthly budget with e-mail alerts to billing admins (FR-015, research R9).
# Steady state is about $65/month; alerts fire at 50%, 90% and 100% of $80.
resource "google_billing_budget" "monthly" {
  billing_account = var.billing_account
  display_name    = "${local.shared.project_id} monthly"

  budget_filter {
    projects = ["projects/${data.google_project.this.number}"]
  }

  amount {
    specified_amount {
      currency_code = "USD"
      units         = "80"
    }
  }

  threshold_rules {
    threshold_percent = 0.5
    spend_basis       = "CURRENT_SPEND"
  }
  threshold_rules {
    threshold_percent = 0.9
    spend_basis       = "CURRENT_SPEND"
  }
  threshold_rules {
    threshold_percent = 1.0
    spend_basis       = "CURRENT_SPEND"
  }

  depends_on = [google_project_service.apis]
}
