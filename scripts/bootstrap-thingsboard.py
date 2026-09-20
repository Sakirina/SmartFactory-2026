#!/usr/bin/env python3
"""Enroll the dedicated tenant and Edge using the pinned ThingsBoard API."""
import argparse
import json
import os
from pathlib import Path
import secrets
import time
from urllib.error import HTTPError, URLError
from urllib.parse import urlparse, parse_qs
from urllib.request import Request, urlopen

root = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser()
parser.add_argument("--url", default="http://127.0.0.1:18080")
parser.add_argument("--state-directory", type=Path, default=root / ".local")
parser.add_argument("--edges", default="edge-a", help="comma-separated registered edge node identifiers")
args = parser.parse_args()
state = args.state_directory.resolve()
edge_ids = args.edges.split(",")
if not edge_ids or any(not item.replace("-", "").replace("_", "").isalnum() for item in edge_ids):
    raise SystemExit("Invalid edge identifiers")
credentials = json.loads((state / "deployment-credentials.json").read_text())

def request(method, path, value=None, token=None):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["X-Authorization"] = "Bearer " + token
    payload = None if value is None else json.dumps(value).encode()
    with urlopen(Request(args.url + path, data=payload, headers=headers, method=method), timeout=25) as response:
        data = response.read()
        if not data:
            return None
        try:
            return json.loads(data)
        except json.JSONDecodeError:
            return data.decode()

def login(username, password):
    return request("POST", "/api/auth/login", {"username": username, "password": password})["token"]

deadline = time.monotonic() + 600
while True:
    try:
        try:
            system_token = login("sysadmin@thingsboard.org", credentials["tb_sysadmin_password"])
        except HTTPError as error:
            if error.code != 401:
                raise
            system_token = login("sysadmin@thingsboard.org", "sysadmin")
            request("POST", "/api/auth/changePassword", {"currentPassword": "sysadmin", "newPassword": credentials["tb_sysadmin_password"]}, system_token)
            system_token = login("sysadmin@thingsboard.org", credentials["tb_sysadmin_password"])
        break
    except (URLError, TimeoutError, ConnectionError):
        if time.monotonic() > deadline:
            raise SystemExit("ThingsBoard did not become ready within ten minutes; inspect container logs.")
        time.sleep(3)

tenants = request("GET", "/api/tenants?pageSize=100&page=0", token=system_token)["data"]
tenant = next((item for item in tenants if item["title"] == "SmartFactory"), None)
if tenant is None:
    tenant = request("POST", "/api/tenant", {"title": "SmartFactory", "region": "Global", "additionalInfo": {"sf_deployment": "dedicated-customer"}}, system_token)
tenant_id = tenant["id"]["id"]
users = request("GET", f"/api/tenant/{tenant_id}/users?pageSize=100&page=0", token=system_token)["data"]
email = "platform@smartfactory.local"
user = next((item for item in users if item["email"] == email), None)
if user is None:
    user = request("POST", "/api/user?sendActivationMail=false", {"tenantId": tenant["id"], "authority": "TENANT_ADMIN", "email": email, "firstName": "SmartFactory", "lastName": "Service"}, system_token)
try:
    tenant_token = login(email, credentials["tb_admin_password"])
except HTTPError:
    link = request("GET", "/api/user/" + user["id"]["id"] + "/activationLink", token=system_token)
    activation = parse_qs(urlparse(link).query)["activateToken"][0]
    request("POST", "/api/noauth/activate?sendActivationMail=false", {"activateToken": activation, "password": credentials["tb_admin_password"]})
    tenant_token = login(email, credentials["tb_admin_password"])

chains = request("GET", "/api/ruleChains?pageSize=100&page=0&type=EDGE", token=tenant_token)["data"]
if not chains:
    chain = request("POST", "/api/ruleChain", {"name": "SmartFactory edge template", "type": "EDGE", "debugMode": False}, tenant_token)
    metadata = {"ruleChainId": chain["id"], "firstNodeIndex": 0, "nodes": [{"name": "Save telemetry", "type": "org.thingsboard.rule.engine.telemetry.TbMsgTimeseriesNode", "configurationVersion": 1, "configuration": {"defaultTTL": 2592000, "useServerTs": False, "processingSettings": {"type": "ON_EVERY_MESSAGE"}}}], "connections": []}
    request("POST", "/api/ruleChain/metadata", metadata, tenant_token)
    request("POST", "/api/ruleChain/" + chain["id"]["id"] + "/edgeTemplateRoot", token=tenant_token)

edges = request("GET", "/api/edges?pageSize=100&page=0", token=tenant_token)["data"]
registered=[]
for node in edge_ids:
    edge = next((item for item in edges if item["name"] == node), None)
    if edge is None:
        edge = request("POST", "/api/edge", {"name": node, "label": node, "type": "SmartFactory", "routingKey": secrets.token_urlsafe(18), "secret": secrets.token_urlsafe(32)}, tenant_token)
    registered.append(edge)
    with os.fdopen(os.open(state / ("tb-edge-"+node+".env"), os.O_CREAT | os.O_TRUNC | os.O_WRONLY, 0o600), "w") as stream:
        stream.write("CLOUD_ROUTING_KEY=" + edge["routingKey"] + "\nCLOUD_ROUTING_SECRET=" + edge["secret"] + "\n")
private = {"url": args.url, "username": email, "password": credentials["tb_admin_password"], "tenant_id": tenant_id, "edge": registered[0], "edges":registered}
with os.fdopen(os.open(state / "tb-bootstrap.json", os.O_CREAT | os.O_TRUNC | os.O_WRONLY, 0o600), "w") as stream:
    json.dump(private, stream, ensure_ascii=False)
with os.fdopen(os.open(state / "tb-edge.env", os.O_CREAT | os.O_TRUNC | os.O_WRONLY, 0o600), "w") as stream:
    stream.write("CLOUD_ROUTING_KEY=" + registered[0]["routingKey"] + "\nCLOUD_ROUTING_SECRET=" + registered[0]["secret"] + "\n")
print("Dedicated tenant and " + str(len(registered)) + " edges enrolled; private credentials saved in the selected state directory.")
