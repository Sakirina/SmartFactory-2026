"""Exercise the public MCP example against paginated and decorated responses."""
import copy
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location('mcp_example', ROOT / 'examples/mcp/client.py')
example = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(example)
LARGE_INTEGER = 9223372036854775806


def page(items, has_more=False, cursor=None, exhausted=False):
    result = {'items': copy.deepcopy(items), 'has_more': has_more,
              'scope': {'limit': 200}, 'scan_budget': 512,
              'budget_exhausted': exhausted}
    if cursor is not None:
        result['next_after'] = cursor
    return result


class PageClient:
    def __init__(self, pages):
        self.pages = iter(pages)
        self.calls = []

    def tool(self, name, arguments, path='/mcp/draft'):
        self.calls.append((name, copy.deepcopy(arguments), path))
        response = next(self.pages)
        if isinstance(response, Exception):
            raise response
        return copy.deepcopy(response)


def definition(kind):
    return {'id': 'published-' + kind, 'kind': kind, 'name': kind,
            'version': LARGE_INTEGER, 'status': 'published',
            'schema_version': '1.0', 'effective_ms': 100,
            'group_id': 'factory', 'dependencies': [],
            'selector': {'device_ids': ['device'], 'keys': ['temperature']},
            'nodes': [{'id': 'source', 'kind': 'constant', 'params': {
                'value': LARGE_INTEGER, '_resources': {'nested': ['keep']},
                'draft_version': LARGE_INTEGER}}],
            'connections': [], 'outputs': [], 'policy': {}}


class DemoClient:
    """Model strict write bodies, version checks and both document traversals."""
    def __init__(self):
        self.definitions = [definition(kind) for kind in ('analysis', 'alarm', 'strategy')]
        self.saved = {}
        self.calls = []
        self.requests = []
        self.list_calls = 0
        self.deny_save = False
        self.denied_status = 403
        self.readonly_mutation = False
        self.changed_publication = False
        self.initial_revision = 1

    def tool(self, name, arguments, path='/mcp/draft'):
        self.calls.append((name, copy.deepcopy(arguments), path))
        if name == 'list_definitions':
            index = self.list_calls % 3
            after = self.list_calls >= 3
            self.list_calls += 1
            self.assert_argument(arguments, {'limit': 200, **({
                'after': ('after-' if after else 'before-') + str(index)
            } if index else {})})
            documents = copy.deepcopy(self.definitions)
            if after:
                documents.reverse()
                if self.changed_publication:
                    documents[0]['nodes'][0]['params']['value'] -= 1
            for item in documents:
                item['_resources'] = [{'id': 'different-evidence-' + str(self.list_calls)}]
            if index == 0:
                return page([], True, ('after-' if after else 'before-') + '1', True)
            if index == 1:
                return page(documents[:2], True, ('after-' if after else 'before-') + '2')
            return page(documents[2:])
        if name == 'get_definition':
            document = copy.deepcopy(next(item for item in self.definitions
                                          if item['id'] == arguments['id']))
            document['_resources'] = [{'id': 'read-evidence'}]
            return document
        if name == 'save_draft':
            if self.deny_save:
                raise RuntimeError('permission denied')
            draft = copy.deepcopy(arguments['draft'])
            if '_resources' in draft or 'draft_version' in draft or '_resources' in draft['definition']:
                raise AssertionError('strict draft endpoint received response decorations')
            previous = self.saved.get(draft['id'])
            expected = previous['version'] if previous else 0
            if type(arguments['expected_version']) is not int or arguments['expected_version'] != expected:
                raise AssertionError('draft revision changed or lost integer precision')
            draft['version'] = previous['version'] + 1 if previous else self.initial_revision
            draft['author_id'], draft['updated_ms'] = 'assistant', 123
            self.saved[draft['id']] = copy.deepcopy(draft)
            draft['_resources'] = [{'id': draft['id'], 'version': draft['version']}]
            draft['draft_version'] = draft['version']
            draft['definition']['_resources'] = [{'id': 'definition-evidence'}]
            return draft
        if name == 'validate_draft':
            return {'valid': True, 'errors': [], 'draft_version': self.saved[arguments['id']]['version'],
                    '_resources': [{'id': arguments['id']}]}
        if name == 'diff_draft':
            return {'draft': copy.deepcopy(self.saved[arguments['id']]), 'published': None,
                    'draft_version': self.saved[arguments['id']]['version'], '_resources': []}
        raise AssertionError('unexpected tool: ' + name)

    @staticmethod
    def assert_argument(actual, expected):
        if actual != expected:
            raise AssertionError((actual, expected))

    def rpc(self, path, method, parameters):
        self.calls.append((method, copy.deepcopy(parameters), path))
        if method == 'initialize':
            return {'protocolVersion': '2025-06-18'}
        if method == 'tools/list':
            return {'tools': [{'name': 'list_definitions'}] + (
                [{'name': 'save_draft'}] if self.readonly_mutation else [])}
        raise AssertionError('unexpected RPC: ' + method)

    def request(self, method, path, body):
        self.requests.append((method, path, copy.deepcopy(body)))
        return self.denied_status, {'error': 'permission denied'}


