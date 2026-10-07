#!/usr/bin/env python3
"""Exercise Kafka's persistent KRaft upgrade in an explicitly isolated fixture.

All images must already be cached. Only this invocation's labeled container and
data directory are modified; the source and pre-finalization cold copies remain
available with the evidence. No existing Compose project is started.
"""
import argparse
import base64
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import re
import shutil
import subprocess
import time
import uuid

SOURCE = "apache/kafka@sha256:01b9a4030e54c6068e66eb3ba4cb82c0d89238629ef1c30d79b86036bf89b1b7"
TARGET = "apache/kafka@sha256:ccd1314e47ec76909e01f86308b4dcf2064f19f7c89759234322314b0e319e26"
STORAGE_INIT = "library/alpine@sha256:3e9b4b680bfc9fb5269227cffbd6d42be39fbf7c0b908123913864aa4447e764"
CLIENT_HEAP = "-Xms32m -Xmx96m"
READY_COMMAND = "KAFKA_HEAP_OPTS='-Xms32m -Xmx96m' /opt/kafka/bin/kafka-broker-api-versions.sh --bootstrap-server 127.0.0.1:9092 > /dev/null 2>&1"


def utc():
    return datetime.now(timezone.utc).isoformat()


class Fixture:
    def __init__(self, directory):
        self.root = directory.resolve()
        self.root.mkdir(parents=True, exist_ok=False, mode=0o700)
        self.name = "sf-kafka-upgrade-" + uuid.uuid4().hex[:10]
        self.cluster_id = base64.urlsafe_b64encode(uuid.uuid4().bytes).decode().rstrip("=")
        self.topic = "sf-migration-records"
        self.group = "sf-migration-reader"
        self.data = self.root / "data"
        self.data.mkdir(mode=0o777)
        self.data.chmod(0o777)
        self.active = False
        self.commands = []
        self.report = {"started_at": utc(), "fixture": self.name, "directory": str(self.root),
                       "source_image": SOURCE, "target_image": TARGET, "resources": {
                           "memory_bytes": 2 * 1024**3, "cpus": 2,
                           "broker_heap_bytes": 256 * 1024**2, "client_heap_bytes": 96 * 1024**2,
                           "platform": "linux/amd64"}, "stages": [], "checks": {}}

    def save(self):
        self.report["commands"] = self.commands
        (self.root / "verification.json").write_text(json.dumps(self.report, indent=2) + "\n")

    def run(self, args, *, stdin=None, timeout=90, required=True):
        started = time.monotonic()
        result = subprocess.run(args, input=stdin, capture_output=True, text=True, timeout=timeout)
        number = len(self.commands) + 1
        log = f"command-{number:03d}.txt"
        (self.root / log).write_text(result.stdout + "\n--- stderr ---\n" + result.stderr)
        self.commands.append({"at": utc(), "argv": args, "exit_code": result.returncode,
                              "elapsed_seconds": round(time.monotonic() - started, 3), "output": log})
        self.save()
        if required and result.returncode:
            raise RuntimeError(f"command {number} exit {result.returncode}: {result.stderr[-3000:]}")
        return result

    def cli(self, name, *args, stdin=None, required=True, timeout=90):
        return self.run(["docker", "exec", "-i", "--env", "KAFKA_HEAP_OPTS=" + CLIENT_HEAP,
                         self.name, "/opt/kafka/bin/" + name + ".sh", *args],
                        stdin=stdin, required=required, timeout=timeout)

    def start(self, image, stage):
        # Host copies have the copying user's ownership. Restore Kafka's UID on
        # this fixture's bind directory before starting the unprivileged broker.
        self.run(["docker", "run", "--rm", "--pull", "never", "--platform", "linux/amd64", "--user", "0:0",
                  "--mount", f"type=bind,src={self.data},dst=/data", STORAGE_INIT, "chown", "-R", "1000:1000", "/data"])
        env = {
            "CLUSTER_ID": self.cluster_id, "KAFKA_NODE_ID": "1", "KAFKA_PROCESS_ROLES": "broker,controller",
            "KAFKA_LISTENERS": "PLAINTEXT://:9092,CONTROLLER://:9093",
            "KAFKA_ADVERTISED_LISTENERS": "PLAINTEXT://localhost:9092",
            "KAFKA_LISTENER_SECURITY_PROTOCOL_MAP": "CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT",
            "KAFKA_CONTROLLER_LISTENER_NAMES": "CONTROLLER", "KAFKA_INTER_BROKER_LISTENER_NAME": "PLAINTEXT",
            "KAFKA_CONTROLLER_QUORUM_VOTERS": "1@localhost:9093", "KAFKA_LOG_DIRS": "/data",
            "KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR": "1", "KAFKA_OFFSETS_TOPIC_NUM_PARTITIONS": "3",
            "KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR": "1", "KAFKA_TRANSACTION_STATE_LOG_MIN_ISR": "1",
            "KAFKA_GROUP_INITIAL_REBALANCE_DELAY_MS": "0", "KAFKA_LOG_SEGMENT_BYTES": "16777216",
            "KAFKA_LOG_RETENTION_BYTES": "67108864", "KAFKA_HEAP_OPTS": "-Xms128m -Xmx256m",
            "KAFKA_JVM_PERFORMANCE_OPTS": "-server -XX:+UseG1GC -XX:MaxDirectMemorySize=64m -XX:ReservedCodeCacheSize=64m -XX:ActiveProcessorCount=2 -Xss512k",
            "MALLOC_ARENA_MAX": "2",
        }
        args = ["docker", "run", "-d", "--name", self.name, "--label", "sf.evolution.fixture=" + self.name,
                "--platform", "linux/amd64", "--pull", "never", "--memory", "2g", "--memory-swap", "2g",
                "--cpus", "2", "--mount", f"type=bind,src={self.data},dst=/data",
                "--health-cmd", READY_COMMAND, "--health-interval", "15s", "--health-timeout", "20s",
                "--health-retries", "8", "--health-start-period", "30s",
                "--log-opt", "max-size=10m", "--log-opt", "max-file=2"]
        for name, value in env.items():
            args += ["--env", name + "=" + value]
        args += [image]
        self.run(args)
        self.active = True
        deadline = time.monotonic() + 150
        while True:
            status = self.run(["docker", "inspect", "--format", "{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{end}}", self.name]).stdout.strip()
            if status == "running healthy":
                break
            if status.startswith("exited") or time.monotonic() > deadline:
                self.run(["docker", "logs", self.name], required=False)
                raise RuntimeError("broker readiness failed: " + status)
            time.sleep(3)
        info = json.loads(self.run(["docker", "inspect", self.name]).stdout)[0]
        self.report["stages"].append({"stage": stage, "at": utc(), "image_id": info["Image"],
                                     "health": info["State"]["Health"], "configured_image": info["Config"]["Image"], "mounts": info["Mounts"]})
        self.save()
        print(utc(), stage, "healthy", flush=True)

    def stop(self, stage):
        if not self.active:
            return
        peak = self.run(["docker", "exec", self.name, "cat", "/sys/fs/cgroup/memory.peak"], required=False)
        if peak.returncode == 0:
            self.report["stages"].append({"stage": stage+"_memory", "at": utc(), "container_peak_bytes": int(peak.stdout.strip())})
        self.run(["docker", "stop", "-t", "45", self.name], timeout=60)
        state = json.loads(self.run(["docker", "inspect", self.name]).stdout)[0]["State"]
        self.report["stages"].append({"stage": stage + "_stopped", "at": utc(), "state": state})
        self.run(["docker", "logs", self.name], required=False)
        self.run(["docker", "rm", "-v", self.name])
        self.active = False
        self.save()
        if state["OOMKilled"]:
            raise RuntimeError("fixture OOM killed")

    def offsets(self):
        raw = self.cli("kafka-get-offsets", "--bootstrap-server", "localhost:9092", "--topic", self.topic).stdout
        return {int(part): int(end) for _, part, end in (line.split(":") for line in raw.splitlines() if line.startswith(self.topic + ":"))}

    def positions(self):
        raw = self.cli("kafka-consumer-groups", "--bootstrap-server", "localhost:9092", "--describe", "--group", self.group).stdout
        return {int(parts[2]): int(parts[3]) for line in raw.splitlines() if (parts := line.split()) and parts[0] == self.group and parts[1] == self.topic and parts[3] != "-"}

    def records(self, offsets):
        records = []
        for partition, end in sorted(offsets.items()):
            if end == 0:
                continue
            raw = self.cli("kafka-console-consumer", "--bootstrap-server", "localhost:9092", "--topic", self.topic,
                           "--partition", str(partition), "--offset", "0", "--max-messages", str(end), "--timeout-ms", "20000",
                           "--consumer-property", "enable.auto.commit=false", "--property", "print.partition=true",
                           "--property", "print.offset=true", "--property", "print.key=true").stdout
            current = [line for line in raw.splitlines() if line.startswith("Partition:")]
            if len(current) != end:
                raise RuntimeError(f"partition {partition}: expected {end} records, read {len(current)}")
            records.extend(current)
        return records

    def snapshot(self, stage):
        offsets = self.offsets()
        records = self.records(offsets)
        features = self.cli("kafka-features", "--bootstrap-server", "localhost:9092", "describe").stdout
        match = re.search(r"Feature: metadata.version.*?FinalizedVersionLevel: (\S+)", features)
        result = {"stage": stage, "at": utc(), "offsets": offsets, "positions": self.positions(), "records": records,
                  "records_sha256": hashlib.sha256("\n".join(records).encode()).hexdigest(),
                  "metadata_version": match.group(1) if match else None, "features": features}
        if result["metadata_version"] is None:
            raise RuntimeError("metadata version not found in features output")
        self.report["stages"].append(result)
        self.save()
        print(utc(), stage, offsets, result["positions"], result["metadata_version"], flush=True)
        return result

    def produce(self, phase, count):
        content = "".join(f"key-{i}:{{\"phase\":\"{phase}\",\"sequence\":{i}}}\n" for i in range(count))
        self.cli("kafka-console-producer", "--bootstrap-server", "localhost:9092", "--topic", self.topic,
                 "--property", "parse.key=true", "--property", "key.separator=:", stdin=content)

    def consume(self, count, stage):
        result = self.cli("kafka-console-consumer", "--bootstrap-server", "localhost:9092", "--topic", self.topic,
                          "--group", self.group, "--from-beginning", "--max-messages", str(count), "--timeout-ms", "20000",
                          "--consumer-property", "max.poll.records=1", "--consumer-property", "auto.commit.interval.ms=100",
                          "--consumer-property", "group.protocol=classic", "--property", "print.key=true")
        records = [line for line in result.stdout.splitlines() if line.startswith("key-")]
        if len(records) != count:
            raise RuntimeError("consumer did not read the requested records")
        self.report["stages"].append({"stage": stage, "at": utc(), "consumed_records": records})
        self.save()

    def backup(self, name):
        if self.active:
            raise RuntimeError("cold copy requires a stopped broker")
        target = self.root / name
        shutil.copytree(self.data, target)
        files = [{"file": str(p.relative_to(target)), "bytes": p.stat().st_size,
                  "sha256": hashlib.sha256(p.read_bytes()).hexdigest()} for p in sorted(target.rglob("*")) if p.is_file()]
        (self.root / (name + "-manifest.json")).write_text(json.dumps(files, indent=2) + "\n")
        self.report["stages"].append({"stage": name, "at": utc(), "cold_copy": str(target), "files": len(files),
                                     "bytes": sum(p["bytes"] for p in files), "manifest": name + "-manifest.json"})
        self.save()
        return target

    def exercise(self):
        self.start(SOURCE, "source_4.0.0")
        self.cli("kafka-topics", "--bootstrap-server", "localhost:9092", "--create", "--topic", self.topic,
                 "--partitions", "3", "--replication-factor", "1")
        self.produce("old", 30)
        self.consume(4, "source_partial_consume")
        old = self.snapshot("source_populated")
        assert len(old["offsets"]) == 3 and sum(old["offsets"].values()) == 30 and min(old["offsets"].values()) > 0
        assert sum(old["positions"].values()) == 4, old["positions"]
        self.stop("source")
        self.backup("source-cold-backup")
        self.start(TARGET, "target_binary_only")
        migrated = self.snapshot("target_before_metadata_update")
        for field in ("offsets", "positions", "records", "metadata_version"):
            assert old[field] == migrated[field], field
        self.report["checks"]["old_topics_partitions_content_offsets_positions_preserved"] = True
        self.consume(26, "remaining_old_records")
        self.produce("new", 12)
        self.consume(12, "new_records_same_consumer")
        before = self.snapshot("before_binary_rollback")
        assert sum(before["positions"].values()) == 42
        self.stop("target_binary_only")
        self.start(SOURCE, "old_binary_before_metadata_finalization")
        rolled = self.snapshot("binary_rollback_preserves_new_records_and_positions")
        for field in ("offsets", "positions", "records", "metadata_version"):
            assert before[field] == rolled[field], field
        self.report["checks"]["binary_rollback_before_metadata_finalization"] = True
        self.stop("old_binary_rollback")
        self.backup("pre-finalization-cold-backup")
        self.start(TARGET, "target_for_metadata_finalization")
        self.cli("kafka-features", "--bootstrap-server", "localhost:9092", "upgrade", "--release-version", "4.3")
        final = self.snapshot("metadata_finalized_4.3")
        assert final["metadata_version"] == "4.3-IV0", final["metadata_version"]
        denied = self.cli("kafka-features", "--bootstrap-server", "localhost:9092", "downgrade", "--metadata", old["metadata_version"], "--dry-run", required=False)
        assert denied.returncode != 0 and "downgrade" in (denied.stdout + denied.stderr).lower()
        self.report["checks"]["metadata_downgrade_rejected_after_finalization"] = True
        self.produce("post-finalization", 3)
        self.consume(3, "post_finalization_consumer")
        final_data = self.snapshot("finalized_written")
        self.stop("finalized_target")
        self.start(TARGET, "finalized_target_restart")
        restarted = self.snapshot("finalized_restart_preserves_data")
        for field in ("offsets", "positions", "records", "metadata_version"):
            assert final_data[field] == restarted[field], field
        self.report["checks"]["new_messages_and_positions_survive_finalized_restart"] = True
        self.stop("finalized_restart")
        final_dir = self.root / "finalized-data"
        self.data.rename(final_dir)
        self.data = self.root / "restored-data"
        shutil.copytree(self.root / "pre-finalization-cold-backup", self.data)
        manifest = json.loads((self.root / "pre-finalization-cold-backup-manifest.json").read_text())
        for item in manifest:
            assert hashlib.sha256((self.data/item["file"]).read_bytes()).hexdigest() == item["sha256"]
        # Read the copy through the same Docker bind source before using it.
        # A fresh source path avoids an observed Docker Desktop stale-directory
        # reference after rename/replacement of a previously mounted host path.
        check = self.run(["docker", "run", "--rm", "--pull", "never", "--platform", "linux/amd64", "--user", "0:0",
                          "--mount", f"type=bind,src={self.data},dst=/proof,readonly", STORAGE_INIT,
                          "sha256sum", *["/proof/"+item["file"] for item in manifest]])
        actual = {line.split(None, 1)[1].removeprefix("/proof/"): line.split(None, 1)[0] for line in check.stdout.splitlines()}
        assert all(actual[item["file"]] == item["sha256"] for item in manifest)
        self.report["checks"]["restored_copy_all_files_match_cold_copy_sha256"] = True
        self.start(SOURCE, "restore_cold_copy_to_4.0.0")
        restored = self.snapshot("cold_copy_restored_at_record_42")
        for field in ("offsets", "positions", "records", "metadata_version"):
            assert before[field] == restored[field], field
        self.report["checks"]["cold_copy_restore_after_metadata_finalization"] = True
        self.report["recovery_cutoff"] = {"restored_records": 42, "finalized_records": 45, "records_after_copy_require_replay": 3}
        self.stop("restored_source")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--directory", type=Path, required=True, help="a new private fixture directory")
    args = parser.parse_args()
    fixture = Fixture(args.directory)
    try:
        fixture.exercise()
        fixture.report["all_checks_passed"] = True
    except Exception as error:
        fixture.report["error"] = str(error)
        fixture.report["all_checks_passed"] = False
        raise
    finally:
        fixture.stop("cleanup")
        fixture.report["completed_at"] = utc()
        fixture.report["temporary_container_removed"] = not fixture.active
        fixture.save()


if __name__ == "__main__":
    main()
