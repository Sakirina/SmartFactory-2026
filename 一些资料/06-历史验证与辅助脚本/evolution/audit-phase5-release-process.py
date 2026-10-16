#!/usr/bin/env python3
"""Audit a retained, successful phase-five release-process evidence directory."""
import argparse
import collections
import datetime
import hashlib
import json
from pathlib import Path


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("directory", type=Path)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    root = args.directory.resolve()
    evidence, source = root / "evidence", root / "source"
    references, errors = [], []

    def record(path, expected=None, kind="evidence"):
        data = path.read_bytes()
        actual = hashlib.sha256(data).hexdigest()
        references.append({"path": str(path), "sha256": actual, "bytes": len(data), "kind": kind})
        if expected is not None and actual != expected:
            errors.append({"kind": "digest_mismatch", "path": str(path), "expected": expected, "actual": actual})
        return actual

    def load(path):
        record(path)
        return json.loads(path.read_text())

    result = load(root / "result.json")
    assert result["status"] == "passed"
    inputs = load(evidence / "compiled-inputs.json")
    canonical = json.dumps(inputs["files"], ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    canonical = canonical.replace("<", "\\u003c").replace(">", "\\u003e").replace("&", "\\u0026").encode()
    assert hashlib.sha256(canonical).hexdigest() == inputs["sha256"] == result["source_sha256"]
    expected_inputs = {str(source / name): expected for name, expected in inputs["files"].items()}
    for name, expected in inputs["files"].items():
        record(source / name, expected, "retained_local_build_input")
    retained_external = {}
    if (evidence / "external-compiled-inputs.json").exists():
        for item in load(evidence / "external-compiled-inputs.json"):
            retained_external[str(Path(item["path"]).resolve())] = item
            expected_inputs[item["path"]] = item["sha256"]
            record(Path(item["retained_copy"]), item["sha256"], "retained_external_build_input")
        for name in ("all-compiled-inputs-before.json", "all-compiled-inputs-after.json"):
            if load(evidence / name) != expected_inputs:
                errors.append({"kind": "global_compiler_input_mismatch", "path": str(evidence / name)})
    binaries = load(evidence / "binaries.json")
    for binary in binaries:
        record(Path(binary["path"]), binary["sha256"], "tested_binary")
    commands = load(evidence / "commands.json")
    command_input_checks = []
    for command in commands:
        assert command["exit_code"] == 0, command["name"]
        for key in ("stdout", "stderr"):
            record(Path(command[key]), kind="raw_command_log")
        if command.get("inputs_before"):
            before = load(Path(command["inputs_before"]))
            after = load(Path(command["inputs_after"]))
            matched = before == after == expected_inputs
            command_input_checks.append({"name": command["name"], "input_count": len(before), "matched": matched})
            if not matched or command.get("changed_inputs"):
                errors.append({"kind": "command_input_mismatch", "command": command["name"]})
    for path in sorted(evidence.glob("*.json")):
        record(path)

    lists = [evidence / "go-list-inputs.stdout.log"]
    if (evidence / "go-list-datatransfer-inputs.stdout.log").exists():
        lists.append(evidence / "go-list-datatransfer-inputs.stdout.log")
    raw = "\n".join(path.read_text() for path in lists)
    decoder, position, packages, external = json.JSONDecoder(), 0, 0, {}
    fields = ("GoFiles", "CgoFiles", "CFiles", "CXXFiles", "MFiles", "HFiles", "FFiles", "SFiles", "SwigFiles", "SwigCXXFiles", "SysoFiles", "EmbedFiles")
    while position < len(raw):
        while position < len(raw) and raw[position].isspace():
            position += 1
        if position == len(raw):
            break
        package, position = decoder.raw_decode(raw, position)
        packages += 1
        if package.get("Error") or package.get("DepsErrors"):
            errors.append({"kind": "go_list_package_error", "package": package.get("ImportPath")})
        base = Path(package["Dir"])
        for field in fields:
            for name in package.get(field, []):
                path = (base / name).resolve()
                if path.is_relative_to(source):
                    if str(path.relative_to(source)) not in inputs["files"]:
                        errors.append({"kind": "local_input_missing", "path": str(path)})
                    continue
                if str(path) not in external:
                    if retained_external:
                        if str(path) not in retained_external:
                            errors.append({"kind": "external_input_missing", "path": str(path)})
                        else:
                            external[str(path)] = retained_external[str(path)]["sha256"]
                    else:
                        external[str(path)] = record(path, kind="external_build_input_observed_after_run")

    assertions = load(evidence / "assertions.json")
    assert all(item["passed"] is True for item in assertions)
    processes = load(evidence / "processes.json")
    exits = [item for item in processes if "exit_code" in item]
    assert all(item["exit_code"] == (-9 if item["injected_sigkill"] else 0) for item in exits)
    http = load(evidence / "http.json")
    functional = [item["case"] for item in assertions if not item["case"].startswith("private-value-absent-")]
    report = {
        "reviewed_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "status": "passed" if not errors else "failed",
        "scope": "Actual process scenario, retained local build inputs, binary digests and original records. External inputs are verified against retained copies and build-time before/after manifests when present; earlier runs use observations after the run. Final specialist provenance and independent acceptance remain separate.",
        "result": str(root / "result.json"),
        "source_sha256": inputs["sha256"],
        "local_inputs": len(inputs["files"]),
        "go_packages": packages,
        "external_input_files": len(retained_external) if retained_external else len(external),
        "go_list_external_files": len(external),
        "external_inputs_retained": bool(retained_external),
        "command_input_checks": command_input_checks,
        "binaries": len(binaries),
        "commands": len(commands),
        "behavior_assertions": len(functional),
        "behavior_cases": functional,
        "plaintext_scan_assertions": len(assertions) - len(functional),
        "http_requests": len(http),
        "http_status_counts": dict(collections.Counter(str(item["status"]) for item in http)),
        "process_exits": len(exits),
        "intentional_sigkill": sum(item["injected_sigkill"] for item in exits),
        "node_ports_closed": result.get("node_ports_closed"),
        "kept_running_for_frontend": result.get("kept_running", False),
        "evidence_references": references,
        "errors": errors,
    }
    args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps({key: report[key] for key in ("status", "local_inputs", "external_input_files", "binaries", "commands", "behavior_assertions", "plaintext_scan_assertions", "http_requests", "process_exits", "intentional_sigkill", "errors")}))
    return bool(errors)


if __name__ == "__main__":
    raise SystemExit(main())
