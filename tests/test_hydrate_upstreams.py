import hashlib
import io
import json
from pathlib import Path
import re
import stat
import sys
import tempfile
import unittest
from unittest import mock
import zipfile

sys.path.insert(0, str(Path(__file__).parents[1] / 'scripts'))
import hydrate_upstreams as hydration
import project
import upstreams
from workspace_lock import file_lock


class LockedHydrationTests(unittest.TestCase):
    def fixture(self, base):
        root = base / 'checkout'
        root.mkdir()
        reference = base / 'reference'
        reference.mkdir()
        self.archives = {}
        projects = {}
        for index, (name, repo) in enumerate(hydration.REPOSITORIES.items()):
            source = reference / name
            source.mkdir()
            (source / 'LICENSE').write_text('MIT fixture\n', encoding='utf-8', newline='\n')
            relative = 'go.mod' if name == 'cliproxyapi' else 'apps/manager-server/go.mod'
            module = ('github.com/router-for-me/CLIProxyAPI/v8' if name == 'cliproxyapi'
                      else 'github.com/seakee/cpa-manager-plus/apps/manager-server')
            path = source / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text('module ' + module + '\n', encoding='utf-8', newline='\n')
            packed = io.BytesIO()
            with zipfile.ZipFile(packed, 'w') as archive:
                for path in source.rglob('*'):
                    if path.is_file():
                        archive.writestr('publisher-root/' + path.relative_to(source).as_posix(), path.read_bytes())
            self.archives[repo] = packed.getvalue()
            projects[name] = {'repository': repo, 'commit': str(index + 1) * 40,
                              'ref': 'moving-tag-not-used', 'tree_sha256': project.tree_digest(source)}
        lock = {'schema_version': 1, 'tree_hash_format': hydration.TREE_FORMAT, 'projects': projects}
        (root / 'upstream.lock.json').write_text(json.dumps(lock), encoding='utf-8')
        return root, lock

    def fetch(self, request, timeout):
        self.assertEqual(90, timeout)
        self.assertIsNone(request.get_header('Authorization'))
        for repo, raw in self.archives.items():
            if request.full_url.startswith('https://codeload.github.com/' + repo + '/zip/'):
                self.assertRegex(request.full_url.rsplit('/', 1)[1], r'^[0-9a-f]{40}$')
                return io.BytesIO(raw)
        self.fail('Unapproved download destination')

    def test_fixed_commits_verified_then_published_and_reused_without_network(self):
        with tempfile.TemporaryDirectory() as tmp:
            root, lock = self.fixture(Path(tmp))
            original = (root / 'upstream.lock.json').read_bytes()
            with mock.patch.object(upstreams, 'github', side_effect=AssertionError('Moving ref lookup forbidden')), mock.patch.object(upstreams, 'open_github', side_effect=self.fetch) as fetch:
                result = hydration.hydrate(root)
                self.assertEqual(list(hydration.REPOSITORIES), result['downloaded'])
                self.assertEqual(2, fetch.call_count)
            for name, item in lock['projects'].items():
                self.assertEqual(item['tree_sha256'], project.tree_digest(root / 'upstream' / name))
            with mock.patch.object(upstreams, 'open_github', side_effect=AssertionError('No download for verified trees')):
                self.assertEqual([], hydration.hydrate(root, offline=True)['downloaded'])
                self.assertEqual([], hydration.hydrate(root)['downloaded'])
            self.assertEqual(original, (root / 'upstream.lock.json').read_bytes())

    def test_offline_missing_and_existing_corruption_fail_without_repair(self):
        with tempfile.TemporaryDirectory() as tmp:
            root, _ = self.fixture(Path(tmp))
            with mock.patch.object(upstreams, 'open_github', side_effect=AssertionError('No network')):
                with self.assertRaisesRegex(ValueError, 'Offline checkout'):
                    hydration.hydrate(root, offline=True)
            with mock.patch.object(upstreams, 'open_github', side_effect=self.fetch):
                hydration.hydrate(root)
            marker = root / 'upstream/cliproxyapi/LICENSE'
            marker.write_text('corrupt')
            with mock.patch.object(upstreams, 'open_github', side_effect=AssertionError('No repair download')):
                with self.assertRaisesRegex(ValueError, 'refusing to replace'):
                    hydration.hydrate(root)
            self.assertEqual('corrupt', marker.read_text())

    def test_invalid_repository_commit_digest_or_format_rejected_before_download(self):
        for field, value in [('repository', 'attacker/repository'), ('commit', 'main'), ('commit', 'a' * 39),
                             ('tree_sha256', 'bad')]:
            with self.subTest(field=field, value=value), tempfile.TemporaryDirectory() as tmp:
                root, lock = self.fixture(Path(tmp))
                lock['projects']['cliproxyapi'][field] = value
                (root / 'upstream.lock.json').write_text(json.dumps(lock))
                with mock.patch.object(upstreams, 'open_github', side_effect=AssertionError('Untrusted network')):
                    with self.assertRaisesRegex(ValueError, 'Unapproved'):
                        hydration.hydrate(root)
                self.assertFalse((root / 'upstream').exists())
        with tempfile.TemporaryDirectory() as tmp:
            root, lock = self.fixture(Path(tmp))
            lock['tree_hash_format'] = 'unknown'
            (root / 'upstream.lock.json').write_text(json.dumps(lock))
            with self.assertRaisesRegex(ValueError, 'Unsupported'):
                hydration.hydrate(root)

    def test_second_archive_failure_does_not_publish_first(self):
        with tempfile.TemporaryDirectory() as tmp:
            root, _ = self.fixture(Path(tmp))
            self.archives['seakee/CPA-Manager-Plus'] = b'not a zip'
            with mock.patch.object(upstreams, 'open_github', side_effect=self.fetch):
                with self.assertRaises(zipfile.BadZipFile):
                    hydration.hydrate(root)
            self.assertEqual([], list((root / 'upstream').iterdir()))
            self.assertFalse(any((root / '.build').glob('hydrate-*')))

    def test_changed_tree_unsafe_archive_and_size_limit_never_publish(self):
        for case in ['checksum', 'traversal', 'symlink', 'duplicate', 'size']:
            with self.subTest(case=case), tempfile.TemporaryDirectory() as tmp:
                root, lock = self.fixture(Path(tmp))
                if case == 'checksum':
                    lock['projects']['cliproxyapi']['tree_sha256'] = '0' * 64
                    (root / 'upstream.lock.json').write_text(json.dumps(lock))
                elif case != 'size':
                    raw = io.BytesIO()
                    with zipfile.ZipFile(raw, 'w') as archive:
                        if case == 'traversal':
                            archive.writestr('root/../escape', 'bad')
                        elif case == 'symlink':
                            info = zipfile.ZipInfo('root/link'); info.create_system = 3
                            info.external_attr = (stat.S_IFLNK | 0o777) << 16
                            archive.writestr(info, '../outside')
                        else:
                            archive.writestr('root/a', 'first')
                            with self.assertWarns(UserWarning):
                                archive.writestr('root/a', 'second')
                    self.archives['router-for-me/CLIProxyAPI'] = raw.getvalue()
                with mock.patch.object(upstreams, 'open_github', side_effect=self.fetch), mock.patch.object(
                    hydration, 'MAX_COMPRESSED', 8 if case == 'size' else hydration.MAX_COMPRESSED
                ):
                    with self.assertRaises((ValueError, FileExistsError)):
                        hydration.hydrate(root)
                self.assertEqual([], list((root / 'upstream').iterdir()))
                self.assertFalse((root / 'escape').exists())

    def test_missing_license_or_wrong_module_rejected_even_with_matching_digest(self):
        for case in ['license', 'module']:
            with self.subTest(case=case), tempfile.TemporaryDirectory() as tmp:
                root, lock = self.fixture(Path(tmp))
                reference = Path(tmp) / 'reference/cliproxyapi'
                if case == 'license':
                    (reference / 'LICENSE').unlink()
                else:
                    (reference / 'go.mod').write_text('module attacker/module\n')
                lock['projects']['cliproxyapi']['tree_sha256'] = project.tree_digest(reference)
                (root / 'upstream.lock.json').write_text(json.dumps(lock))
                target = root / 'upstream/cliproxyapi'
                target.parent.mkdir(); target.mkdir()
                for file in reference.iterdir():
                    (target / file.name).write_bytes(file.read_bytes())
                with self.assertRaisesRegex(ValueError, 'LICENSE|module identity'):
                    hydration.hydrate(root, offline=True)

    def test_reparse_directory_and_busy_workspace_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            root, _ = self.fixture(Path(tmp))
            build = root / '.build'; build.mkdir()
            with file_lock(build / 'promotion.lock'):
                with self.assertRaisesRegex(ValueError, 'Workspace is busy'):
                    hydration.hydrate(root)
            from types import SimpleNamespace
            info = SimpleNamespace(st_mode=stat.S_IFDIR, st_file_attributes=0x400)
            with mock.patch.object(Path, 'lstat', return_value=info):
                with self.assertRaisesRegex(ValueError, 'reparse'):
                    hydration.real_directory(root)


    def test_public_ci_exports_only_approved_metadata_paths(self):
        root = Path(__file__).parents[1]
        allowed = {
            'verify.yml': ['dist/*/build-info.json', 'dist/*/checksums.txt'],
            'upstream-candidate.yml': [
                '.build/candidates/*/candidate.json',
                '.build/candidates/*/validation.json',
                '.build/candidates/*/source/upstream.lock.json',
            ],
        }
        for name, expected in allowed.items():
            with self.subTest(workflow=name):
                text = (root / '.github/workflows' / name).read_text(encoding='utf-8')
                # An added/differently-shaped export must fail for explicit review,
                # rather than silently escaping the metadata allowlist.
                blocks = re.findall(r'(?m)^ {10}path: \|\n((?: {12}\S[^\n]*\n)+)', text)
                self.assertEqual(text.count('actions/upload-artifact@'), len(blocks))
                exported = [line.strip() for block in blocks for line in block.splitlines()]
                self.assertEqual(expected, exported)


if __name__ == '__main__':
    unittest.main()
