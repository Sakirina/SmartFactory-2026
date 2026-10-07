"""Exercise message deployment generation and release component recognition."""
import importlib.util
import json
from pathlib import Path
import runpy
import shutil
import subprocess
import sys
import tempfile
import types
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[1]


def load_script(name):
    spec = importlib.util.spec_from_file_location(name.replace('-', '_'), ROOT / 'scripts' / (name + '.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class MessageDeploymentTests(unittest.TestCase):
    def setUp(self):
        self.images = json.loads((ROOT / 'deploy/images.lock.json').read_text())['images']
        self.native = json.loads((ROOT / 'deploy/compose.native.json').read_text())

    def copy_lock(self, root):
        (root / 'deploy').mkdir(parents=True)
        shutil.copy(ROOT / 'deploy/images.lock.json', root / 'deploy/images.lock.json')

    def test_native_generator_matches_entire_committed_compose(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.copy_lock(root)
            (root / 'scripts').mkdir()
            script = root / 'scripts/prepare-deploy.py'
            shutil.copy(ROOT / 'scripts/prepare-deploy.py', script)
            subprocess.run([sys.executable, str(script)], check=True, capture_output=True)
            generated = json.loads((root / 'deploy/compose.native.json').read_text())
            self.assertEqual(generated, self.native)
            self.assertEqual(generated['services']['kafka']['image'], self.images['kafka']['pinned'])
            self.assertIn('kafka-broker-api-versions.sh', generated['services']['kafka']['healthcheck']['test'][1])
            self.assertEqual(generated['services']['cloud-db']['volumes'], ['cloud-db-pg18:/var/lib/postgresql'])

    def test_site_generator_matches_committed_compose_and_keeps_file_budget(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.copy_lock(root)
            argv = ['prepare-site.py', '--root', str(root), '--binary-dir', str(root / 'bin')]
            # Certificate generation is excluded; the generator creates real
            # credentials, server configurations, ports, resources and Compose.
            with patch.object(sys, 'argv', argv), patch('subprocess.run'):
                runpy.run_path(str(ROOT / 'scripts/prepare-site.py'), run_name='__main__')
            generated = json.loads((root / 'deploy/compose.site.json').read_text())
            self.assertEqual(generated, json.loads((ROOT / 'deploy/compose.site.json').read_text()))
            for name, service in generated['services'].items():
                self.assertEqual(service['image'], self.images['nats']['pinned'])
                self.assertEqual(service['mem_limit'], '256m')
                self.assertEqual(service['memswap_limit'], '256m')
                self.assertEqual(service['environment']['GOMEMLIMIT'], '192MiB')
                self.assertTrue(all(not value.startswith('/') for value in service['volumes']))
                config = (root / '.local/site' / (name + '.conf')).read_text()
                self.assertIn('max_file_store: 2147483648', config)
                self.assertIn('verify: true', config)

    def test_runtime_profiles_use_target_protocol_probe_and_persistent_nats(self):
        module = load_script('prepare-runtime')
        for profile in ['capacity', 'site']:
            with self.subTest(profile=profile), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                bundle = root / 'bundle'
                self.copy_lock(bundle)
                (bundle / 'bin').mkdir()
                (bundle / 'bin/sf-edge').touch()
                argv = ['prepare-runtime.py', '--bundle', str(bundle), '--directory', str(root / 'state'), '--profile', profile, '--hosting', 'hosted']
                with patch.object(sys, 'argv', argv), patch.object(module.subprocess, 'run'), patch.object(module.subprocess, 'check_output', return_value=b'{"audit_public_key":"fixture"}'):
                    module.main()
                config = json.loads((root / 'state/compose.json').read_text())
                kafka = config['services']['kafka']
                self.assertEqual(kafka['image'], self.images['kafka']['pinned'])
                self.assertEqual(kafka['healthcheck'], self.native['services']['kafka']['healthcheck'])
                self.assertFalse(any(value['image'] == self.images['nats_upgrade_intermediate']['pinned'] for value in config['services'].values()))
                nats = {name: value for name, value in config['services'].items() if name.startswith('nats-')}
                self.assertEqual(len(nats), 3 if profile == 'site' else 0)
                for name, service in nats.items():
                    self.assertEqual(service['image'], self.images['nats']['pinned'])
                    self.assertIn(name + ':/data', service['volumes'])
                    self.assertEqual(service['memswap_limit'], '256m')
                    self.assertEqual(service['environment']['GOMEMLIMIT'], '192MiB')
                    self.assertIn('max_file_store: 2147483648', (root / 'state' / (name + '.conf')).read_text())

    def test_release_exports_intermediate_source_license_and_verified_bytes(self):
        builder = load_script('build-release')
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve() / 'source'
            output = Path(temporary) / 'bundle'
            self.copy_lock(root)
            shutil.copy(ROOT / 'deploy/dependencies.json', root / 'deploy/dependencies.json')
            shutil.copytree(ROOT / 'deploy/licenses', root / 'deploy/licenses')
            frontend = root / 'Frontends'
            (frontend / 'dist').mkdir(parents=True)
            (frontend / 'dist/index.html').write_text('fixture\n')
            (frontend / 'package-lock.json').write_text('{"packages":{}}\n')
            (root / 'docs').mkdir()
            (root / 'docs/private.md').write_text('private\n')
            (root / 'Wiki').mkdir()
            (root / 'Wiki/migration.md').write_text('Public migration instructions\n')
            (root / 'README.md').write_text('[Migration](Wiki/migration.md)\n')
            for module in ['Platform', 'DataTransfer']:
                (root / module).mkdir()
            toolchain = types.SimpleNamespace(environment=lambda _: {}, preflight=lambda *_: {'fixture': True})

            def check_output(argv, **kwargs):
                return 'go version fixture\n' if argv[:2] == ['go', 'version'] else ''

            # Stub compilation and package enumeration, while executing the
            # existing source selection, container inventory, licenses and manifest.
            def run(argv, **kwargs):
                if argv[0] == 'git':
                    return subprocess.CompletedProcess(argv, 1, '', '')
                return subprocess.CompletedProcess(argv, 0, '', '')

            with patch.object(builder, 'ROOT', root), patch.dict(sys.modules, {'toolchain': toolchain}), patch.object(sys, 'argv', ['build-release.py', '--output', str(output), '--skip-frontend-build']), patch.object(builder.subprocess, 'run', side_effect=run), patch.object(builder.subprocess, 'check_output', side_effect=check_output):
                builder.main()
            inventory = json.loads((output / 'components.json').read_text())['components']
            containers = {value['id']: value for value in inventory if value['ecosystem'] == 'container'}
            for component in ['kafka', 'nats', 'nats_upgrade_intermediate']:
                self.assertEqual(containers[component]['image'], self.images[component]['pinned'])
                self.assertEqual(containers[component]['source'], json.loads((ROOT / 'deploy/dependencies.json').read_text())['sources'][component])
                self.assertTrue(containers[component]['license_files'])
                self.assertTrue(all((output / value).is_file() for value in containers[component]['license_files']))
            self.assertFalse((output / 'source/docs/private.md').exists())
            self.assertTrue((output / 'Wiki/migration.md').is_file())
            command = [sys.executable, str(ROOT / 'scripts/verify-release.py'), str(output)]
            result = subprocess.run(command, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            lock = output / 'deploy/images.lock.json'
            lock.write_text(lock.read_text() + ' ')
            result = subprocess.run(command, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('Release mismatch: deploy/images.lock.json', result.stdout + result.stderr)


if __name__ == '__main__':
    unittest.main()
