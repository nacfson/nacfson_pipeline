# Contract: WireGuard Slot Agent (GKE side of the tunnel)

**Implements**: FR-008, FR-009, FR-022, FR-030, SC-009 | **Namespace**: `oci-link` (GKE only, removable after a move)

## Kubernetes objects

| Object | Owner | Contents |
|---|---|---|
| `Namespace/oci-link` | Flux | platform namespace, Restricted pod security not enforced (host network), admitted by preflight for this DaemonSet only |
| `Job/wg-slot-keygen` | Flux | creates `Secret/wg-slot-keys` and `ConfigMap/wg-slot-public` only if the Secret is absent; never overwrites |
| `Secret/wg-slot-keys` | keygen Job | `slot-0.key` … `slot-3.key` (WireGuard private keys) |
| `ConfigMap/wg-slot-public` | keygen Job | `slot-<n>.pub`, `slot-<n>.address` |
| `ConfigMap/wg-peers` | Flux | per broker: name, public key, reserved public IP, port 51820, VCN address `/32` |
| `Lease/wg-slot-0` … `wg-slot-3` | slot agent | holder = node name; annotations per [ops-events-and-metrics.md](ops-events-and-metrics.md) |
| `DaemonSet/wg-slot-agent` | Flux | `hostNetwork: true`, `NET_ADMIN` only, read-only `hostPath /var/lib/fluent-bit-state`, requests = limits 10m / 32Mi |
| `ServiceAccount`, `Role`, `RoleBinding` | Flux | leases get/list/watch/update, events create, both in `oci-link`; nodes get (ClusterRole) |

## Behaviour on each node

1. **Claim**: list the Leases. Take the lowest slot whose Lease has expired or whose holder is not a current Node, by an `update` with the observed `resourceVersion`. If the update conflicts, re-list and try the next slot. If no slot is free, emit Event `NoFreeSlot` (type Warning, reason `NoFreeSlot`) and retry every 30 s. Do not start a tunnel.
2. **Bring up** (idempotent):
   - interface `wg0` with private key `slot-<n>.key` and address `10.201.0.<n+1>/32`
   - one peer per entry in `wg-peers`, with `AllowedIPs` = that broker's `/32` and `PersistentKeepalive 25`
   - routes `10.200.0.11/32`, `.12/32`, `.13/32` dev `wg0`
   - nftables table `oci_link` with one rule: `oifname "wg0" snat to 10.201.0.<n+1>`
3. **Renew** every 30 s, writing the two metric annotations. Read the buffer size by summing `/var/lib/fluent-bit-state/chunks`. Read the handshake age with `wg show wg0 latest-handshakes`.
4. **Stale tunnel**: if every peer's handshake is older than 180 s, emit Event `TunnelStale` once. When it recovers, emit `TunnelRecovered`.
5. **Release** on SIGTERM: delete `wg0` and table `oci_link`, then clear the Lease holder.
6. **Never** touch any interface, route, or nftables table it did not create.

## OCI side (Ansible, per broker VM)

- `wg0` with the broker's own key (generated on the VM; the public key is copied into `wg-peers` through a pull request), `ListenPort 51820`
- one `[Peer]` per slot: public key from `wg-slot-public`, `AllowedIPs = 10.201.0.<n+1>/32`
- nftables on `wg0`: accept TCP 9092 to the VCN address, drop everything else

## Spike S1 and fallback

The SNAT rule must work for pod traffic under GKE Dataplane V2 (research R6). If quickstart Scenario 0 fails, the fallback switches `fluent-bit` and `ops-relay` to `hostNetwork: true` and replaces the SNAT rule with `ip route replace 10.200.0.0/24 dev wg0 src 10.201.0.<n+1>`. Everything else in this contract is unchanged.
