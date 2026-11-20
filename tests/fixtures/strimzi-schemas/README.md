# Strimzi JSON schemas

`scripts/kafka-declarations.py check` (rule 8) validates the Strimzi render against these schemas with kubeconform. They are converted from the CRDs of the pinned Strimzi release. Nothing here is applied to a cluster.

The pinned release is 1.3.1, the first release whose `kafka-versions.yaml` lists Kafka 4.3.2.

## Regenerate

```bash
v=1.3.1
tmp=$(mktemp -d)
for f in 040-Crd-kafka 043-Crd-kafkatopic 044-Crd-kafkauser 045-Crd-kafkanodepool; do
  curl -sfo "$tmp/$f.yaml" "https://raw.githubusercontent.com/strimzi/strimzi-kafka-operator/$v/packaging/install/cluster-operator/$f.yaml"
done
python3 scripts/crd-to-jsonschema.py --out "tests/fixtures/strimzi-schemas/$v" "$tmp"/*.yaml
```

The converter closes every object that lists its properties (`additionalProperties: false`), so kubeconform's strict mode rejects unknown fields. It turns `x-kubernetes-int-or-string` into a string-or-integer choice and pins `apiVersion` and `kind`.

When the pin moves, update `STRIMZI_VERSION` in `scripts/kafka-declarations.py` and delete the old directory.
