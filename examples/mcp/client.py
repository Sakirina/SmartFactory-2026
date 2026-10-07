#!/usr/bin/env python3
"""Dependency-free MCP Streamable HTTP client and three-definition acceptance."""
import argparse
import copy
import getpass
import hashlib
import json
import os
from pathlib import Path
import time
import urllib.error
import urllib.request
import uuid


MAX_DOCUMENT_PAGES = 1000


class Client:
    def __init__(self, base, token):
        self.base, self.token, self.sequence = base.rstrip('/'), token, 0

    def request(self, method, path, body=None):
        headers = {'Authorization': 'Bearer ' + self.token,
                   'Content-Type': 'application/json',
                   'Accept': 'application/json, text/event-stream',
                   'MCP-Protocol-Version': '2025-06-18'}
        data = None if body is None else json.dumps(body).encode()
        request = urllib.request.Request(self.base + path, data=data, headers=headers, method=method)
        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                raw = response.read()
                return response.status, json.loads(raw) if raw else None
        except urllib.error.HTTPError as error:
            return error.code, json.loads(error.read())

    def rpc(self, path, method, params):
        self.sequence += 1
        status, response = self.request('POST', path, {'jsonrpc': '2.0', 'id': self.sequence,
                                                       'method': method, 'params': params})
        if status != 200 or 'error' in response:
            raise RuntimeError(f'MCP {method} failed, HTTP {status}: {response}')
        return response['result']

    def tool(self, name, arguments, path='/mcp/draft'):
        result = self.rpc(path, 'tools/call', {'name': name, 'arguments': arguments})
        if result.get('isError'):
            raise RuntimeError(result['content'][0]['text'])
        return result['structuredContent']['result']


def collect_documents(client, tool, arguments=None, path='/mcp/read',
                      max_pages=MAX_DOCUMENT_PAGES):
    """Finish an authorized traversal, including empty continuation pages."""
    arguments = dict(arguments or {})
    arguments.setdefault('limit', 200)
    items, seen, exhausted_pages = [], set(), 0
    scope = None
    for page_number in range(1, max_pages + 1):
        try:
            page = client.tool(tool, arguments, path)
        except RuntimeError as error:
            raise RuntimeError(f'Incomplete {tool} traversal: {error}') from error
        if (not isinstance(page, dict) or not isinstance(page.get('items'), list)
                or any(not isinstance(item, dict) for item in page['items'])
                or type(page.get('has_more')) is not bool
                or not isinstance(page.get('scope'), dict)):
            raise RuntimeError(f'Incomplete {tool} traversal: invalid document page')
        if scope is None:
            scope = copy.deepcopy(page['scope'])
        items.extend(page['items'])
        exhausted_pages += bool(page.get('budget_exhausted'))
        if not page['has_more']:
            result = dict(page, items=items, scope=scope, pages_read=page_number,
                          budget_exhausted_pages=exhausted_pages)
            result.pop('next_after', None)
            return result
        cursor = page.get('next_after')
        if not isinstance(cursor, str) or not cursor or cursor in seen:
            raise RuntimeError(f'Incomplete {tool} traversal: missing or repeated next_after')
        seen.add(cursor)
        # The cursor belongs to this identity and filter; never decode or alter it.
        arguments['after'] = cursor
    raise RuntimeError(f'Incomplete {tool} traversal: exceeded {max_pages} pages')


def definition_content(value):
    definition = copy.deepcopy(value)
    definition.pop('_resources', None)
    return definition


def published_definitions(client):
    items = collect_documents(client, 'list_definitions')['items']
    if (any(not isinstance(item.get('id'), str) or not item['id'] for item in items)
            or len({item['id'] for item in items}) != len(items)):
        raise RuntimeError('Incomplete list_definitions traversal: invalid or duplicate definition ID')
    return sorted((definition_content(item) for item in items), key=lambda item: item['id'])


def draft_input(value):
    """Remove response decorations only at their protocol-defined levels."""
    draft = copy.deepcopy(value)
    draft.pop('_resources', None)
    decorated_version = draft.pop('draft_version', draft.get('version'))
    version = draft.get('version')
    if (type(version) is not int or version < 1
            or type(decorated_version) is not int or decorated_version != version):
        raise RuntimeError('Invalid or inconsistent draft revision')
    draft['definition'] = definition_content(draft['definition'])
    return draft


