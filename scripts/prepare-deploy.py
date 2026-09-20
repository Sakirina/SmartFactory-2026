#!/usr/bin/env python3
"""Generate private credentials and a digest-pinned native development stack."""
import json
import os
from pathlib import Path
import secrets

root = Path(__file__).resolve().parents[1]
state = root / ".local"
state.mkdir(mode=0o700, exist_ok=True)
credential_path = state / "deployment-credentials.json"
if not credential_path.exists():
    with os.fdopen(os.open(credential_path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600), "w") as stream:
        json.dump({key: secrets.token_urlsafe(32) for key in ["postgres_password", "tb_admin_password", "tb_sysadmin_password"]}, stream)
credentials = json.loads(credential_path.read_text())
env_path = state / "native.env"
with os.fdopen(os.open(env_path, os.O_CREAT | os.O_TRUNC | os.O_WRONLY, 0o600), "w") as stream:
    stream.write("POSTGRES_PASSWORD=" + credentials["postgres_password"] + "\n")
edge_env = state / "tb-edge.env"
if not edge_env.exists():
    with os.fdopen(os.open(edge_env, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600), "w") as stream:
        stream.write("# Filled by bootstrap-thingsboard.py after cloud tenant enrollment.\n")
images = json.loads((root / "deploy/images.lock.json").read_text())["images"]
logging = {"driver": "json-file", "options": {"max-size": "10m", "max-file": "3"}}
services = {}
def service(name, image, **kwargs):
    services[name] = {"image": images[image]["pinned"], "platform": "linux/amd64", "pull_policy": "never", "logging": logging, **kwargs}
for name, port, database in [("cloud-db", 54320, "thingsboard"), ("edge-db", 54321, "tb_edge")]:
    service(name, "postgres", restart="unless-stopped", environment={"POSTGRES_DB": database, "POSTGRES_PASSWORD": "${POSTGRES_PASSWORD:?run scripts/prepare-deploy.py}"}, ports=[f"127.0.0.1:{port}:5432"], volumes=[name + ":/var/lib/postgresql/data"], healthcheck={"test": ["CMD-SHELL", f"pg_isready -U postgres -d {database}"], "interval": "5s", "timeout": "3s", "retries": 20})
service("kafka", "kafka", restart="unless-stopped", environment={"KAFKA_NODE_ID": "1", "KAFKA_PROCESS_ROLES": "broker,controller", "KAFKA_LISTENERS": "PLAINTEXT://:9092,CONTROLLER://:9093", "KAFKA_ADVERTISED_LISTENERS": "PLAINTEXT://kafka:9092", "KAFKA_LISTENER_SECURITY_PROTOCOL_MAP": "CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT", "KAFKA_CONTROLLER_QUORUM_VOTERS": "1@kafka:9093", "KAFKA_CONTROLLER_LISTENER_NAMES": "CONTROLLER", "KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR": "1", "KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR": "1", "KAFKA_TRANSACTION_STATE_LOG_MIN_ISR": "1", "KAFKA_LOG_RETENTION_HOURS": "48", "KAFKA_LOG_DIRS": "/tmp/kraft-combined-logs", "KAFKA_HEAP_OPTS": "-Xms256m -Xmx512m"}, volumes=["kafka:/tmp/kraft-combined-logs"], healthcheck={"test": ["CMD-SHELL", "bash -ec 'exec 3<>/dev/tcp/127.0.0.1/9092'"], "interval": "10s", "timeout": "10s", "retries": 30})
jvm_limits = " -XX:MaxDirectMemorySize=128m -XX:ReservedCodeCacheSize=128m -XX:ActiveProcessorCount=4 -Xss512k"
cloud_env = {"SPRING_DATASOURCE_URL": "jdbc:postgresql://cloud-db:5432/thingsboard", "SPRING_DATASOURCE_USERNAME": "postgres", "SPRING_DATASOURCE_PASSWORD": "${POSTGRES_PASSWORD}", "TB_QUEUE_TYPE": "kafka", "TB_KAFKA_SERVERS": "kafka:9092", "TB_QUEUE_KAFKA_BOOTSTRAP_SERVERS": "kafka:9092", "EDGES_ENABLED": "true", "JS_EVALUATOR": "local", "JAVA_OPTS": "-Xms384m -Xmx768m -XX:MaxMetaspaceSize=384m" + jvm_limits, "TB_SERVICE_ID": "sf-cloud-native"}
service("kafka-storage-init", "runtime", user="0:0", command=["chown", "1000:1000", "/storage"], volumes=["kafka:/storage"])
services["kafka"]["depends_on"] = {"kafka-storage-init": {"condition": "service_completed_successfully"}}
dependencies = {"cloud-db": {"condition": "service_healthy"}, "kafka": {"condition": "service_healthy"}}
service("tb-install", "thingsboard", profiles=["install"], environment={**cloud_env, "INSTALL_TB": "true", "LOAD_DEMO": "false"}, depends_on=dependencies)
service("thingsboard", "thingsboard", restart="unless-stopped", environment=cloud_env, ports=["127.0.0.1:18080:8080", "127.0.0.1:17070:7070", "127.0.0.1:11883:1883"], extra_hosts=["host.docker.internal:host-gateway"], depends_on=dependencies, volumes=["tb-logs:/var/log/thingsboard"])
service("thingsboard-edge", "thingsboard_edge", profiles=["edge"], restart="unless-stopped", environment={"SPRING_DATASOURCE_URL": "jdbc:postgresql://edge-db:5432/tb_edge", "SPRING_DATASOURCE_USERNAME": "postgres", "SPRING_DATASOURCE_PASSWORD": "${POSTGRES_PASSWORD}", "CLOUD_RPC_HOST": "host.docker.internal", "CLOUD_RPC_PORT": "17071", "CLOUD_RPC_SSL_ENABLED": "false", "JAVA_OPTS": "-Xms256m -Xmx512m -XX:MaxMetaspaceSize=256m" + jvm_limits}, env_file=["../.local/tb-edge.env"], ports=["127.0.0.1:18081:8080", "127.0.0.1:11884:1883"], extra_hosts=["host.docker.internal:host-gateway"], depends_on={"edge-db": {"condition": "service_healthy"}, "thingsboard": {"condition": "service_started"}}, volumes=["tb-edge-data:/data", "tb-edge-logs:/var/log/tb-edge"])
config = {"name": "smartfactory-native", "services": services, "volumes": {name: {} for name in ["cloud-db", "edge-db", "kafka", "tb-logs", "tb-edge-data", "tb-edge-logs"]}}
(root / "deploy/compose.native.json").write_text(json.dumps(config, indent=2) + "\n")
print("Prepared digest-pinned compose and private credentials. No images were downloaded.")
