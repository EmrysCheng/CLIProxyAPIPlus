import hashlib
import io
import json
import sys
import tempfile
import unittest
import zipfile
from pathlib import Path
from types import SimpleNamespace
from unittest import mock
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'scripts'))
import project
import source_kit
import upstreams
import web_source_update


class SourceUpdateTests(unittest.TestCase):
    def fixture(self, root):
        (root / 'VERSION').write_text('1.0.0\n', encoding='utf-8')
        (root / 'scripts').mkdir()
        (root / 'scripts/worker.py').write_text('print("trusted recipe")\n', encoding='utf-8')
        projects = {}
        for name, repository, module, commit in [
            ('cliproxyapi', 'router-for-me/CLIProxyAPI', 'github.com/router-for-me/CLIProxyAPI/v8', 'a' * 40),
            ('cpa-manager-plus', 'seakee/CPA-Manager-Plus', 'github.com/seakee/cpa-manager-plus/apps/manager-server', 'b' * 40),
        ]:
            directory = root / 'upstream' / name
            directory.mkdir(parents=True)
            (directory / 'LICENSE').write_text('fixture license\n', encoding='utf-8')
            mod = directory / ('go.mod' if name == 'cliproxyapi' else 'apps/manager-server/go.mod')
            mod.parent.mkdir(parents=True, exist_ok=True)
            mod.write_text('module ' + module + '\n', encoding='utf-8')
            projects[name] = {'repository': repository, 'ref': 'v1.0.0', 'commit': commit, 'tree_sha256': project.tree_digest(directory)}
        (root / 'upstream.lock.json').write_text(json.dumps({'schema_version': 1, 'projects': projects}), encoding='utf-8')
        return projects

    def test_source_recipe_binds_code_lock_and_immutable_snapshots(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            self.fixture(root)
            (root / '.build').mkdir()
            (root / '.build/private.key').write_text('must not be packaged')
            destination = root / '.build/kit'
            data = source_kit.package(root, destination)
            self.assertEqual(upstreams.product_digest(root), data['product_source_sha256'])
            self.assertEqual(hashlib.sha256((root / 'upstream.lock.json').read_bytes()).hexdigest(), data['upstream_lock_sha256'])
            self.assertNotIn('.build/private.key', data['records'])
            self.assertFalse((destination / '.build/private.key').exists())
            for name, checksum in data['records'].items():
                self.assertEqual(checksum, hashlib.sha256((destination / name).read_bytes()).hexdigest())
            (root / 'upstream/cliproxyapi/LICENSE').write_text('changed')
            with self.assertRaisesRegex(ValueError, 'Immutable upstream'):
                source_kit.manifest(root)

    def test_pin_staging_never_follows_moving_tags_and_keeps_partner(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            projects = self.fixture(root)
            original_lock = (root / 'upstream.lock.json').read_bytes()
            package = io.BytesIO()
            with zipfile.ZipFile(package, 'w') as archive:
                archive.writestr('root/LICENSE', 'new fixture license\n')
                archive.writestr('root/go.mod', 'module github.com/router-for-me/CLIProxyAPI/v8\n')
            target = {'version': 'v1.0.1', 'commit': 'c' * 40}
            def fetch(request, **kwargs):
                self.assertEqual('https://codeload.github.com/router-for-me/CLIProxyAPI/zip/' + 'c' * 40, request.full_url)
                return io.BytesIO(package.getvalue())
            with mock.patch.object(project, 'ROOT', root), mock.patch.object(upstreams, 'ROOT', root), mock.patch.object(upstreams, 'github', side_effect=AssertionError('moving tag lookup forbidden')), mock.patch.object(upstreams, 'open_github', side_effect=fetch):
                candidate = upstreams.stage(SimpleNamespace(project='all', channel='stable', ref=None, offline=False, pins={'cliproxyapi': target}))
            staged = json.loads((candidate / 'source/upstream.lock.json').read_text(encoding='utf-8'))
            self.assertEqual('c' * 40, staged['projects']['cliproxyapi']['commit'])
            self.assertEqual('v1.0.1', staged['projects']['cliproxyapi']['ref'])
            self.assertEqual(projects['cpa-manager-plus'], staged['projects']['cpa-manager-plus'])
            self.assertEqual(original_lock, (root / 'upstream.lock.json').read_bytes())
            self.assertEqual(projects['cliproxyapi']['tree_sha256'], project.tree_digest(root / 'upstream/cliproxyapi'))

    def test_manager_only_stage_keeps_core_exactly_pinned(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            projects = self.fixture(root)
            original = (root / 'upstream.lock.json').read_bytes()
            package = io.BytesIO()
            with zipfile.ZipFile(package, 'w') as archive:
                archive.writestr('root/LICENSE', 'new manager fixture\n')
                archive.writestr('root/apps/manager-server/go.mod', 'module github.com/seakee/cpa-manager-plus/apps/manager-server\n')
            target = {'version': 'v1.0.1', 'commit': 'd' * 40}
            def fetch(request, **kwargs):
                self.assertEqual('https://codeload.github.com/seakee/CPA-Manager-Plus/zip/' + 'd' * 40, request.full_url)
                return io.BytesIO(package.getvalue())
            with mock.patch.object(project, 'ROOT', root), mock.patch.object(upstreams, 'ROOT', root), mock.patch.object(upstreams, 'open_github', side_effect=fetch):
                candidate = upstreams.stage(SimpleNamespace(project='all', channel='stable', ref=None, offline=False, pins={'cpa-manager-plus': target}))
            staged = json.loads((candidate / 'source/upstream.lock.json').read_text(encoding='utf-8'))
            self.assertEqual(projects['cliproxyapi'], staged['projects']['cliproxyapi'])
            self.assertEqual('d' * 40, staged['projects']['cpa-manager-plus']['commit'])
            self.assertEqual(original, (root / 'upstream.lock.json').read_bytes())

    def test_candidate_node_engine_is_checked_as_data_not_executable_code(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            path = root / 'upstream/cpa-manager-plus/package.json'
            path.parent.mkdir(parents=True)
            required = '^24.22.1 || >=26.12.1'
            path.write_text(json.dumps({'engines': {'node': required}}), encoding='utf-8')
            tools = {'node': 'trusted-node', 'npm_cli': str(root / 'npm/bin/npm-cli.js')}
            with mock.patch.object(web_source_update.subprocess, 'run') as run:
                web_source_update.check_node_engine(root, tools)
            command = run.call_args.args[0]
            self.assertEqual('trusted-node', command[0])
            self.assertEqual('-e', command[1])
            self.assertNotIn(required, command[2])
            self.assertEqual(required, command[3])
            self.assertTrue(run.call_args.kwargs['check'])

    def test_source_redirects_cannot_leave_fixed_https_origin(self):
        request = upstreams.urllib.request.Request('https://codeload.github.com/router-for-me/CLIProxyAPI/zip/' + 'c' * 40)
        handler = upstreams._GithubRedirect()
        for target in ['http://codeload.github.com/archive', 'https://attacker.example/archive', 'https://user:pass@codeload.github.com/archive', 'https://api.github.com/archive']:
            with self.assertRaisesRegex(ValueError, 'trusted HTTPS origin'):
                handler.redirect_request(request, None, 302, 'redirect', {}, target)

    def test_stage_pin_must_be_a_complete_commit(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            self.fixture(root)
            with mock.patch.object(project, 'ROOT', root), mock.patch.object(upstreams, 'ROOT', root):
                with self.assertRaisesRegex(ValueError, 'commit'):
                    upstreams.stage(SimpleNamespace(project='all', channel='stable', ref=None, offline=False, pins={'cliproxyapi': {'version': 'v1.0.1', 'commit': 'main'}}))


if __name__ == '__main__':
    unittest.main()
