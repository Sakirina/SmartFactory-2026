#!/usr/bin/env python3
"""Run a bounded test and record OS process I/O counters, including descendants."""
import argparse
import ctypes
import datetime
import json
import os
from pathlib import Path
import platform
import signal
import subprocess
import time


def reader():
    if platform.system() == "Darwin":
        # sys/resource.h, rusage_info_v2: UUID then 18 uint64 fields.
        class Usage(ctypes.Structure):
            _fields_ = [("uuid", ctypes.c_ubyte * 16), ("values", ctypes.c_uint64 * 18)]
        lib = ctypes.CDLL("/usr/lib/libproc.dylib", use_errno=True)
        lib.proc_pid_rusage.argtypes = [ctypes.c_int, ctypes.c_int, ctypes.c_void_p]
        def read(pid):
            value = Usage()
            if lib.proc_pid_rusage(pid, 2, ctypes.byref(value)):
                return None
            return {"read_bytes": value.values[16], "write_bytes": value.values[17], "rss_bytes": value.values[6]}
        return read, "macOS proc_pid_rusage RUSAGE_INFO_V2"
    def read(pid):
        try:
            counters = dict(line.split(":", 1) for line in Path(f"/proc/{pid}/io").read_text().splitlines())
            status = dict(line.split(":", 1) for line in Path(f"/proc/{pid}/status").read_text().splitlines())
            return {"read_bytes": int(counters["read_bytes"]), "write_bytes": int(counters["write_bytes"]), "rss_bytes": int(status["VmRSS"].split()[0]) * 1024}
        except (FileNotFoundError, PermissionError, KeyError):
            return None
    return read, "Linux /proc/PID/io and /proc/PID/status"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--timeout", type=float, default=300)
    parser.add_argument("--max-write-mib", type=float, default=64)
    parser.add_argument("--sample-interval-ms", type=int, default=100)
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    command = args.command[1:] if args.command[:1] == ["--"] else args.command
    if not command or args.timeout <= 0 or args.max_write_mib <= 0 or args.sample_interval_ms < 10:
        parser.error("command and positive limits are required")
    read, method = reader()
    started = time.monotonic()
    process = subprocess.Popen(command, start_new_session=True)
    tracked = {process.pid}
    peaks = {}
    stop_reason = None
    try:
        while process.poll() is None:
            rows = [line.split(None, 2) for line in subprocess.check_output(["ps", "-axo", "pid=,ppid=,comm="], text=True).splitlines()]
            changed = True
            while changed:
                changed = False
                for row in rows:
                    if len(row) == 3 and int(row[1]) in tracked and int(row[0]) not in tracked:
                        tracked.add(int(row[0]))
                        changed = True
            for row in rows:
                if len(row) != 3 or int(row[0]) not in tracked:
                    continue
                pid = int(row[0])
                sample = read(pid)
                if sample is None:
                    continue
                entry = peaks.setdefault(pid, {"pid": pid, "process": row[2], "read_bytes": 0, "write_bytes": 0, "rss_bytes": 0})
                for key, value in sample.items():
                    entry[key] = max(entry[key], value)
            if sum(x["write_bytes"] for x in peaks.values()) > args.max_write_mib * 1024**2:
                stop_reason = "process write budget exceeded"
                break
            if time.monotonic() - started > args.timeout:
                stop_reason = "time budget exceeded"
                break
            time.sleep(args.sample_interval_ms / 1000)
    finally:
        if process.poll() is None:
            os.killpg(process.pid, signal.SIGINT)
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGTERM)
                process.wait(timeout=5)
    report = {"captured_at": datetime.datetime.now(datetime.timezone.utc).isoformat(), "method": method,
              "measurement": "largest sampled cumulative per-process counters; includes children observed while alive",
              "processes": list(peaks.values()), "sample_interval_ms": args.sample_interval_ms,
              "limitations": "Sampling may miss short-lived descendants and their final writes. The budget is a sampled stop condition, not an OS quota. Docker VM and host swap I/O are outside these process counters.",
              "sampled_write_bytes": sum(x["write_bytes"] for x in peaks.values()),
              "elapsed_seconds": time.monotonic() - started, "exit_code": process.returncode,
              "stop_reason": stop_reason, "max_write_mib": args.max_write_mib}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2) + "\n")
    print(f"I/O report: {args.output}")
    return process.returncode if stop_reason is None else 1


if __name__ == "__main__":
    raise SystemExit(main())
