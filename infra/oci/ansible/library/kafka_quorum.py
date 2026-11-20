#!/usr/bin/python3
import json

DOCUMENTATION = r"""
module: kafka_quorum
short_description: Read the KRaft controller quorum
description:
  - Runs kafka-metadata-quorum describe --status and --replication and returns the leader, voters, observers, directory IDs, and per-node lag.
  - Read only. Playbooks run add-controller and remove-controller themselves with the values it returns.
options:
  admin:
    description: Command prefix that runs a Kafka tool, for example podman exec kafka.
    type: list
    elements: str
    required: true
  admin_args:
    description: Arguments every tool gets, the bootstrap servers and the admin client config.
    type: list
    elements: str
    required: true
"""


def parse_status(out: str) -> dict:
    fields = {}
    for line in out.splitlines():
        key, sep, value = line.partition(":")
        if sep:
            fields[key.strip()] = value.strip()
    voters = json.loads(fields.get("CurrentVoters", "[]"))
    observers = json.loads(fields.get("CurrentObservers", "[]"))
    return {
        "cluster_id": fields.get("ClusterId", ""),
        "leader": int(fields.get("LeaderId", "-1")),
        "max_follower_lag": int(fields.get("MaxFollowerLag", "-1")),
        "voters": [{"id": v["id"], "directory_id": v["directoryId"]} for v in voters],
        "observers": [{"id": o["id"], "directory_id": o["directoryId"]} for o in observers],
    }


def parse_replication(out: str) -> list:
    rows = []
    lines = [line for line in out.splitlines() if line.strip()]
    for line in lines[1:]:
        cols = line.split()
        if len(cols) < 7 or not cols[0].isdigit():
            continue
        rows.append({"node_id": int(cols[0]), "directory_id": cols[1], "log_end_offset": int(cols[2]),
                     "lag": int(cols[3]), "status": cols[6]})
    return rows


def read(run) -> dict:
    status = parse_status(run(["describe", "--status"]))
    status["replication"] = parse_replication(run(["describe", "--replication"]))
    return status


def main():
    from ansible.module_utils.basic import AnsibleModule

    module = AnsibleModule(
        argument_spec={
            "admin": {"type": "list", "elements": "str", "required": True},
            "admin_args": {"type": "list", "elements": "str", "required": True},
        },
        supports_check_mode=True,
    )

    def run(args):
        cmd = [*module.params["admin"], "/opt/kafka/bin/kafka-metadata-quorum.sh", *module.params["admin_args"], *args]
        rc, out, err = module.run_command(cmd)
        if rc != 0:
            module.fail_json(msg="kafka-metadata-quorum failed", cmd=cmd, rc=rc, stdout=out, stderr=err)
        return out

    module.exit_json(changed=False, quorum=read(run))


if __name__ == "__main__":
    main()
