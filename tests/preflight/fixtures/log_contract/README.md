# Log Contract Preflight Fixtures

This directory contains minimal Kubernetes workload manifests testing the platform preflight gate rules (`check_log_contract`):
- `missing-annotation`: missing `logs.nacfson.cloud/<container>`
- `bad-annotation-value`: invalid declaration string
- `log-file-env`: forbidden log file environment variable
- `undeclared-log-volume`: undeclared log volume mount
- `declared-data-path-ok`: valid declared data path with reason
- `v0-plain`: bare `v0.0.0` version rejected in both clusters
- `v0-commit-in-vps-ok`: `v0.0.0-<commit>` allowed in both clusters
- `legacy-list-drift`: drift between manifests and `legacy-containers.txt`
- `done-regressed`: regressed migration row
