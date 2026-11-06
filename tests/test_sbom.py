"""SBOM notices must not depend on whether a runner's Go cache is warm."""
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).parents[1] / 'scripts'))
import sbom


class SBOMNoticeTests(unittest.TestCase):
    def test_cold_and_warm_module_caches_include_the_same_license(self):
        for warm in (False, True):
            with self.subTest(warm=warm), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                build = root / '.build'
                front = build / 'frontend'
                front.mkdir(parents=True)
                (front / 'package-lock.json').write_text('{"packages": {}}')
                (build / 'frontend-current.json').write_text(json.dumps({'work': str(front)}))
                module = root / 'module-cache' / 'go-strftime@v1.0.0'
                license_bytes = b'Copyright fixture\nMIT license\n'
                pins = root / 'internal/sourceupdate/portable-notices.lock.json'
                pins.parent.mkdir(parents=True)
                key = hashlib.sha256(b'go:github.com/ncruces/go-strftime@v1.0.0').hexdigest()[:24]
                pins.write_text(json.dumps({'platforms': {'linux/arm64': {
                    key: {'required': {'LICENSE': hashlib.sha256(license_bytes).hexdigest()}, 'optional': {}}
                }}}))
                if warm:
                    module.mkdir(parents=True)
                    (module / 'LICENSE').write_bytes(license_bytes)

                def download(command, *, cwd, check):
                    self.assertEqual(command, ['pinned-go', 'mod', 'download', 'github.com/ncruces/go-strftime@v1.0.0'])
                    self.assertEqual(cwd, root)
                    self.assertTrue(check)
                    module.mkdir(parents=True, exist_ok=True)
                    (module / 'LICENSE').write_bytes(license_bytes)

                def list_modules(command, *, cwd, text):
                    self.assertEqual(command, ['pinned-go', 'list', '-m', '-json', 'all'])
                    entry = {'Path': 'github.com/ncruces/go-strftime', 'Version': 'v1.0.0'}
                    # Go omits Dir for a module whose source is not downloaded.
                    if module.is_dir():
                        entry['Dir'] = str(module)
                    return json.dumps(entry)

                output = root / 'package'
                with mock.patch.object(sbom.project, 'ROOT', root), \
                        mock.patch.object(sbom.project, 'BUILD', build), \
                        mock.patch.object(sys, 'argv', ['sbom.py', '--go', 'pinned-go', '--output', str(output)]), \
                        mock.patch.object(sbom.subprocess, 'run', side_effect=download), \
                        mock.patch.object(sbom.subprocess, 'check_output', side_effect=list_modules):
                    sbom.main()
                index = json.loads((output / 'licenses-index.json').read_text())
                notices = index[0]['notices']
                self.assertEqual(len(notices), 1)
                self.assertEqual(notices[0]['sha256'], hashlib.sha256(license_bytes).hexdigest())
                self.assertEqual((output / notices[0]['path']).read_bytes(), license_bytes)
                report = json.loads((output / 'compliance-report.json').read_text())
                self.assertEqual(report['missing_notice_files'], [])

    def test_download_failure_does_not_emit_an_incomplete_inventory(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / 'package'
            failure = subprocess.CalledProcessError(1, ['pinned-go', 'mod', 'download'])
            with mock.patch.object(sys, 'argv', ['sbom.py', '--go', 'pinned-go', '--output', str(output)]), \
                    mock.patch.object(sbom.subprocess, 'run', side_effect=failure), \
                    mock.patch.object(sbom.subprocess, 'check_output', return_value=json.dumps({
                        'Path': 'github.com/ncruces/go-strftime', 'Version': 'v1.0.0'
                    })) as listing:
                with self.assertRaises(subprocess.CalledProcessError):
                    sbom.main()
            listing.assert_called_once()
            self.assertFalse((output / 'licenses-index.json').exists())
            self.assertFalse((output / 'sbom.spdx.json').exists())

    def test_unreviewed_modules_are_not_downloaded_into_the_notice_inventory(self):
        entry = {'Path': 'example.com/test-only', 'Version': 'v1.0.0'}
        with mock.patch.object(sbom.subprocess, 'check_output', return_value=json.dumps(entry)), \
                mock.patch.object(sbom.subprocess, 'run') as download:
            self.assertEqual(sbom.notice_modules('pinned-go'), [entry])
        download.assert_not_called()


if __name__ == '__main__':
    unittest.main()
