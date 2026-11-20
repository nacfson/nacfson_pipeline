#!/usr/bin/python3
import re

DOCUMENTATION = r"""
module: kafka_declared
short_description: Converge Kafka topics, topic configs, and ACLs to the declarations
description:
  - Reads topics, dynamic topic configs, and ACLs with the Kafka CLI tools, then creates, alters, or removes only what differs from infra/kafka/cluster.yaml.
  - Refuses to shrink partitions or change a replication factor.
options:
  topics:
    description: Declared topics, as rendered into kafka_topics.
    type: list
    elements: dict
    required: true
  identities:
    description: Declared identities with their grants, as rendered into kafka_identities.
    type: list
    elements: dict
    required: true
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

ACL_LINE = re.compile(r"^\s*\(principal=(?P<principal>[^,]+), host=(?P<host>[^,]+), operation=(?P<op>[A-Z_]+), permissionType=(?P<perm>[A-Z]+)\)\s*$")
RESOURCE_LINE = re.compile(r"^Current ACLs for resource `ResourcePattern\(resourceType=(?P<type>[A-Z_]+), name=(?P<name>.*), patternType=(?P<pattern>[A-Z]+)\)`:\s*$")
CONFIG_LINE = re.compile(r"^\s+(?P<key>[a-z0-9.]+)=(?P<value>\S*) sensitive=")
CLUSTER_NAME = "kafka-cluster"


def enum_name(op: str) -> str:
    return re.sub(r"(?<!^)(?=[A-Z])", "_", op).upper()


def camel(enum: str) -> str:
    return "".join(part.capitalize() for part in enum.split("_"))


def topic_configs(topic: dict) -> dict:
    return {
        "max.message.bytes": str(topic["max_message_bytes"]),
        "message.timestamp.type": topic["timestamp_type"],
        "min.insync.replicas": str(topic["min_insync_replicas"]),
        "retention.bytes": str(topic["retention_bytes"]),
        "retention.ms": str(topic["retention_ms"]),
        "segment.bytes": str(topic["segment_bytes"]),
    }


def parse_topic_list(out: str) -> set:
    return {line.strip() for line in out.splitlines() if line.strip()}


def parse_topic_describe(out: str) -> dict:
    head = next(line for line in out.splitlines() if line.startswith("Topic:") and "PartitionCount:" in line)
    fields = dict(part.split(": ", 1) for part in head.split("\t") if ": " in part)
    return {"partitions": int(fields["PartitionCount"]), "replication_factor": int(fields["ReplicationFactor"])}


def parse_dynamic_configs(out: str) -> dict:
    return {m.group("key"): m.group("value") for m in map(CONFIG_LINE.match, out.splitlines()) if m}


def parse_acls(out: str) -> set:
    acls, resource = set(), None
    for line in out.splitlines():
        r = RESOURCE_LINE.match(line)
        if r:
            resource = (r.group("type"), r.group("name"), r.group("pattern"))
            continue
        a = ACL_LINE.match(line)
        if a and resource:
            acls.add((*resource, a.group("principal"), a.group("op"), a.group("perm")))
    return acls


def desired_acls(identities: list) -> set:
    acls = set()
    for ident in identities:
        for grant in ident.get("grants", []):
            rtype = grant["resource"].upper()
            name = CLUSTER_NAME if grant["resource"] == "cluster" else grant["name"]
            for op in grant["ops"]:
                acls.add((rtype, name, "LITERAL", f"User:{ident['name']}", enum_name(op), "ALLOW"))
    return acls


def acl_args(acl: tuple) -> list:
    rtype, name, pattern, principal, op, perm = acl
    principal_flag = "--allow-principal" if perm == "ALLOW" else "--deny-principal"
    target = ["--cluster"] if rtype == "CLUSTER" else [f"--{rtype.lower()}", name]
    return [principal_flag, principal, "--operation", camel(op), *target, "--resource-pattern-type", pattern.lower()]


def plan(state: dict, topics: list, identities: list) -> tuple:
    actions, errors = [], []
    for topic in topics:
        name, want = topic["name"], topic_configs(topic)
        if name not in state["topics"]:
            cmd = ["kafka-topics.sh", "--create", "--topic", name, "--partitions", str(topic["partitions"]),
                   "--replication-factor", str(topic["replication_factor"])]
            for key, value in sorted(want.items()):
                cmd += ["--config", f"{key}={value}"]
            actions.append(cmd)
            continue
        shape = state["shapes"][name]
        if shape["replication_factor"] != topic["replication_factor"]:
            errors.append(f"{name}: replication factor is {shape['replication_factor']}, declared {topic['replication_factor']}; change it with a partition reassignment, not this module")
        if shape["partitions"] > topic["partitions"]:
            errors.append(f"{name}: has {shape['partitions']} partitions, declared {topic['partitions']}; Kafka cannot remove partitions")
        elif shape["partitions"] < topic["partitions"]:
            actions.append(["kafka-topics.sh", "--alter", "--topic", name, "--partitions", str(topic["partitions"])])
        have = state["configs"][name]
        changed = {k: v for k, v in want.items() if have.get(k) != v}
        extra = sorted(k for k in have if k not in want)
        if changed:
            actions.append(["kafka-configs.sh", "--alter", "--entity-type", "topics", "--entity-name", name,
                            "--add-config", ",".join(f"{k}={v}" for k, v in sorted(changed.items()))])
        if extra:
            actions.append(["kafka-configs.sh", "--alter", "--entity-type", "topics", "--entity-name", name,
                            "--delete-config", ",".join(extra)])
    want_acls = desired_acls(identities)
    for acl in sorted(want_acls - state["acls"]):
        actions.append(["kafka-acls.sh", "--add", *acl_args(acl)])
    for acl in sorted(state["acls"] - want_acls):
        actions.append(["kafka-acls.sh", "--remove", "--force", *acl_args(acl)])
    return actions, errors


def read_state(run, topics: list) -> dict:
    existing = parse_topic_list(run(["kafka-topics.sh", "--list"]))
    state = {"topics": existing, "shapes": {}, "configs": {}}
    for topic in topics:
        name = topic["name"]
        if name in existing:
            state["shapes"][name] = parse_topic_describe(run(["kafka-topics.sh", "--describe", "--topic", name]))
            state["configs"][name] = parse_dynamic_configs(run(["kafka-configs.sh", "--describe", "--entity-type", "topics", "--entity-name", name]))
    state["acls"] = parse_acls(run(["kafka-acls.sh", "--list"]))
    return state


def converge(run, topics: list, identities: list, apply: bool) -> dict:
    actions, errors = plan(read_state(run, topics), topics, identities)
    if errors:
        return {"failed": True, "msg": "; ".join(errors), "actions": actions}
    if apply:
        for action in actions:
            run(action)
    return {"changed": bool(actions), "actions": actions}


def main():
    from ansible.module_utils.basic import AnsibleModule

    module = AnsibleModule(
        argument_spec={
            "topics": {"type": "list", "elements": "dict", "required": True},
            "identities": {"type": "list", "elements": "dict", "required": True},
            "admin": {"type": "list", "elements": "str", "required": True},
            "admin_args": {"type": "list", "elements": "str", "required": True},
        },
        supports_check_mode=True,
    )
    p = module.params

    def run(cmd):
        tool, rest = cmd[0], cmd[1:]
        rc, out, err = module.run_command([*p["admin"], f"/opt/kafka/bin/{tool}", *p["admin_args"], *rest])
        if rc != 0:
            module.fail_json(msg=f"{tool} failed", cmd=cmd, rc=rc, stdout=out, stderr=err)
        return out

    result = converge(run, p["topics"], p["identities"], apply=not module.check_mode)
    if result.get("failed"):
        module.fail_json(**result)
    module.exit_json(**result)


if __name__ == "__main__":
    main()
