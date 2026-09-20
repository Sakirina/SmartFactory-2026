#!/usr/bin/env python3
"""Verify 10,000 physical protocol events across duplicate delivery and gateway restart.

The simulator and cloud/edge services must be running. This command owns the
DataTransfer process, so stop an existing instance before starting this check.
"""
import argparse
import json
import os
from pathlib import Path
import signal
import sqlite3
import subprocess
import time
from urllib.request import Request, urlopen

root = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser()
parser.add_argument("--binary", default="/private/tmp/smartfactory-build/bin/datatransfer")
parser.add_argument("--count", type=int, default=10000)
parser.add_argument("--timeout", type=int, default=600)
args = parser.parse_args()
if args.count < 10:
    raise SystemExit("count must be at least 10")
evidence = {"check": "counter_duplicate_restart_disconnect", "requested_count": args.count, "started_ms": int(time.time()*1000)}
output = root / ".local/evidence/counter-reliability.json"
process = None
log = open(root / ".local/evidence/counter-gateway.log", "ab", buffering=0)

def api(path, value=None):
    request = Request("http://127.0.0.1:18083"+path, data=None if value is None else json.dumps(value).encode(), headers={"Content-Type":"application/json"})
    with urlopen(request, timeout=120) as response:
        return json.load(response)

def snapshot(name):
    with sqlite3.connect(f"file:{root / '.local' / (name+'.db')}?mode=ro", uri=True, timeout=10) as connection:
        count = connection.execute("SELECT count(*) FROM observations WHERE device_id='counter-1' AND key='pulse' AND definition_id=''").fetchone()[0]
        latest = connection.execute("SELECT data FROM latest WHERE device_id='counter-1' AND key='goods-count.total'").fetchone()
        return {"raw_count":count, "derived_total":None if latest is None else json.loads(latest[0])["value"]}

def start():
    global process
    process = subprocess.Popen([args.binary,"-config",str(root / ".local/datatransfer-simulator.yaml")],cwd=root,stdout=log,stderr=subprocess.STDOUT)
    deadline=time.monotonic()+15
    while time.monotonic()<deadline:
        if process.poll() is not None:
            raise RuntimeError("DataTransfer did not start; inspect counter-gateway.log")
        try:
            with urlopen("http://127.0.0.1:18082/healthz",timeout=1) as response:
                if response.status==200:return
        except Exception:
            time.sleep(0.1)
    # Startup is also confirmed by the process and actual delivery checks below.
    if process.poll() is not None:raise RuntimeError("DataTransfer startup failed")

def stop():
    global process
    if process is not None and process.poll() is None:
        process.send_signal(signal.SIGTERM)
        try: process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill();process.wait(timeout=5)
    process=None

try:
    evidence["before"]={name:snapshot(name) for name in ["edge","cloud"]}
    producer_before=api("/state")["devices"]["counter-1"]["total"]
    evidence["producer_before"]=producer_before
    start()
    first,disconnected=args.count//2,args.count//5
    api("/pulses",{"count":first,"duplicate_every":3})
    stop()
    api("/pulses",{"count":disconnected,"duplicate_every":2})
    evidence["pending_while_gateway_stopped"]=api("/state")["pending_events"]
    assert evidence["pending_while_gateway_stopped"]>=disconnected
    start()
    api("/pulses",{"count":args.count-first-disconnected,"duplicate_every":5})
    target=producer_before+args.count
    deadline=time.monotonic()+args.timeout
    while True:
        current={name:snapshot(name) for name in ["edge","cloud"]}
        producer=api("/state")
        passed=producer["pending_events"]==0
        for name,value in current.items():
            passed=passed and value["raw_count"]-evidence["before"][name]["raw_count"]==args.count and value["derived_total"]==target
        if passed:break
        if time.monotonic()>deadline:raise TimeoutError(f"count reconciliation timed out: {current}, pending={producer['pending_events']}")
        time.sleep(1)
    evidence.update(after=current,producer_after=producer["devices"]["counter-1"]["total"],pending_events=producer["pending_events"],passed=True,finished_ms=int(time.time()*1000))
    print("PASS:",args.count,"unique counter events committed on edge and cloud; duplicate, restart and disconnect recovery verified",flush=True)
except BaseException as error:
    evidence.update(passed=False,error=str(error),finished_ms=int(time.time()*1000))
    raise
finally:
    stop();log.close();output.write_text(json.dumps(evidence,indent=2,ensure_ascii=False)+"\n")
