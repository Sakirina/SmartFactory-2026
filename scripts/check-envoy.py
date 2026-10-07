#!/usr/bin/env python3
"""Run the pinned Envoy against an existing application and verify TLS, mTLS and SSE."""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import secrets
import ssl
import subprocess
import tempfile
import time
from urllib.error import HTTPError, URLError
from urllib.parse import urlparse
from urllib.request import HTTPSHandler, ProxyHandler, Request, build_opener

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--upstream-url', required=True)
    parser.add_argument('--state-directory', type=Path, required=True)
    parser.add_argument('--pki-binary', type=Path, required=True)
    parser.add_argument('--output', type=Path, default=ROOT / '.local/evolution/envoy-runtime-verification.json')
    args = parser.parse_args()
    upstream = urlparse(args.upstream_url)
    if upstream.scheme != 'http' or upstream.hostname not in ['127.0.0.1', 'localhost'] or upstream.path not in ['', '/']:
        parser.error('upstream must be a local HTTP application')
    credentials = json.loads((args.state_directory / 'development-credentials.json').read_text())
    image = json.loads((ROOT / 'deploy/images.lock.json').read_text())['images']['envoy']['pinned']
    private = tempfile.TemporaryDirectory(prefix='smartfactory-envoy-check-', dir='/private/tmp')
    work = Path(private.name)
    os.chmod(work, 0o700)
    name = 'sf-envoy-check-' + secrets.token_hex(5)
    evidence = {'schema_version': 1, 'created_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'image': image, 'checks': [], 'startup_probe_attempts': [], 'resources': {'cpu_limit': 0.5, 'memory_limit_bytes': 134217728}}

    def pki(directory, node=None, client=False):
        command = [str(args.pki_binary), '--dir', str(directory)]
        command += ['--mode', 'init'] if node is None else ['--node', node]
        if client:
            command += ['--client']
        subprocess.run(command, check=True, capture_output=True)

    def check(name, **values):
        evidence['checks'].append({'name': name, 'passed': True, 'checked_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), **values})

    def call(url, context, body=None, token=None):
        headers = {'Content-Type': 'application/json'}
        if token:
            headers['Authorization'] = 'Bearer ' + token
        request = Request(url, data=None if body is None else json.dumps(body).encode(), headers=headers)
        opener = build_opener(ProxyHandler({}), HTTPSHandler(context=context))
        return opener.open(request, timeout=8)

    container = False
    try:
        certs = work / 'pki'
        pki(certs)
        pki(certs, 'web')
        pki(certs, 'edge-a', True)
        wrong = work / 'untrusted'
        pki(wrong)
        pki(wrong, 'untrusted-client', True)
        manager = {'@type': 'type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager',
                   'stat_prefix': 'web', 'route_config': {'name': 'web', 'virtual_hosts': [{'name': 'web', 'domains': ['*'], 'routes': [{'match': {'prefix': '/'}, 'route': {'cluster': 'cloud', 'timeout': '0s'}}]}]},
                   'http_filters': [{'name': 'envoy.filters.http.router', 'typed_config': {'@type': 'type.googleapis.com/envoy.extensions.filters.http.router.v3.Router'}}]}
        def listener(port, mutual):
            common = {'tls_certificates': [{'certificate_chain': {'filename': '/certs/web.pem'}, 'private_key': {'filename': '/certs/web.key'}}]}
            tls = {'@type': 'type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.DownstreamTlsContext', 'common_tls_context': common}
            if mutual:
                common['validation_context'] = {'trusted_ca': {'filename': '/certs/ca.pem'}}
                tls['require_client_certificate'] = True
            return {'name': 'mutual' if mutual else 'web', 'address': {'socket_address': {'address': '0.0.0.0', 'port_value': port}},
                    'filter_chains': [{'transport_socket': {'name': 'envoy.transport_sockets.tls', 'typed_config': tls}, 'filters': [{'name': 'envoy.filters.network.http_connection_manager', 'typed_config': manager}]}]}
        config = {'static_resources': {'listeners': [listener(8443, False), listener(9443, True)], 'clusters': [{'name': 'cloud', 'connect_timeout': '3s', 'type': 'STRICT_DNS', 'dns_lookup_family': 'V4_ONLY',
                  'load_assignment': {'cluster_name': 'cloud', 'endpoints': [{'lb_endpoints': [{'endpoint': {'address': {'socket_address': {'address': 'host.docker.internal', 'port_value': upstream.port}}}}]}]}}]}}
        config_path = work / 'web.json'
        config_path.write_text(json.dumps(config))
        evidence['configuration_sha256'] = hashlib.sha256(config_path.read_bytes()).hexdigest()
        evidence['verified_upstream'] = {'host': 'host.docker.internal', 'port': upstream.port, 'dns_lookup_family': 'V4_ONLY', 'route_prefix': '/', 'route_timeout': '0s'}
        result = subprocess.run(['docker', 'run', '--detach', '--rm', '--pull', 'never', '--platform', 'linux/amd64', '--user', '0:0', '--name', name,
                                 '--label', 'smartfactory.fixture=evolution-envoy', '--memory', '128m', '--memory-swap', '128m', '--cpus', '0.5',
                                 '--publish', '127.0.0.1::8443', '--publish', '127.0.0.1::9443', '--add-host', 'host.docker.internal:host-gateway',
                                 '--mount', f'type=bind,src={certs},dst=/certs,readonly', '--mount', f'type=bind,src={config_path},dst=/config/web.json,readonly',
                                 image, '-c', '/config/web.json', '--concurrency', '2', '--log-level', 'warning'], check=True, capture_output=True, text=True)
        container = True
        evidence['container_id'] = result.stdout.strip()
        def port(number):
            value = subprocess.check_output(['docker', 'port', name, str(number) + '/tcp'], text=True).strip()
            return int(value.rsplit(':', 1)[1])
        base = 'https://127.0.0.1:' + str(port(8443))
        mutual = 'https://127.0.0.1:' + str(port(9443))
        context = ssl.create_default_context(cafile=str(certs / 'ca.pem'))
        for attempt in range(60):
            try:
                with call(base + '/health', context) as response:
                    if json.load(response)['status'] == 'ok':
                        check('tls_application_health', status=response.status, startup_attempts=attempt+1)
                        break
            except (URLError, TimeoutError, ConnectionError) as error:
                probe = {'attempt': attempt+1, 'at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'type': type(error).__name__, 'message': str(error)}
                if isinstance(error, HTTPError):
                    probe['body'] = error.read(4096).decode(errors='replace')
                evidence['startup_probe_attempts'].append(probe)
                if attempt == 0:
                    print(json.dumps(probe), flush=True)
                time.sleep(0.5)
        else:
            raise RuntimeError('Envoy did not route application health')
        trusted = ssl.create_default_context(cafile=str(certs / 'ca.pem'))
        trusted.load_cert_chain(str(certs / 'edge-a.pem'), str(certs / 'edge-a.key'))
        with call(mutual + '/health', trusted) as response:
            assert json.load(response)['status'] == 'ok'
            check('mtls_trusted_client', status=response.status)
        untrusted = ssl.create_default_context(cafile=str(certs / 'ca.pem'))
        untrusted.load_cert_chain(str(wrong / 'untrusted-client.pem'), str(wrong / 'untrusted-client.key'))
        for label, invalid in [('mtls_missing_client', context), ('mtls_untrusted_client', untrusted)]:
            try:
                with call(mutual + '/health', invalid) as response:
                    response.read()
            except (URLError, OSError) as error:
                check(label, rejected_by_tls=True, error_type=type(error).__name__)
            else:
                raise RuntimeError(label + ' was accepted')
        try:
            call(base + '/api/sf/v1/entities', context)
        except HTTPError as response:
            assert response.code == 401
            assert response.headers.get('X-Request-ID')
            check('unauthenticated_response_preserved', status=response.code, request_id_present=True)
        else:
            raise RuntimeError('unauthenticated application request was accepted')
        with call(base + '/api/sf/v1/login', context, {'login': 'admin', 'password': credentials['password']}) as response:
            token = json.load(response)['token']
        with call(base + '/api/sf/v1/events?device_ids=climate-1&limit=100', context, token=token) as response:
            assert response.headers['Content-Type'].startswith('text/event-stream')
            assert response.headers.get('X-Request-ID')
            started = time.monotonic()
            times = []
            payload_sizes = []
            while len(times) < 3:
                line = response.readline()
                if not line:
                    raise RuntimeError('SSE ended before three data frames')
                if line.startswith(b'data: '):
                    json.loads(line[6:])
                    times.append(round(time.monotonic() - started, 3))
                    payload_sizes.append(len(line) - 6)
            assert times[-1] < 5 and times[1] > times[0]
            check('application_sse_unbuffered', frames=len(times), arrival_seconds=times, payload_bytes=payload_sizes, request_id_present=True)
        evidence['all_checks_passed'] = True
    except Exception as error:
        evidence['all_checks_passed'] = False
        evidence['error'] = str(error)
        raise
    finally:
        if container:
            evidence['container_logs'] = subprocess.run(['docker', 'logs', name], capture_output=True, text=True).stderr[-20000:]
            evidence['memory_observation'] = subprocess.run(['docker', 'stats', '--no-stream', '--format', '{{.MemUsage}}', name], capture_output=True, text=True).stdout.strip()
            subprocess.run(['docker', 'stop', '--time', '5', name], capture_output=True)
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(json.dumps(evidence, indent=2) + '\n')
        private.cleanup()
    print('Pinned Envoy TLS, mTLS trusted/rejected clients, HTTP authentication and three SSE frames passed')


if __name__ == '__main__':
    main()
