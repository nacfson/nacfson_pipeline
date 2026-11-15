output "budget_name" {
  description = "Resource name of the monthly budget."
  value       = google_billing_budget.monthly.name
}
