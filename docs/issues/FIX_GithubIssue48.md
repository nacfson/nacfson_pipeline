# FIX_GithubIssue48

- Issue: https://github.com/nacfson/nacfson_pipeline/issues/48
- State: CLOSED on GitHub. Floating release tags were not adopted.
- Live contract: none. [`DEPENDENCIES.md`](../../DEPENDENCIES.md) and the constitution require immutable image digests for hosted projects.

## What is wrong

Updating a project image meant editing the deployment YAML by hand. The request was for GitOps to notice a release tag in another project and rewrite the YAML to that tag.

## Contract

The platform rule is the opposite of a floating tag. Third-party images use pinned tags. Hosted project images use `@sha256:` digests. A release tag that moves would ship an image the preflight gate cannot name. Closing this issue did not add tag-following automation, and it should not.

## How to check

`deploy/projects/pn/` image fields contain `@sha256:`. There is no `:latest` and no floating release tag. Image updates go through a digest pin and a pull request, not a tag reconcile.

## Fixed when

The operator can update a pin without hand-editing the wrong file. The update still records a digest. Tag-following GitOps is not the fix.

## Not checked

The image-update skill and the closed PR were not re-run.
