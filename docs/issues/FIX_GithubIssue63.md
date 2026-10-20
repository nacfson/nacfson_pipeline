# FIX_GithubIssue63

- Issue: https://github.com/nacfson/nacfson_pipeline/issues/63
- State: OPEN. No load-balancer owner was chosen in this note.
- Live contract: [`contracts/CONTRACT_TLS_INGRESS.md`](../../contracts/CONTRACT_TLS_INGRESS.md) covers certificates and routes, not who binds host ports 80 and 443.

## What is wrong

Public traffic reaches Traefik on one VM. The repo does not declare a load balancer, a ServiceLB binding, or the firewall rules that open 80/443. `bootstrap/templates/k3s.service.j2` does not pass `--disable=traefik` or `--disable=servicelb`, so k3s defaults are doing the work. Those defaults are outside Flux.

## Contract

There is no live contract for the edge bind. `docs/tls-certificates.md` describes the current path, including manual OCI and host firewall steps. Writing that path into a contract is one of the issue's options. It is not done here. The owner still has to pick ServiceLB, Cilium LB-IPAM, an OCI load balancer, or a Cloudflare proxy.

## How to check

On the node, confirm which process owns 80 and 443, and that a k3s upgrade does not move that bind without a Git change. A preflight check cannot see a rule that is not in the repo.

## Fixed when

The chosen bind is declared in Git, or the current ServiceLB path is written down and a check fails when 80/443 are not on Traefik.

## Not checked

The VPS API tunnel (`127.0.0.1:6443`) was down. Node firewall rules were not inspected.
