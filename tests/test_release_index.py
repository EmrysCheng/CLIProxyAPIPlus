import hashlib
import io
import json
import sys
import tarfile
import tempfile
import unittest
import zipfile
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'scripts'))
import release_index


class ReleaseIndexTests(unittest.TestCase):
    def package(self, root, source='a' * 64, version='1.2.3', target='windows_amd64'):
        suffix = '.zip' if target.startswith('windows') else '.tar.gz'
        path = root / ('CLIProxyAPIPlus_' + version + '_' + target + '_portable' + suffix)
        info = json.dumps({'version': version, 'product_source_sha256': source}).encode()
        if suffix == '.zip':
            with zipfile.ZipFile(path, 'w') as archive:
                archive.writestr('build-info.json', info)
        else:
            with tarfile.open(path, 'w:gz') as archive:
                entry = tarfile.TarInfo('build-info.json'); entry.size = len(info)
                archive.addfile(entry, io.BytesIO(info))
        path.with_name(path.name + '.sha256').write_text(hashlib.sha256(path.read_bytes()).hexdigest() + '  ' + path.name)
        return path

    def test_loopback_requires_explicit_opt_in(self):
        for url in ['http://127.0.0.1:26973', 'http://[::1]:26973']:
            with self.assertRaises(ValueError): release_index.validate_base_url(url)
            self.assertEqual(url, release_index.validate_base_url(url, True))
        for url in ['http://localhost:26973', 'http://192.168.0.1', 'http://0.0.0.0', 'http://[::]', 'http://user:pass@127.0.0.1', 'http://127.0.0.1/#fragment', 'http://127.0.0.1/?query', 'http://127.0.0.1:99999']:
            with self.assertRaises(ValueError): release_index.validate_base_url(url, True)
        self.assertEqual('https://example.test/packages', release_index.validate_base_url('https://example.test/packages'))

    def test_source_identity_comes_from_verified_archives_without_git(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            self.package(root)
            self.package(root, target='linux_arm64')
            index, files = release_index.generate(root, '1.2.3', 'http://127.0.0.1:26973', allow_local_http=True)
            self.assertEqual('a' * 64, index['release']['product_source_sha256'])
            self.assertNotIn('source_commit', index['release'])
            self.assertEqual(2, len(files))
            output = root / 'published/index.json'
            release_index.publish(index, files, output, copy_assets=True)
            self.assertEqual(index, json.loads(output.read_text(encoding='utf-8')))
            for f in files: self.assertEqual(f.read_bytes(), (output.parent / f.name).read_bytes())
            with self.assertRaises(FileExistsError): release_index.publish(index, files, output)
            index['release']['notes'] = 'updated'
            release_index.publish(index, files, output, copy_assets=True, replace=True)
            self.assertEqual('updated', json.loads(output.read_text(encoding='utf-8'))['release']['notes'])

    def test_service_bundle_ignores_full_variant_and_service_archives(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            portable = self.package(root)
            # Full/ReleaseServer archives are not installable Portable assets,
            # even if they share the release directory and current version.
            (root / 'CLIProxyAPIPlus_1.2.3_linux_amd64_full.tar.gz').write_bytes(b'not a portable archive')
            (root / 'CLIProxyAPIPlus_ReleaseServer_1.2.3_windows_amd64_portable.zip').write_bytes(b'not an application archive')
            index, files = release_index.generate(root, '1.2.3', 'http://127.0.0.1:26973', allow_local_http=True, portable_only=True)
            self.assertEqual([portable], files)
            self.assertEqual([portable.name], [x['name'] for x in index['release']['assets']])

    def test_hash_drift_and_mixed_sources_are_rejected(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            f = self.package(root)
            f.write_bytes(f.read_bytes() + b'changed')
            with self.assertRaisesRegex(ValueError, 'checksum'): release_index.generate(root, '1.2.3', 'https://example.test')
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            self.package(root)
            self.package(root, source='b' * 64, target='linux_arm64')
            with self.assertRaisesRegex(ValueError, 'different product sources'): release_index.generate(root, '1.2.3', 'https://example.test')

    def test_invalid_or_missing_source_identity_is_rejected(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            self.package(root, source='development')
            with self.assertRaisesRegex(ValueError, 'identity'): release_index.generate(root, '1.2.3', 'https://example.test')
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            self.package(root)
            with self.assertRaisesRegex(ValueError, 'source-commit'): release_index.generate(root, '1.2.3', 'https://example.test', source_commit='made-up')


if __name__ == '__main__':
    unittest.main()
