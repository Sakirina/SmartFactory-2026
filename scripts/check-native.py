#!/usr/bin/env python3
"""Verify committed ingress -> TB rule chain -> derived storage on cloud and Edge."""
import argparse
import json
from pathlib import Path
import time
from urllib.parse import quote
from urllib.request import Request, urlopen

root = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--state-directory', type=Path, default=root / '.local')
parser.add_argument('--output', type=Path, default=root / '.local/evidence/native-roundtrip.json')
parser.add_argument('--profiles', choices=['cloud', 'edge', 'both'], default='both')
parser.add_argument('--cloud-url', default='http://127.0.0.1:8090')
parser.add_argument('--edge-url', default='http://127.0.0.1:8091')
parser.add_argument('--tb-cloud-url', default='http://127.0.0.1:18080')
parser.add_argument('--tb-edge-url', default='http://127.0.0.1:18081')
args = parser.parse_args()
private = json.loads((args.state_directory / "development-credentials.json").read_text())
native = json.loads((args.state_directory / "tb-bootstrap.json").read_text())
dependencies = json.loads((root / "deploy/dependencies.json").read_text())
evidence = {"check": "native_cloud_edge_roundtrip", "started_ms": int(time.time()*1000),
            "ce_version": dependencies["images"]["thingsboard"].rsplit(":", 1)[1],
            "edge_version": dependencies["images"]["thingsboard_edge"].rsplit(":", 1)[1],
            "sources": dependencies["sources"], "profiles": []}

def req(base, method, path, token, value=None, tb=False):
    headers = {"Content-Type": "application/json", ("X-Authorization" if tb else "Authorization"): "Bearer " + token}
    data = None if value is None else json.dumps(value).encode()
    with urlopen(Request(base+path, method=method, data=data, headers=headers), timeout=20) as res:
        raw = res.read()
        return json.loads(raw) if raw else None

profiles = [("cloud", args.cloud_url, args.tb_cloud_url), ("edge", args.edge_url, args.tb_edge_url)]
for name, base, tb in profiles:
    if args.profiles not in ['both', name]:
        continue
    user = req(base, "POST", "/api/sf/v1/login", "", {"login":"admin", "password":private["password"]})["token"]
    tb_token = req(tb, "POST", "/api/auth/login", "", {"username":native["username"], "password":native["password"]}, True)["token"]
    now = int(time.time()*1000)
    mid = f"native-evidence-{name}-{now}"
    batch = {"message_id":mid, "source_id":"edge-a", "critical":True, "event":{"kind":"native_verification"}, "points":[{"device_id":"climate-1", "key":"temperature", "value":22.5, "observed_ms":now, "quality":"GOOD", "time_source":"simulation"}]}
    first = req(base,"POST","/api/sf/v1/ingest",private["service_token"],batch)
    repeated = req(base,"POST","/api/sf/v1/ingest",private["service_token"],batch)
    assert first["committed"] and not first["duplicate"] and repeated["duplicate"]
    deadline = time.monotonic()+90
    while True:
        result = req(base,"GET",f"/api/sf/v1/data?device_ids=climate-1&from_ms={now}&to_ms={now}&limit=100",user)
        derived = [p for p in result["points"] if p["key"] == "climate-average.temperature"]
        if derived:
            entities = req(base,"GET","/api/sf/v1/entities",user)
            entity = next(e for e in entities if e["id"] == "climate-1")
            if not entity.get("tb_id"):
                time.sleep(1)
                continue
            timeseries = req(tb,"GET",f"/api/plugins/telemetry/DEVICE/{entity['tb_id']}/values/timeseries?keys=temperature,climate-average.temperature&startTs={now-1}&endTs={now+1}&agg=NONE&limit=100",tb_token,tb=True)
            if timeseries.get("temperature") and timeseries.get("climate-average.temperature"):
                break
        if time.monotonic()>deadline:
            raise SystemExit(f"{name}: native callback or projection did not complete")
        time.sleep(1)
    evidence["profiles"].append({"name":name,"message_id":mid,"ingress":first,"repeat":repeated,"derived":derived,"tb_timeseries":timeseries,"passed":True})
    print(name+": committed ingress, duplicate suppression, native callback and both telemetry projections passed")
evidence["finished_ms"]=int(time.time()*1000)
path=args.output
path.parent.mkdir(exist_ok=True)
path.write_text(json.dumps(evidence,ensure_ascii=False,indent=2)+"\n")
