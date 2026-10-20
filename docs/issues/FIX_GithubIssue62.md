# FIX_GithubIssue62

- Issue: https://github.com/nacfson/nacfson_pipeline/issues/62
- State: CLOSED on GitHub. This note did not re-check the cluster.
- Live contract: [`contracts/CONTRACT_ISOLATION_BUDGET.md`](../../contracts/CONTRACT_ISOLATION_BUDGET.md) §2. That section still describes the in-cluster allow-list. It does not name pn's public hosts.

## What is wrong

`proj-pn` NetworkPolicy egress allows DNS, `vault-db-proxy`, the gateway, and the in-namespace backend. It has no public `443` or `ipBlock` rule. The issue's claim is that any working public call is therefore leaving through a namespace that has no egress policy (`identity` or `platform`), or it is failing. The backend image's real public hosts are not in this repo.

## Contract

The live isolation contract matches the current allow-list. It does not say which public hosts pn may call. Closing the issue did not add that list.

## How to check

Hubble flows from `proj-pn` to `reserved:world`. `POLICY_DENIED` means the policy is doing what the contract says. A successful public call means some other path is open and is still undeclared.

## Fixed when

The required public hosts are named in the contract and allowed in one declared place: a platform egress proxy, or a platform-owned policy. Project manifests must not grow a private exception that preflight cannot see.

## Not checked

Hubble was not queried. The issue was closed without a matching contract edit found in this pass.
