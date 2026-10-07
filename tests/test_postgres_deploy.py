"""Validate PostgreSQL major-version volumes in generated deployment profiles."""
import importlib.util
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[1]


class PostgresDeploymentTests(unittest.TestCase):
    def assert_postgres(self, config, count):
        lock = json.loads((ROOT / 'deploy/images.lock.json').read_text())['images']['postgres']
        self.assertEqual(lock['reference'], 'postgres:18.6')
        databases = [value for name, value in config['services'].items() if name.endswith('-db')]
        self.assertEqual(len(databases), count)
        for database in databases:
            self.assertEqual(database['image'], lock['pinned'])
            storage = [value for value in database['volumes'] if value.endswith(':/var/lib/postgresql')]
            self.assertEqual(len(storage), 1)
            volume = storage[0].split(':')[0]
            self.assertTrue(volume.endswith('-pg18'))
            self.assertIn(volume, config['volumes'])
        for value in config['services'].values():
            for mount in value.get('volumes', []):
                if mount.startswith('/'):
                    continue
                self.assertIn(mount.split(':')[0], config['volumes'])

    def test_native_generator_matches_major_version_layout(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / 'scripts').mkdir()
            (root / 'deploy').mkdir()
            shutil.copy(ROOT / 'scripts/prepare-deploy.py', root / 'scripts/prepare-deploy.py')
            shutil.copy(ROOT / 'deploy/images.lock.json', root / 'deploy/images.lock.json')
            subprocess.run([sys.executable, str(root / 'scripts/prepare-deploy.py')], check=True, capture_output=True)
            generated = json.loads((root / 'deploy/compose.native.json').read_text())
            self.assert_postgres(generated, 2)
            committed = json.loads((ROOT / 'deploy/compose.native.json').read_text())

            for name in ['cloud-db', 'edge-db']:
                self.assertEqual(generated['services'][name], committed['services'][name])
                self.assertEqual(generated['volumes'][name + '-pg18'], committed['volumes'][name + '-pg18'])

    def test_runtime_capacity_and_site_keep_all_declared_volumes(self):
        spec = importlib.util.spec_from_file_location('prepare_runtime', ROOT / 'scripts/prepare-runtime.py')
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        for profile, count in [('capacity', 2), ('site', 4)]:
            with self.subTest(profile=profile), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                bundle = root / 'bundle'
                (bundle / 'bin').mkdir(parents=True)
                (bundle / 'deploy').mkdir()
                (bundle / 'bin/sf-edge').touch()
                shutil.copy(ROOT / 'deploy/images.lock.json', bundle / 'deploy/images.lock.json')
                argv = ['prepare-runtime.py', '--bundle', str(bundle), '--directory', str(root / 'state'), '--profile', profile, '--hosting', 'hosted']
                # Certificate contents are outside this storage test; all compose
                # generation, volume declarations and profile branches execute.
                with patch.object(sys, 'argv', argv), patch.object(module.subprocess, 'run'), patch.object(module.subprocess, 'check_output', return_value=b'{"audit_public_key":"fixture"}'):
                    module.main()
                config = json.loads((root / 'state/compose.json').read_text())
                self.assert_postgres(config, count)
                if profile == 'site':
                    self.assertTrue(all(name in config['volumes'] for name in ['nats-1', 'nats-2', 'nats-3']))


if __name__ == '__main__':
    unittest.main()
