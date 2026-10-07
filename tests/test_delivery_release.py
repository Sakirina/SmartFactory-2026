"""Verify publisher identities and independent license locations in releases."""
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location('delivery_builder', ROOT / 'scripts/build-release.py')
builder = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(builder)


class DeliveryReleaseTests(unittest.TestCase):
    def test_go_distribution_license_matches_actual_runtime_version(self):
        with tempfile.TemporaryDirectory() as temporary:
            formula=Path(temporary)/'go/1.27.1'
            goroot=formula/'libexec'
            goroot.mkdir(parents=True)
            (formula/'LICENSE').write_text('Go Authors license\n')
            receipt=formula/'INSTALL_RECEIPT.json'
            receipt.write_text(json.dumps({'source':{'versions':{'stable':'1.27.1'}}}))
            license_path,distribution=builder.go_runtime_license(goroot,'1.27.1')
            self.assertEqual(license_path,formula/'LICENSE')
            self.assertEqual(distribution['distribution_version'],'1.27.1')
            with self.assertRaisesRegex(RuntimeError,'actual compiler version'):
                builder.go_runtime_license(goroot,'1.26.0')
            (goroot/'LICENSE').write_text('Direct Go distribution license\n')
            self.assertEqual(builder.go_runtime_license(goroot,'1.27.1')[0],goroot/'LICENSE')

    def test_shared_go_and_embed_input_keeps_both_sources(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary)
            directory=root/'Platform/internal/store'
            directory.mkdir(parents=True)
            (directory/'migration.go').write_text('package store\n')
            package={'Dir':str(directory),'ImportPath':'example/store','GoFiles':['migration.go'],'EmbedFiles':['migration.go']}
            with patch.object(builder.subprocess,'check_output',return_value=json.dumps(package)):
                inputs,observed=builder.build_inputs(root,{'Platform':['sf-edge']},{})
            self.assertEqual(inputs['source/Platform/internal/store/migration.go']['input_kinds'],['EmbedFiles','GoFiles'])
            self.assertEqual(len(observed),1)

    def test_alias_publisher_and_same_version_licenses_remain_distinct(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / 'source'
            output = Path(temporary) / 'bundle'
            packages = {
                'node_modules/@typescript/native': {'name': 'typescript', 'version': '7.0.2', 'resolved': 'https://registry.npmjs.org/typescript/-/typescript-7.0.2.tgz', 'integrity': 'sha512-seven'},
                'node_modules/typescript': {'name': '@typescript/typescript6', 'version': '6.0.2', 'resolved': 'https://registry.npmjs.org/@typescript/typescript6/-/typescript6-6.0.2.tgz', 'integrity': 'sha512-six'},
                'node_modules/typescript/node_modules/@typescript/old': {'name': 'typescript', 'version': '6.0.2', 'resolved': 'https://registry.npmjs.org/typescript/-/typescript-6.0.2.tgz', 'integrity': 'sha512-old'},
                'node_modules/another/node_modules/typescript': {'name': 'typescript', 'version': '6.0.2', 'resolved': 'https://registry.npmjs.org/typescript/-/typescript-6.0.2.tgz', 'integrity': 'sha512-old'},
            }
            for location, metadata in packages.items():
                directory = root / 'Frontends' / location
                directory.mkdir(parents=True)
                (directory / 'package.json').write_text(json.dumps({'name': metadata['name'], 'version': metadata['version']}))
                (directory / 'LICENSE').write_text(location + '\n')
            (root / 'Frontends/package-lock.json').write_text(json.dumps({'packages': packages}))
            entries = builder.npm_components(root, output)
            self.assertEqual(len({entry['id'] for entry in entries}), 4)
            for entry, (location, metadata) in zip(entries, packages.items()):
                self.assertEqual(entry['module'], metadata['name'])
                self.assertEqual(entry['version'], metadata['version'])
                self.assertEqual(entry['installation_path'], location)
                self.assertEqual(entry['resolved'], metadata['resolved'])
                self.assertEqual(entry['integrity'], metadata['integrity'])
                self.assertEqual((output / entry['license_files'][0]).read_text(), location + '\n')

    def test_verifier_rejects_unlisted_private_file(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / 'release-manifest.json').write_text(json.dumps({'format': 'smartfactory-release-v1', 'target': 'linux/amd64', 'files': {}}))
            (root / 'deployment-credentials.json').write_text('{}\n')
            result = subprocess.run([sys.executable, str(ROOT / 'scripts/verify-release.py'), str(root)], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('Unlisted release files: deployment-credentials.json', result.stderr)


if __name__ == '__main__':
    unittest.main()
