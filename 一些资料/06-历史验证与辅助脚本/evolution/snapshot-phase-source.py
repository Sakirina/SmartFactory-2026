#!/usr/bin/env python3
"""Preserve coordinator-reviewed phase source without runtime data or caches."""
import argparse
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import subprocess
import tarfile


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", required=True)
    parser.add_argument("--phase", type=int, required=True)
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[2]
    destination = Path(args.output).resolve()
    destination.mkdir(parents=True, exist_ok=False)
    allowed = {"Platform", "DataTransfer", "Frontends", "contracts", "scripts",
               "deploy", "tests", "examples", "Baseline", "Wiki", ".github"}
    root_files = {"README.md", ".gitignore", ".gitattributes", "跨服务协作备忘.md"}
    listed = subprocess.check_output(
        ["git", "ls-files", "--cached", "--others", "--exclude-standard", "-z"],
        cwd=root).decode().split("\0")
    candidates = sorted(set(p for p in listed if p and
        (p.split("/")[0] in allowed or p in root_files) and
        not any(v.startswith("._") or v == ".DS_Store" for v in Path(p).parts)))
    selected = []
    absent = []
    for relative in candidates:
        path = root / relative
        if not path.is_file():
            absent.append(relative)
            continue
        if path.is_symlink() or not path.resolve().is_relative_to(root):
            raise RuntimeError(f"Unexpected source link: {relative}")
        selected.append(relative)
    files = {p: {"sha256": digest(root / p), "bytes": (root / p).stat().st_size}
             for p in selected}
    archive = destination / "source.tar.gz"
    with tarfile.open(archive, "w:gz", compresslevel=6) as output:
        for relative in selected:
            output.add(root / relative, arcname=relative, recursive=False)
    archive_differences = []
    with tarfile.open(archive, "r:gz") as saved:
        for member in saved:
            content = saved.extractfile(member).read()
            if hashlib.sha256(content).hexdigest() != files[member.name]["sha256"]:
                archive_differences.append(member.name)
    changed = [p for p in selected if digest(root / p) != files[p]["sha256"]]
    metadata = {}
    documents = root / "docs/evolution"
    for path in sorted(documents.rglob("*")):
        if not path.is_file() or path.suffix not in {".md", ".json"}:
            continue
        if any(v.startswith("._") or v == ".DS_Store" for v in path.parts):
            continue
        if path.is_symlink() or not path.resolve().is_relative_to(documents):
            raise RuntimeError(f"Unexpected phase document link: {path}")
        relative = str(path.relative_to(root))
        target = destination / "phase-records" / path.relative_to(documents)
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(path.read_bytes())
        metadata[relative] = {"path": str(target), "sha256": digest(target)}
    report = {
        "phase": args.phase,
        "created_at": datetime.now(timezone.utc).isoformat(),
        "status": "verified" if not changed and not archive_differences else "source_changed",
        "head": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root).decode().strip(),
        "files": files,
        "count": len(files),
        "total_source_bytes": sum(v["bytes"] for v in files.values()),
        "absent_tracked_candidates": absent,
        "changed_during_archive": changed,
        "archive_content_differences": archive_differences,
        "archive": {"path": str(archive), "sha256": digest(archive), "bytes": archive.stat().st_size},
        "phase_records": metadata,
        "scope": "Git-listed authored source, tests, contracts, build and public documentation; runtime data, browser output, dependencies and caches excluded"
    }
    (destination / "manifest.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps({k: report[k] for k in ["phase", "status", "count", "total_source_bytes", "archive", "changed_during_archive", "archive_content_differences"]}, ensure_ascii=False))
    if changed or archive_differences:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
