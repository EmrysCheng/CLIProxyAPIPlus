from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).parents[1] / 'scripts'))
import data as maintenance


class NativeMaintenancePathTests(unittest.TestCase):
    def test_native_build_tree_defaults_match_supported_architectures(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for system, machine, relative in [
                ('posix', 'aarch64', 'dist/linux-arm64-portable/cliproxyapi-plus'),
                ('posix', 'arm64', 'dist/linux-arm64-portable/cliproxyapi-plus'),
                ('posix', 'ARM64', 'dist/linux-arm64-portable/cliproxyapi-plus'),
                ('posix', 'x86_64', 'dist/linux-amd64-portable/cliproxyapi-plus'),
                ('posix', 'AMD64', 'dist/linux-amd64-portable/cliproxyapi-plus'),
                ('nt', 'AMD64', 'dist/windows-amd64-portable/cliproxyapi-plus.exe'),
            ]:
                with self.subTest(system=system, machine=machine):
                    self.assertEqual(root / relative, maintenance.default_binary(root, system=system, machine=machine))

    def test_installed_root_binary_still_wins_over_build_tree(self):
        for system, executable in [('posix', 'cliproxyapi-plus'), ('nt', 'cliproxyapi-plus.exe')]:
            with self.subTest(system=system), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                (root / executable).write_bytes(b'fixture-not-executed')
                self.assertEqual(root / executable, maintenance.default_binary(root, system=system, machine='aarch64'))

    def test_module_default_uses_same_native_selector(self):
        self.assertEqual(maintenance.default_binary(), maintenance.DEFAULT_BINARY)


if __name__ == '__main__':
    unittest.main()
