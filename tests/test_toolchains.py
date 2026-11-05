import hashlib
import importlib.util
import io
import json
from pathlib import Path
import sys
import tarfile
import tempfile
import unittest
from unittest import mock
import zipfile

SCRIPTS = Path(__file__).parents[1] / 'scripts'
sys.path.insert(0, str(SCRIPTS))
import toolchains


class BundledSDKTests(unittest.TestCase):
    def archive(self, root, entries):
        path = root / 'tools.tar.gz'
        with tarfile.open(path, 'w:gz') as out:
            for name, data, link in entries:
                info = tarfile.TarInfo(name)
                if link:
                    info.type = tarfile.SYMTYPE; info.linkname = link
                    out.addfile(info)
                else:
                    info.size = len(data); info.mode = 0o755
                    out.addfile(info, io.BytesIO(data))
        return path

    def test_links_materialize_without_filesystem_links_and_keep_mode(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            archive = self.archive(root, [('sdk/bin/python3.13', b'ELF fixture', None), ('sdk/bin/python3', b'', 'python3.13')])
            output = root / 'out'
            executable = toolchains.extract(archive, output, 'sdk/')
            self.assertEqual(b'ELF fixture', (output / 'bin/python3').read_bytes())
            self.assertFalse((output / 'bin/python3').is_symlink())
            self.assertEqual({'bin/python3.13', 'bin/python3'}, set(executable))

    def test_unsafe_links_and_paths_and_special_files_rejected(self):
        for name, link in [('sdk/bin/escape', '../../outside'), ('sdk/bin/absolute', '/outside'), ('sdk/../escape', None), ('sdk/bin/cycle', 'cycle')]:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as tmp:
                root=Path(tmp);archive=self.archive(root, [(name,b'bad',link)])
                with self.assertRaises(ValueError):toolchains.extract(archive,root/'out','sdk/')

    def test_publisher_hash_is_checked_even_for_cached_archives(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);(root/('0'*64+'.zip')).write_bytes(b'corrupt')
            with self.assertRaisesRegex(ValueError,'checksum mismatch'):
                toolchains.download({'url':'https://nodejs.org/pinned.zip','sha256':'0'*64},root)

    def test_verify_binds_manifest_platform_contents_and_extra_files(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp); target='windows/amd64'; records={}
            for name in toolchains.paths(target).values():
                path=root/name;path.parent.mkdir(exist_ok=True,parents=True);path.write_bytes(b'fixture')
                records[name]=hashlib.sha256(b'fixture').hexdigest()
            data={'schema_version':1,'target':target,'tools':toolchains.paths(target),'records':records,'executables':[]}
            manifest=root/'toolchain-manifest.json';manifest.write_text(json.dumps(data),encoding='utf-8')
            lock={'platforms':{target:{'manifest_sha256':toolchains.sha(manifest)}}}
            self.assertEqual(data,toolchains.verify(root,target,lock))
            extra=root/'extra.py';extra.write_bytes(b'code')
            with self.assertRaisesRegex(ValueError,'unlisted'):toolchains.verify(root,target,lock)
            extra.unlink();next(iter(records))
            manifest.write_bytes(b'{}')
            with self.assertRaisesRegex(ValueError,'pinned digest'):toolchains.verify(root,target,lock)

    def test_packaging_reuses_verified_sdk_with_default_proxy_without_downloading(self):
        for target in ['windows/amd64', 'linux/amd64', 'linux/arm64']:
            with self.subTest(target=target), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                source = root / 'source'
                source.mkdir()
                records = {}
                for name in toolchains.paths(target).values():
                    path = source / name
                    path.parent.mkdir(parents=True, exist_ok=True)
                    path.write_bytes(b'fixture')
                    records[name] = hashlib.sha256(b'fixture').hexdigest()
                data = {'schema_version': 1, 'target': target, 'tools': toolchains.paths(target),
                        'records': records, 'executables': []}
                manifest = source / 'toolchain-manifest.json'
                manifest.write_text(json.dumps(data), encoding='utf-8')
                lock = {'platforms': {target: {'manifest_sha256': toolchains.sha(manifest)}}}
                destination = root / 'distribution'
                destination.mkdir()
                with mock.patch.object(toolchains, 'read_lock', return_value=lock), mock.patch.object(
                    toolchains, 'prepare', side_effect=AssertionError('SDK download forbidden')
                ):
                    # Both a fresh package and a rebuild reusing its verified SDK get the default.
                    for _ in range(2):
                        toolchains.package(destination, target, source=source)
                        self.assertEqual(
                            {'bundle': './build-tools',
                             'go_proxy': 'https://goproxy.cn|https://proxy.golang.org'},
                            json.loads((destination / 'update-tools.json').read_text(encoding='utf-8')),
                        )
                        self.assertEqual(records, toolchains.inventory(destination / 'build-tools'))

    def test_real_lock_has_complete_platform_pins_and_licenses(self):
        lock=toolchains.read_lock()
        for target,item in lock['platforms'].items():
            self.assertRegex(item['manifest_sha256'],r'^[0-9a-f]{64}$')
            self.assertEqual({'go','node','python','python-licenses'},set(item['archives']))
            for archive in item['archives'].values():
                self.assertRegex(archive['sha256'],r'^[0-9a-f]{64}$')
                self.assertTrue(archive['url'].startswith('https://'))
            self.assertIn('LICENSE.openssl-3.txt',item['archives']['python-licenses']['only'])


if __name__ == '__main__':unittest.main()
