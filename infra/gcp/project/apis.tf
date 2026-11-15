# Google APIs the gke/ root and Spec 012 need (FR-004).
# disable_on_destroy = false: removing this file never switches an API off under running resources.
resource "google_project_service" "apis" {
  for_each = toset([
    "container.googleapis.com",
    "compute.googleapis.com",
    "cloudkms.googleapis.com",
    "artifactregistry.googleapis.com",
    "cloudbuild.googleapis.com",
    "iam.googleapis.com",
    "iamcredentials.googleapis.com",
    "billingbudgets.googleapis.com",
  ])

  service            = each.value
  disable_on_destroy = false
}