class MCPExampleTests(unittest.TestCase):
    def test_empty_budget_page_continues_with_opaque_cursor_and_same_filters(self):
        cursor = 'opaque+/= cursor \u2603'
        client = PageClient([page([], True, cursor, True), page([{'id': 'allowed'}])])
        arguments = {'kind': 'analysis', 'limit': 1}
        report = example.collect_documents(client, 'list_definitions', arguments)
        self.assertEqual(arguments, {'kind': 'analysis', 'limit': 1})
        self.assertEqual(client.calls[1], ('list_definitions', {
            'kind': 'analysis', 'limit': 1, 'after': cursor}, '/mcp/read'))
        self.assertEqual(report['items'], [{'id': 'allowed'}])
        self.assertEqual(report['pages_read'], 2)
        self.assertEqual(report['budget_exhausted_pages'], 1)
        self.assertFalse(report['has_more'])
        self.assertNotIn('next_after', report)

    def test_missing_or_invalid_continuation_is_incomplete(self):
        for cursor in (None, '', 17, {'cursor': 'not-a-string'}):
            with self.subTest(cursor=cursor):
                client = PageClient([page([], True, cursor)])
                with self.assertRaisesRegex(RuntimeError, 'Incomplete.*next_after'):
                    example.collect_documents(client, 'list_definitions')
                self.assertEqual(len(client.calls), 1)

    def test_repeated_or_cyclic_cursor_is_incomplete(self):
        for cursors in (['a', 'a'], ['a', 'b', 'a']):
            with self.subTest(cursors=cursors):
                client = PageClient([page([], True, cursor) for cursor in cursors])
                with self.assertRaisesRegex(RuntimeError, 'Incomplete.*repeated'):
                    example.collect_documents(client, 'list_definitions')
                self.assertEqual(len(client.calls), len(cursors))

    def test_distinct_cursors_still_have_a_finite_page_limit(self):
        client = PageClient([page([], True, str(index)) for index in range(3)])
        with self.assertRaisesRegex(RuntimeError, 'Incomplete.*exceeded 3 pages'):
            example.collect_documents(client, 'list_definitions', max_pages=3)
        self.assertEqual(len(client.calls), 3)

    def test_malformed_page_never_reports_completion(self):
        for response in ([], {}, {'items': 'old-array', 'has_more': False},
                         {'items': ['wrong-item'], 'has_more': False, 'scope': {}},
                         {'items': [], 'has_more': 'false', 'scope': {}},
                         {'items': [], 'has_more': False, 'scope': 'wrong-scope'}):
            with self.subTest(response=response):
                with self.assertRaisesRegex(RuntimeError, 'Incomplete.*invalid document page'):
                    example.collect_documents(PageClient([response]), 'discover_catalogue')

    def test_cursor_or_permission_error_preserves_incomplete_traversal(self):
        client = PageClient([page([], True, 'opaque'), RuntimeError('permission denied')])
        with self.assertRaisesRegex(RuntimeError, 'Incomplete.*permission denied'):
            example.collect_documents(client, 'list_definitions')
        self.assertEqual(len(client.calls), 2)

    def test_duplicate_definition_ids_are_rejected(self):
        client = PageClient([page([{'id': 'duplicate'}, {'id': 'duplicate'}])])
        with self.assertRaisesRegex(RuntimeError, 'Incomplete.*duplicate definition ID'):
            example.published_definitions(client)

    def test_decorations_are_removed_only_at_protocol_response_levels(self):
        original = {'id': 'draft', 'version': LARGE_INTEGER, 'draft_version': LARGE_INTEGER,
                    '_resources': ['draft-evidence'], 'base_version': 0,
                    'definition': definition('strategy')}
        original['definition']['_resources'] = ['definition-evidence']
        saved = copy.deepcopy(original)
        result = example.draft_input(original)
        self.assertEqual(original, saved)
        self.assertNotIn('_resources', result)
        self.assertNotIn('draft_version', result)
        self.assertNotIn('_resources', result['definition'])
        self.assertEqual(result['definition']['nodes'], original['definition']['nodes'])
        self.assertEqual(result['version'], LARGE_INTEGER)
        self.assertIs(type(result['version']), int)

    def test_invalid_or_inconsistent_revisions_are_rejected(self):
        for version, decorated in ((True, True), (1.0, 1.0), ('1', '1'), (0, 0), (1, 2)):
            with self.subTest(version=version, decorated=decorated):
                with self.assertRaisesRegex(RuntimeError, 'draft revision'):
                    example.draft_input({'version': version, 'draft_version': decorated})

    def test_full_three_kind_demo_ignores_page_order_and_response_decorations(self):
        client = DemoClient()
        report = example.draft_demo(client)
        self.assertTrue(report['passed'])
        self.assertEqual(report['published_before_sha256'], report['published_after_sha256'])
        self.assertEqual([item['kind'] for item in report['drafts']], ['analysis', 'alarm', 'strategy'])
        self.assertEqual([item['version'] for item in report['drafts']], [2, 2, 2])
        self.assertEqual(client.list_calls, 6)
        mutations = [call for call in client.calls if call[0] == 'save_draft']
        self.assertEqual(len(mutations), 6)
        self.assertTrue(all(call[2] == '/mcp/draft' for call in mutations))
        self.assertEqual([call[1]['expected_version'] for call in mutations], [0, 1, 0, 1, 0, 1])
        for draft in client.saved.values():
            self.assertEqual(draft['definition']['status'], 'draft')
            self.assertEqual(draft['definition']['version'], 0)
            self.assertEqual(draft['base_version'], 0)
            self.assertTrue(draft['definition']['name'].endswith('（修改后）'))
            self.assertEqual(draft['definition']['nodes'][0]['params'], {
                'value': LARGE_INTEGER, '_resources': {'nested': ['keep']},
                'draft_version': LARGE_INTEGER})
        self.assertEqual(len(client.requests), 3)
        self.assertTrue(all(item['status'] == 403 for item in report['denied_api_requests']))

    def test_large_draft_revision_is_used_exactly_for_the_second_save(self):
        client = DemoClient()
        client.initial_revision = LARGE_INTEGER
        report = example.draft_demo(client)
        expected = [call[1]['expected_version'] for call in client.calls if call[0] == 'save_draft']
        self.assertEqual(expected, [0, LARGE_INTEGER] * 3)
        self.assertTrue(all(item['version'] == LARGE_INTEGER + 1 for item in report['drafts']))

    def test_missing_authorized_kind_stops_before_creating_drafts(self):
        client = DemoClient()
        client.definitions = client.definitions[:2]
        with self.assertRaisesRegex(RuntimeError, 'authorized published strategy'):
            example.draft_demo(client)
        self.assertFalse(client.saved)

    def test_draft_permission_error_stops_the_demo(self):
        client = DemoClient()
        client.deny_save = True
        with self.assertRaisesRegex(RuntimeError, 'permission denied'):
            example.draft_demo(client)
        self.assertFalse(client.saved)

    def test_forbidden_api_must_return_403(self):
        client = DemoClient()
        client.denied_status = 200
        with self.assertRaisesRegex(RuntimeError, 'expected HTTP 403'):
            example.draft_demo(client)

    def test_read_endpoint_must_not_advertise_mutation(self):
        client = DemoClient()
        client.readonly_mutation = True
        with self.assertRaisesRegex(RuntimeError, 'advertised draft mutation'):
            example.draft_demo(client)

    def test_published_content_changes_are_detected(self):
        client = DemoClient()
        client.changed_publication = True
        with self.assertRaisesRegex(RuntimeError, 'published definitions changed'):
            example.draft_demo(client)

    def test_discover_cli_collects_all_catalogue_pages(self):
        client = PageClient([page([], True, 'opaque-catalogue', True),
                             page([{'id': 'authorized-output'}])])
        client.token = 'fixture-token'
        calls = []
        client.rpc = lambda *args: calls.append(args) or {'protocolVersion': '2025-06-18'}
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / 'discovery.json'
            with patch.object(example, 'Client', return_value=client), \
                    patch.dict(example.os.environ, {'SF_MCP_TOKEN': 'fixture-token'}, clear=True), \
                    patch('sys.argv', ['client.py', 'discover', '--output', str(output)]):
                example.main()
            report = json.loads(output.read_text())
        self.assertEqual(calls[0][0], '/mcp/read')
        self.assertEqual(report['items'], [{'id': 'authorized-output'}])
        self.assertEqual(report['pages_read'], 2)
        self.assertFalse(report['has_more'])

    def test_http_transport_retains_exact_integer_arguments_and_response(self):
        response = unittest.mock.MagicMock()
        response.__enter__.return_value = response
        response.status = 200
        response.read.return_value = json.dumps({'jsonrpc': '2.0', 'id': 1, 'result': {
            'structuredContent': {'result': {'version': LARGE_INTEGER}}, 'content': []}}).encode()
        client = example.Client('http://127.0.0.1:8090', 'fixture-token')
        with patch.object(example.urllib.request, 'urlopen', return_value=response) as open_request:
            result = client.tool('save_draft', {'expected_version': LARGE_INTEGER, 'draft': {}})
        request = open_request.call_args.args[0]
        self.assertIn(str(LARGE_INTEGER).encode(), request.data)
        self.assertEqual(json.loads(request.data)['params']['arguments']['expected_version'], LARGE_INTEGER)
        self.assertEqual(result['version'], LARGE_INTEGER)
        self.assertIs(type(result['version']), int)


if __name__ == '__main__':
    unittest.main()