def draft_demo(client):
    before = published_definitions(client)
    originals = {}
    for kind in ('analysis', 'alarm', 'strategy'):
        originals[kind] = next((item for item in before
                                if item.get('kind') == kind and item.get('status') == 'published'), None)
        if originals[kind] is None:
            raise RuntimeError(f'draft-demo requires an authorized published {kind} definition')
    report = {'acceptance': 'A17', 'started_ms': int(time.time()*1000), 'drafts': []}
    for kind in ('analysis', 'alarm', 'strategy'):
        original = originals[kind]
        definition = definition_content(client.tool('get_definition', {'id': original['id']}, '/mcp/read'))
        identifier = 'mcp-demo-' + kind + '-' + uuid.uuid4().hex[:10]
        definition.update(id=identifier, name='MCP 示例 ' + kind, status='draft', version=0, effective_ms=0)
        draft = client.tool('save_draft', {'draft': {'id': identifier, 'definition': definition,
                                                   'base_version': 0}, 'expected_version': 0})
        draft = draft_input(draft)
        draft['definition']['name'] += '（修改后）'
        draft = client.tool('save_draft', {'draft': draft, 'expected_version': draft['version']})
        draft = draft_input(draft)
        validation = client.tool('validate_draft', {'id': identifier})
        if not validation['valid']:
            raise RuntimeError(f'{kind} draft validation failed: {validation}')
        difference = client.tool('diff_draft', {'id': identifier})
        report['drafts'].append({'id': identifier, 'kind': kind, 'version': draft['version'],
                                 'valid': validation['valid'], 'diff': difference})
    report['denied_api_requests'] = []
    for path in ('/api/sf/v1/executions', '/api/sf/v1/config',
                 '/api/sf/v1/drafts/' + report['drafts'][0]['id'] + '/publish'):
        status, _ = client.request('POST', path, {})
        if status != 403:
            raise RuntimeError(f'AI operation expected HTTP 403: {path}, got {status}')
        report['denied_api_requests'].append({'path': path, 'status': status})
    readonly = client.rpc('/mcp/read', 'tools/list', {})
    if any(tool['name'] == 'save_draft' for tool in readonly['tools']):
        raise RuntimeError('read endpoint advertised draft mutation')
    after = published_definitions(client)
    digest = lambda value: hashlib.sha256(json.dumps(value, sort_keys=True).encode()).hexdigest()
    report['published_before_sha256'] = digest(before)
    report['published_after_sha256'] = digest(after)
    if before != after:
        raise RuntimeError('published definitions changed during the demonstration')
    report['passed'] = True
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['discover', 'draft-demo'])
    parser.add_argument('--base-url', default='http://127.0.0.1:8090')
    parser.add_argument('--login', help='dedicated AI login; otherwise use SF_MCP_TOKEN')
    parser.add_argument('--output', type=Path)
    args = parser.parse_args()
    token = os.environ.get('SF_MCP_TOKEN', '')
    client = Client(args.base_url, token)
    if args.login:
        password = os.environ.get('SF_MCP_PASSWORD') or getpass.getpass('AI account password: ')
        status, result = client.request('POST', '/api/sf/v1/login', {'login': args.login, 'password': password})
        if status != 200:
            raise SystemExit('AI login failed')
        client.token = result['token']
    if not client.token:
        raise SystemExit('set SF_MCP_TOKEN or supply --login')
    endpoint = '/mcp/read' if args.command == 'discover' else '/mcp/draft'
    client.rpc(endpoint, 'initialize', {'protocolVersion': '2025-06-18', 'capabilities': {},
                                          'clientInfo': {'name': 'sf-python-example', 'version': '1.0'}})
    report = (draft_demo(client) if args.command == 'draft-demo' else
              collect_documents(client, 'discover_catalogue'))
    raw = json.dumps(report, ensure_ascii=False, indent=2) + '\n'
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(raw)
    else:
        print(raw, end='')


if __name__ == '__main__':
    main()
