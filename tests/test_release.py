"""Check the contents of public release bundles using temporary repositories."""
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location('build_release', Path(__file__).resolve().parents[1] / 'scripts/build-release.py')
builder = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(builder)


class ReleaseContentsTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve() / 'source'
        self.root.mkdir()
        self.output = Path(self.temporary.name) / 'bundle'
        self.public = {
            'README.md', '.gitignore', '.gitattributes', '.github/workflows/build-release.yml',
            'DataTransfer/go.mod', 'DataTransfer/cmd/datatransfer/main.go',
            'Platform/go.mod', 'Frontends/package-lock.json', 'Frontends/src/main.tsx',
            'contracts/1.0/Definition.schema.json', 'deploy/licenses/NOTICE',
            'examples/factory/definitions.json', 'scripts/prepare-runtime.py',
            'tests/fixtures/requirements-contracts.txt', 'deploy/.env.example',
        }
        self.private = {
            'Baseline/requirements.md', 'DataTransfer/docs/design.md',
            'docs/internal.md', 'deliverables/demo.pptx', '跨服务协作备忘.md',
            'scripts/create-product-document.py', 'scripts/create-product-presentation.mjs',
            'scripts/record-demo.mjs', 'scripts/__pycache__/build.pyc',
            'DataTransfer/.cache/cache.json', 'Platform/.local/state.json',
            'Platform/state.db', 'Platform/state.db-wal', 'Platform/client.key',
            'Frontends/node_modules/private.json', 'Frontends/dist/old.js',
            'Frontends/.env.production', 'deploy/deployment-credentials.json',
            'tests/AGENTS.md', 'tests/.agents/instructions.md',
        }
        for name in self.public | self.private:
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(name + '\n')
        self.patch = patch.object(builder, 'ROOT', self.root)
        self.patch.start()
        self.addCleanup(self.patch.stop)

    def assert_public_bundle(self):
        source_files = {str(path.relative_to(self.output / 'source')) for path in (self.output / 'source').rglob('*') if path.is_file()}
        self.assertEqual(source_files, self.public)
        self.assertTrue((self.output / 'scripts/prepare-runtime.py').is_file())
        self.assertTrue((self.output / 'deploy/licenses/NOTICE').is_file())
        self.assertTrue((self.output / 'tests/fixtures/requirements-contracts.txt').is_file())
        for name in self.private:
            self.assertFalse((self.output / name).exists(), name)
            self.assertFalse((self.output / 'source' / name).exists(), name)

    def test_tracked_internal_files_stay_out_of_both_bundle_copies(self):
        subprocess.run(['git', 'init', '-q', str(self.root)], check=True)
        subprocess.run(['git', 'add', '-f', '.'], cwd=self.root, check=True)
        subprocess.run(['git', '-c', 'user.name=Release test', '-c', 'user.email=release-test@example.invalid', 'commit', '-qm', 'fixture'], cwd=self.root, check=True)
        expected = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=self.root, text=True).strip()
        self.assertEqual(builder.copy_release_sources(self.output), expected)
        self.assert_public_bundle()

    def test_unpacked_source_keeps_the_same_public_contents(self):
        (self.root.parent / 'release-manifest.json').write_text(json.dumps({'source_commit': 'fixture-commit'}))
        self.assertEqual(builder.copy_release_sources(self.output), 'fixture-commit')
        self.assert_public_bundle()

    def test_symlinks_and_nested_output_are_not_included(self):
        (self.root / 'scripts/private-link.py').symlink_to(self.root / 'docs/internal.md')
        output = self.root / 'scripts/generated-release'
        output.mkdir()
        (output / 'old.py').write_text('old output\n')
        selected, _ = builder.source_snapshot(output)
        self.assertEqual({str(relative) for _, relative in selected}, self.public)


if __name__ == '__main__':
    unittest.main()
