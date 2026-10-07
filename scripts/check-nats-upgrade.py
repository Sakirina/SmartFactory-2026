#!/usr/bin/env python3
"""Roll an isolated three-node JetStream fixture and retain replication evidence.

Prepare the root with prepare-site.py --root/--project, use this invocation's
private bind stores, and build the current coordination test binary first. Images
must be cached. All migrations keep the same server identities and store paths.
"""
import argparse
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import re
import shutil
import subprocess
import time
import urllib.request

TARGET = "library/nats@sha256:b2301863d84fda2cf8260d308203708f5231b2db34d821cc518d5c4c3c79780a"


def utc():
    return datetime.now(timezone.utc).isoformat()


class Fixture:
    def __init__(self, root, intermediate):
        self.root = root.resolve()
        self.compose = self.root / "deploy/compose.site.json"
        self.site = self.root / ".local/site"
        self.binary = self.root / "coordination.test"
        self.intermediate = intermediate
        self.config = json.loads(self.compose.read_text())
        self.project = self.config["name"]
        if not self.project.startswith("sf-message-"):
            raise ValueError("this check requires a separate sf-message-* Compose project")
        for service in self.config["services"].values():
            if service.get("labels", {}).get("sf.evolution.fixture") != self.project:
                raise ValueError("fixture label missing; existing deployments are not eligible")
        # The migration namespace and the independent coordination acceptance
        # namespace each reserve 4 x 512MiB. This fixture permits both sets of
        # reservations; production preparation retains its 2GiB file budget.
        for path in self.site.glob("nats-*.conf"):
            path.write_text(re.sub(r"max_file_store: [0-9]+", "max_file_store: 6442450944", path.read_text()))
        self.report = {"started_at": utc(), "directory": str(self.root), "project": self.project,
                       "target_image": TARGET, "intermediate_image": intermediate, "stages": [], "checks": {},
                       "fixture_resources": {"container_memory_bytes_per_node": 256 * 1024**2,
                                             "logical_max_file_storage_bytes_per_node": 6 * 1024**3,
                                             "production_max_file_storage_bytes_per_node": 2 * 1024**3}}
        self.commands = []

    def save(self):
        self.report["commands"] = self.commands
        (self.root / "verification.json").write_text(json.dumps(self.report, indent=2) + "\n")

    def run(self, argv, timeout=100, required=True, env=None):
        result = subprocess.run(argv, capture_output=True, text=True, timeout=timeout, env=env)
        filename = f"command-{len(self.commands)+1:03d}.txt"
        (self.root / filename).write_text(result.stdout + "\n--- stderr ---\n" + result.stderr)
        self.commands.append({"at": utc(), "argv": argv, "exit_code": result.returncode, "output": filename})
        self.save()
        if required and result.returncode:
            raise RuntimeError(result.stdout[-2000:] + result.stderr[-2000:])
        return result

    def dc(self, *args):
        return self.run(["docker", "compose", "-f", str(self.compose), *args])

    def status(self, node):
        return json.loads(self.run(["docker", "inspect", f"{self.project}-{node}-1"]).stdout)[0]

    def wait_ready(self):
        deadline = time.monotonic() + 90
        attempts = []
        while True:
            try:
                members = [json.load(urllib.request.urlopen(f"http://127.0.0.1:{18221+i}/jsz?raft=true", timeout=3)) for i in range(1, 4)]
                leaders = {m["meta_cluster"].get("leader") for m in members}
                leader = next(iter(leaders)) if len(leaders) == 1 else None
                leader_view = members[int(leader.split("-")[-1])-1]["meta_cluster"] if leader else {}
                replicas = leader_view.get("replicas", [])
                if leader and leader_view.get("cluster_size") == 3 and len(replicas) == 2 and all(p.get("current") and not p.get("offline") for p in replicas):
                    self.report.setdefault("readiness_attempts", []).append({"at": utc(), "result": "metadata_R3_current", "leader": leader, "preceding_attempts": attempts})
                    self.save()
                    return
                attempts.append({"at": utc(), "leaders": list(leaders), "leader_replicas": replicas})
            except Exception as error:
                attempts.append({"at": utc(), "error": str(error)})
            if time.monotonic() > deadline:
                raise RuntimeError("metadata quorum readiness timed out")
            time.sleep(1)

    def snapshot(self, stage):
        nodes = []
        for index in range(1, 4):
            data = {}
            for endpoint in ["varz", "routez", "jsz?accounts=true&streams=true&consumers=true&raft=true"]:
                with urllib.request.urlopen(f"http://127.0.0.1:{18221+index}/" + endpoint, timeout=5) as response:
                    data[endpoint.split("?")[0]] = json.load(response)
            info = self.status(f"nats-{index}")
            data.update({"configured_image": info["Config"]["Image"], "image_id": info["Image"], "memory_limit": info["HostConfig"]["Memory"]})
            nodes.append(data)
        name = stage + "-members.json"
        (self.root / name).write_text(json.dumps(nodes, indent=2) + "\n")
        leaders = {n["jsz"]["meta_cluster"].get("leader") for n in nodes}
        if len(leaders) != 1 or not next(iter(leaders)):
            raise RuntimeError("metadata leader agreement was not restored")
        for node in nodes:
            if node["jsz"]["meta_cluster"]["cluster_size"] != 3 or node["routez"]["num_routes"] < 2:
                raise RuntimeError("cluster members or routes missing")
            if not node["varz"].get("tls_required") or not node["varz"].get("tls_verify"):
                raise RuntimeError("client mutual TLS is not active")
            cluster = node["varz"]["cluster"]
            if not cluster.get("tls_required") or not cluster.get("tls_verify"):
                raise RuntimeError("peer route mutual TLS is not active")
        self.report["stages"].append({"stage": stage, "at": utc(), "members": name,
                                     "versions": [n["varz"]["version"] for n in nodes],
                                     "meta_leader": next(iter(leaders)), "mem_bytes": [n["varz"]["mem"] for n in nodes]})
        self.save()
        print(utc(), stage, self.report["stages"][-1]["versions"], next(iter(leaders)), flush=True)
        return next(iter(leaders))

    def verify(self, stage, step="verify"):
        import os
        self.wait_ready()
        environment = dict(os.environ, SF_SITE_MIGRATION_FIXTURE=str(self.site), SF_SITE_MIGRATION_STEP=step)
        for attempt in range(15):
            result = self.run([str(self.binary), "-test.v", "-test.run", "^TestPersistentThreeReplicaUpgrade$", "-test.timeout", "2m"], timeout=125, env=environment, required=False)
            if result.returncode == 0:
                break
            # At this line Open has failed before any fixture lease/checkpoint
            # action; retrying this startup probe cannot duplicate an action.
            if "infrastructure_test.go:75:" not in result.stdout or "context deadline exceeded" not in result.stdout or attempt == 14:
                raise RuntimeError(result.stdout + result.stderr)
            self.report.setdefault("startup_probe_attempts", []).append({"at": utc(), "stage": stage, "error": result.stdout, "next_attempt": attempt+2})
            self.save()
            time.sleep(2)
        output = self.site / "migration-result.json"
        target = self.root / (stage + "-checkpoint.json")
        shutil.copy2(output, target)
        result = json.loads(target.read_text())
        if not all(result["checks"].values()):
            raise RuntimeError("persistent checkpoint check failed")
        self.report["stages"].append({"stage": stage + "_checkpoint", "at": utc(), "result": target.name,
                                     "fence": result["execution"]["fence"], "revision": result["checkpoint_revision"],
                                     "sha256": result["checkpoint_sha256"]})
        self.save()

    def cold_copy(self, name):
        self.dc("stop", "-t", "30")
        destination = self.root / name
        destination.mkdir()
        for node in self.config["services"]:
            shutil.copytree(self.root / ("data-"+node), destination / node)
        shutil.copy2(self.site / "persistent-checkpoint.json", destination / "expected-checkpoint.json")
        files = [{"path": str(p.relative_to(destination)), "bytes": p.stat().st_size,
                  "sha256": hashlib.sha256(p.read_bytes()).hexdigest()} for p in sorted(destination.rglob("*")) if p.is_file()]
        (destination / "manifest.json").write_text(json.dumps(files, indent=2)+"\n")
        self.report["stages"].append({"stage": name, "at": utc(), "bytes": sum(f["bytes"] for f in files), "cold_copy": str(destination)})
        self.save()
        self.dc("start")
        time.sleep(4)
        self.verify(name + "_restart")

    def drain(self, node):
        name = f"{self.project}-{node}-1"
        self.run(["docker", "kill", "--signal", "USR2", name])
        started = time.monotonic()
        while True:
            state = self.status(node)["State"]
            if state["Status"] == "exited":
                break
            if time.monotonic() - started > 50:
                raise RuntimeError("lame-duck drain did not finish within fixture budget")
            time.sleep(2)
        self.run(["docker", "logs", name], required=False)
        if state["ExitCode"] or state["OOMKilled"]:
            raise RuntimeError("node did not drain normally: " + json.dumps(state))
        self.report["stages"].append({"stage": "drained_"+node, "at": utc(), "duration_seconds": round(time.monotonic()-started, 3), "exit_code": state["ExitCode"], "oom_killed": state["OOMKilled"]})
        self.save()

    def roll(self, image, version):
        leader = self.snapshot("before_roll_"+version)
        order = [n for n in self.config["services"] if n != leader] + [leader]
        for node in order:
            self.drain(node)
            self.config["services"][node]["image"] = image
            self.compose.write_text(json.dumps(self.config, indent=2)+"\n")
            self.dc("up", "-d", "--no-deps", "--pull", "never", node)
            time.sleep(3)
            self.verify(version + "_" + node)
            self.snapshot(version + "_" + node)

    def exercise(self):
        self.verify("source_2.11.9", "verify" if (self.site / "persistent-checkpoint.json").exists() else "seed")
        self.snapshot("source_2.11.9")
        self.cold_copy("source-cold-backup")
        self.roll(self.intermediate, "2.12")
        self.roll(TARGET, "2.14.7")
        self.report["checks"]["rolling_upgrade_old_signed_checkpoint_preserved"] = True
        self.verify("upgraded_takeover", "advance")
        self.report["checks"]["fence_increase_and_stale_owner_rejected"] = True
        self.dc("restart", "nats-2")
        time.sleep(4)
        self.verify("single_node_restart")
        self.snapshot("single_node_restart")
        self.cold_copy("target-cold-backup")
        self.snapshot("all_node_restart")
        self.report["checks"]["persistent_data_survives_single_and_all_node_restart"] = True
        import os
        environment = dict(os.environ, SF_SITE_INTEGRATION=str(self.site), SF_SITE_FAULTS="1", SF_SITE_RESET_TEST_BUCKETS="1")
        self.wait_ready()
        self.run([str(self.binary), "-test.v", "-test.run", "^TestRealThreeReplicaCoordination$", "-test.timeout", "3m"], timeout=185, env=environment)
        self.verify("after_quorum_fault")
        self.snapshot("after_quorum_fault")
        self.report["checks"]["current_code_real_coordination_including_quorum_fault"] = True


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--directory", type=Path, required=True)
    parser.add_argument("--intermediate-image", required=True)
    args = parser.parse_args()
    fixture = Fixture(args.directory, args.intermediate_image)
    try:
        fixture.exercise()
        fixture.report["all_checks_passed"] = True
    except Exception as error:
        fixture.report["all_checks_passed"] = False
        fixture.report["error"] = str(error)
        raise
    finally:
        fixture.dc("stop", "-t", "30")
        for node in fixture.config["services"]:
            fixture.run(["docker", "logs", f"{fixture.project}-{node}-1"], required=False)
        fixture.dc("down")
        fixture.report["completed_at"] = utc()
        fixture.report["temporary_project_removed"] = True
        fixture.save()


if __name__ == "__main__":
    main()
