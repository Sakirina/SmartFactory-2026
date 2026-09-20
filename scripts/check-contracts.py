#!/usr/bin/env python3
"""Validate generated contracts and editable graphs with Draft 2020-12 semantics."""
import json
from pathlib import Path
from jsonschema import Draft202012Validator

root = Path(__file__).resolve().parents[1]
directory = root / "contracts/1.0"
schemas = list(directory.glob("*.schema.json"))
for path in schemas:
    Draft202012Validator.check_schema(json.loads(path.read_text()))
schema = json.loads((directory / "Definition.schema.json").read_text())
validator = Draft202012Validator(schema)
definitions = json.loads((root / "examples/factory/definitions.json").read_text())["definitions"]
for definition in definitions:
    validator.validate(definition)
    invalid = {**definition, "schema_version": "99.0"}
    assert not validator.is_valid(invalid), "unknown schema version was accepted"
    invalid = {**definition, "nodes": [{"id": "bad", "type": "unknown", "params": {}}]}
    assert not validator.is_valid(invalid), "unknown node was accepted"
spec = json.loads((directory / "openapi.json").read_text())
def walk(value):
    if isinstance(value, dict):
        reference = value.get("$ref", "")
        if reference.startswith("#/"):
            target = spec
            for segment in reference[2:].split("/"):
                target = target[segment]
        for child in value.values():
            walk(child)
    elif isinstance(value, list):
        for child in value:
            walk(child)
walk(spec)
for name, schema in spec["components"]["schemas"].items():
    Draft202012Validator.check_schema(schema)
print(json.dumps({"schemas": len(schemas), "example_definitions": len(definitions), "api_operations": sum(len(value) for value in spec["paths"].values()), "unknown_versions_rejected": True, "unknown_nodes_rejected": True, "references_valid": True}))
