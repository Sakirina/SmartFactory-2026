#!/usr/bin/env python3
"""Check the coordinator's requirement records and local evidence references."""
import collections
import datetime
import hashlib
import json
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
ACCEPTANCE = ROOT / "docs/evolution/acceptance.json"
EXPECTED_PHASE_COUNTS = {1: 9, 2: 12, 3: 7, 4: 10, 5: 7}


def audit():
    source = ACCEPTANCE.read_bytes()
    record = json.loads(source)
    requirements = record["requirements"]
    errors = []
    identifiers = [item["id"] for item in requirements]
    expected = [f"E{phase}-{number:02d}" for phase, count in EXPECTED_PHASE_COUNTS.items() for number in range(1, count + 1)]
    if sorted(identifiers) != sorted(expected):
        errors.append({"kind": "requirement_ids", "actual": identifiers, "expected": expected})
    phases = []
    for phase, count in EXPECTED_PHASE_COUNTS.items():
        items = [item for item in requirements if item["phase"] == phase]
        statuses = dict(collections.Counter(item["verification_status"] for item in items))
        phases.append({"phase": phase, "expected": count, "recorded": len(items), "verification": statuses})
        if len(items) != count:
            errors.append({"kind": "phase_count", "phase": phase, "expected": count, "actual": len(items)})
    references = []
    for item in requirements:
        if item["verification_status"] == "passed":
            if item["implementation_status"] != "complete" or not item["implementation"] or not item["evidence"]:
                errors.append({"kind": "incomplete_passed_record", "id": item["id"]})
        for field in ("implementation", "evidence"):
            for name in item[field]:
                path = ROOT / name
                reference = {"requirement": item["id"], "field": field, "path": name, "exists": path.exists()}
                references.append(reference)
                if not reference["exists"]:
                    errors.append({"kind": "missing_reference", **reference})
    all_passed = len(requirements) == sum(EXPECTED_PHASE_COUNTS.values()) and all(item["verification_status"] == "passed" for item in requirements)
    if record["overall_status"] == "complete" and (not all_passed or errors):
        errors.append({"kind": "premature_overall_completion"})
    report = {
        "checked_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "source_sha256": hashlib.sha256(source).hexdigest(),
        "overall_status": record["overall_status"],
        "all_requirements_passed": all_passed,
        "record_consistency_passed": not errors,
        "phases": phases,
        "reference_count": len(references),
        "errors": errors,
        "references": references,
        "scope": "Record consistency and reference existence; specialist and coordinator reviews establish behavioral verification.",
    }
    output = ROOT / ".local/evolution/acceptance-record-audit.json"
    output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps({key: report[key] for key in ("overall_status", "all_requirements_passed", "record_consistency_passed", "phases", "reference_count", "errors")}, ensure_ascii=False))
    return 0 if not errors else 1


if __name__ == "__main__":
    raise SystemExit(audit())
