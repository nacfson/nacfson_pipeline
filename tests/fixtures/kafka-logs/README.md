# Kafka log fixtures

These files are raw Kafka log output, captured from the local rehearsal with `./scripts/kafka-rehearsal.sh capture-logs`. They are never edited by hand. The ops agent's Lua rules (`infra/oci/ansible/roles/ops_agent/files/ops_events.lua`) are tested only against them, because guessed log lines let real bugs through.

Each file is the complete log of one node over the window of one event, so the rules must also ignore every unrelated line around it.

| File | Event | Node |
|---|---|---|
| `broker-fence-unfence.controller-<n>.log` | broker 3 paused 25 s: the active controller fences it, then unfences it | the active controller |
| `isr-shrink-expand.broker-<n>.log` | the same pause, seen by partition leaders: ISR shrinks without broker 3, then grows back | brokers 1 and 2 |
| `controller-leader-change.controller-<n>.log` | the active controller is stopped and another voter takes over | the new active controller |
| `broker-stop.broker-3.log` | `kafka` stopped on broker 3 (controlled shutdown) | broker 3 |
| `broker-stop.controller-<n>.log` | the same stop, seen by the active controller | the active controller |

The directory name is the Kafka version. When the image pin moves, capture into a new directory and rerun the Lua tests against it.

Kafka's controller logs reach stdout, and so the journal on the VMs, only because `infra/kafka/log4j2.yaml` replaces the image's default, which writes them to a separate file.
