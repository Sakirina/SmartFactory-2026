import importlib.util
import json
from pathlib import Path
import platform
import shutil
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location('toolchain', ROOT / 'scripts/toolchain.py')
toolchain = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(toolchain)


class ToolchainConsistencyTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        files = ['deploy/toolchains.json', 'deploy/dependencies.json', 'deploy/images.lock.json', 'deploy/go-version', 'deploy/node-version',
                 'deploy/compose.native.json', 'deploy/compose.site.json', 'Platform/go.mod', 'DataTransfer/go.mod',
                 'Frontends/package.json', 'Frontends/package-lock.json', '.github/workflows/build-release.yml']
        for name in files:
            target = self.root / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(ROOT / name, target)

    def change_json(self, name, change):
        path = self.root / name
        content = json.loads(path.read_text())
        change(content)
        path.write_text(json.dumps(content))

    def test_selected_configuration_is_consistent(self):
        self.assertEqual(toolchain.configuration_errors(self.root), [])

    def test_wrong_go_module_and_ci_version_are_rejected(self):
        path = self.root / 'DataTransfer/go.mod'
        path.write_text(path.read_text().replace('toolchain go1.27.1', 'toolchain go1.26.8'))
        path = self.root / '.github/workflows/build-release.yml'
        path.write_text(path.read_text().replace('node-version-file: deploy/node-version', "node-version: '22'"))
        errors = toolchain.configuration_errors(self.root)
        self.assertTrue(any('DataTransfer/go.mod' in item for item in errors))
        self.assertTrue(any('CI node' in item for item in errors))

    def test_unknown_compose_image_and_conflicting_sizes_are_rejected(self):
        self.change_json('deploy/compose.native.json', lambda value: value['services']['kafka'].update(image='apache/kafka:0.0.0-invalid'))
        self.change_json('deploy/images.lock.json', lambda value: value['images']['kafka'].update(compressed_bytes=1))
        errors = toolchain.configuration_errors(self.root)
        self.assertTrue(any('absent from current lock' in item for item in errors))
        self.assertTrue(any('layer sizes' in item for item in errors))

    def test_frontend_lock_drift_is_rejected(self):
        self.change_json('Frontends/package-lock.json', lambda value: value['packages']['node_modules/react'].update(version='19.0.0'))
        self.assertTrue(any('react' in item for item in toolchain.configuration_errors(self.root)))

    def test_frontend_alias_target_and_version_drift_are_rejected(self):
        self.change_json('Frontends/package-lock.json', lambda value: value['packages']['node_modules/@typescript/native'].update(name='wrong-typescript', version='7.0.1'))
        errors = toolchain.configuration_errors(self.root)
        self.assertTrue(any('alias target' in item and '@typescript/native' in item for item in errors))
        self.assertTrue(any('lock differs' in item and '@typescript/native' in item for item in errors))

    def test_frontend_alias_declaration_drift_is_rejected(self):
        self.change_json('Frontends/package-lock.json', lambda value: value['packages']['']['devDependencies'].update(typescript='npm:@typescript/typescript6@6.0.3'))
        self.assertTrue(any('lock declarations' in item for item in toolchain.configuration_errors(self.root)))

    def test_workspace_runtime_and_no_implicit_go_download(self):
        system = {'Darwin': 'darwin', 'Linux': 'linux', 'Windows': 'win'}[platform.system()]
        machine = {'arm64': 'arm64', 'aarch64': 'arm64', 'x86_64': 'x64', 'AMD64': 'x64'}[platform.machine()]
        binary = self.root / '.local/toolchains' / f'node-v24.21.0-{system}-{machine}'
        if system != 'win':
            binary = binary / 'bin'
        binary.mkdir(parents=True)
        result = toolchain.environment(self.root, {'PATH': '/usr/bin', 'GOTOOLCHAIN': 'auto'})
        self.assertTrue(result['PATH'].startswith(str(binary)))
        self.assertEqual(result['GOTOOLCHAIN'], 'local')


if __name__ == '__main__':
    unittest.main()
