import hashlib
import importlib.util
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('project', Path(__file__).parents[1] / 'scripts/project.py')
project = importlib.util.module_from_spec(spec)
spec.loader.exec_module(project)

class BuildIntegrityTests(unittest.TestCase):
    def test_artifact_sdk_sources_are_behind_a_go_module_boundary(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary)
            with mock.patch.object(project,'ROOT',root):
                project.isolate_artifacts()
                marker=root/'dist/go.mod'
                self.assertIn('module cliproxyapi-plus-build-artifacts',marker.read_text())
                project.isolate_artifacts()
                marker.write_text('module unexpected\n',encoding='utf-8')
                with self.assertRaisesRegex(ValueError,'Unexpected artifact module'):
                    project.isolate_artifacts()

    def test_tree_digest_is_path_and_content_sensitive(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            (root / 'a').write_bytes(b'one')
            first = project.tree_digest(root)
            (root / 'a').write_bytes(b'two')
            self.assertNotEqual(first, project.tree_digest(root))
            (root / 'a').write_bytes(b'one')
            (root / 'a').rename(root / 'b')
            self.assertNotEqual(first, project.tree_digest(root))

    def test_tree_digest_uses_platform_independent_byte_order(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d)
            for name in ['a','B','a-dir/z']:
                f=root/name;f.parent.mkdir(parents=True,exist_ok=True);f.write_bytes(b'fixture')
            expected=hashlib.sha256()
            for name in ['B','a','a-dir/z']:
                encoded=name.encode();expected.update(len(encoded).to_bytes(4,'big'));expected.update(encoded);expected.update((7).to_bytes(8,'big'));expected.update(b'fixture')
            self.assertEqual(expected.hexdigest(),project.tree_digest(root))

    def test_linux_archive_sets_executable_mode_on_any_host(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);content=root/'content';content.mkdir()
            (content/'cliproxyapi-plus').write_bytes(b'fixture');(content/'README.md').write_bytes(b'doc')
            archive=root/'release.tar.gz';project.write_release_tar(archive,content,'cliproxyapi-plus')
            with tarfile.open(archive) as packed:
                self.assertEqual(0o755,packed.getmember('cliproxyapi-plus').mode)
                self.assertEqual(0o644,packed.getmember('README.md').mode)

    def test_linux_release_server_launcher_is_executable(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);content=root/'content';content.mkdir()
            (content/'server').write_bytes(b'fixture');(content/'start.sh').write_bytes(b'#!/bin/sh\n')
            archive=root/'release.tar.gz'
            project.write_release_tar(archive,content,'server',executable_names={'start.sh'})
            with tarfile.open(archive) as packed:
                self.assertEqual(0o755,packed.getmember('server').mode)
                self.assertEqual(0o755,packed.getmember('start.sh').mode)

    def test_patch_drift_fails_before_writing(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d)
            (root/'a').write_text('alpha',encoding='utf-8')
            change={'path':'a','sha256':'0'*64,'operations':[{'find':'alpha','replace':'beta','count':1}]}
            with self.assertRaisesRegex(ValueError,'preimage drift'):
                project.apply_changes(root,[change])
            self.assertEqual('alpha',(root/'a').read_text(encoding='utf-8'))

    def test_patch_accepts_only_explicit_preimage_variants(self):
        variants=[b'alpha', b'alpha\n']
        hashes=[hashlib.sha256(raw).hexdigest() for raw in variants]
        for raw in variants:
            with self.subTest(raw=raw), tempfile.TemporaryDirectory() as d:
                root=Path(d);(root/'a').write_bytes(raw)
                change={'path':'a','sha256':hashes,
                        'operations':[{'find':'alpha','replace':'beta','count':1}]}
                project.apply_changes(root,[change])
                self.assertEqual(raw.replace(b'alpha',b'beta'),(root/'a').read_bytes())
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);(root/'a').write_bytes(b'alpha\n\n')
            with self.assertRaisesRegex(ValueError,'preimage drift'):
                project.apply_changes(root,[change])
            self.assertEqual(b'alpha\n\n',(root/'a').read_bytes())

    def test_patch_variant_schema_rejects_empty_or_ambiguous_pins(self):
        digest=hashlib.sha256(b'alpha').hexdigest()
        for pin in [[], None, True, {}, 1, [digest, None], [digest, digest],
                    ['*'], ['0'*63], [digest.upper()]]:
            with self.subTest(pin=pin), tempfile.TemporaryDirectory() as d:
                root=Path(d);(root/'a').write_bytes(b'alpha')
                change={'path':'a','sha256':pin,
                        'operations':[{'find':'alpha','replace':'beta','count':1}]}
                with self.assertRaisesRegex(ValueError,'Invalid patch preimage'):
                    project.apply_changes(root,[change])
                self.assertEqual(b'alpha',(root/'a').read_bytes())

    def test_patch_variants_keep_context_and_atomic_validation(self):
        raw=b'alpha alpha';digest=hashlib.sha256(raw).hexdigest()
        with tempfile.TemporaryDirectory() as d:
            root=Path(d)
            for name in ['a','b']:(root/name).write_bytes(raw)
            good={'path':'a','sha256':[digest],
                  'operations':[{'find':'alpha','replace':'beta','count':2}]}
            bad={**good,'path':'b','sha256':['0'*64]}
            with self.assertRaisesRegex(ValueError,'preimage drift'):
                project.apply_changes(root,[good,bad])
            self.assertEqual(raw,(root/'a').read_bytes())
            bad={**good,'path':'b','operations':[{'find':'alpha','replace':'beta','count':1}]}
            with self.assertRaisesRegex(ValueError,'count mismatch'):
                project.apply_changes(root,[good,bad])
            self.assertEqual(raw,(root/'a').read_bytes())
            self.assertEqual(raw,(root/'b').read_bytes())

    def test_v8055_exact_preimages_preserve_embedded_policy_hooks(self):
        # Reconstruct only the reviewed diff from the immutable v8.0.53
        # snapshot; every resulting byte is pinned to official v8.0.55
        # e6e5708e677adb2463d45accdd24bc9fd028ddd8, without network access.
        reviewed={
            'internal/config/parse.go':'b02c9ca81cae37fc23d5cd6442a180b103584da533f9c4f379a4342994024698',
            'internal/config/config_load.go':'03a29fc05fd601a624e874aaa7057453c1e0763b034b0b2c520cf48adae1d515',
            'sdk/cliproxy/service_config.go':'58813994284a5522539292c2b9d04ac1b9eb1f9fa659fd2c6d9529438994612f',
        }
        changes=json.loads((project.ROOT/'patches/cliproxyapi/changes.json').read_text())
        for change in changes:
            name=change['path']
            if name not in reviewed:continue
            raw=(project.ROOT/'upstream/cliproxyapi'/name).read_bytes()
            if hashlib.sha256(raw).hexdigest()!=reviewed[name]:
                if name.startswith('internal/config/'):
                    before=b'\tif cfg.LogsMaxTotalSizeMB < 0 {'
                    after=b'\tcfg.KVStore.Path = strings.TrimSpace(cfg.KVStore.Path)\n\n'+before
                    self.assertEqual(1,raw.count(before));raw=raw.replace(before,after)
                else:
                    before=b'\ts.applyDiscoveryConfigContext(ctx, cfg)\n'
                    after=before+b'\tif s.kvStoreManaged.Load() {\n\t\ts.applyKVStoreConfig(cfg)\n\t}\n'
                    self.assertEqual(1,raw.count(before));raw=raw.replace(before,after)
                    before=b'\tcfg.Plugins.StoreAuth = nil\n}'
                    after=b'\tcfg.Plugins.StoreAuth = nil\n\tcfg.KVStore.Disabled = true\n}'
                    self.assertEqual(1,raw.count(before));raw=raw.replace(before,after)
            self.assertEqual(reviewed[name],hashlib.sha256(raw).hexdigest())
            with self.subTest(path=name), tempfile.TemporaryDirectory() as d:
                root=Path(d);target=root/name;target.parent.mkdir(parents=True)
                target.write_bytes(raw)
                project.apply_changes(root,[change])
                text=target.read_text()
                self.assertEqual(1,text.count('ValidateEmbedded('))
                if name.startswith('internal/config/'):
                    self.assertEqual(1,text.count('applyEmbeddedLoggingDefaults(&cfg)'))
                    self.assertIn('cfg.KVStore.Path = strings.TrimSpace(cfg.KVStore.Path)',text)
                else:
                    self.assertIn('cfg.KVStore.Disabled = true',text)
                    self.assertIn('s.applyKVStoreConfig(cfg)',text)

    def test_patch_requires_exact_context_count(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);(root/'a').write_bytes(b'alpha alpha')
            change={'path':'a','sha256':hashlib.sha256(b'alpha alpha').hexdigest(),
                    'operations':[{'find':'alpha','replace':'beta','count':1}]}
            with self.assertRaisesRegex(ValueError,'count mismatch'):
                project.apply_changes(root,[change])
            self.assertEqual('alpha alpha',(root/'a').read_text(encoding='utf-8'))

    def test_patch_validates_all_files_before_any_write(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d)
            for name in ['a','b']:(root/name).write_bytes(b'alpha')
            good={'path':'a','sha256':hashlib.sha256(b'alpha').hexdigest(),
                  'operations':[{'find':'alpha','replace':'beta','count':1}]}
            bad={**good,'path':'b','sha256':'0'*64}
            with self.assertRaises(ValueError):project.apply_changes(root,[good,bad])
            self.assertEqual('alpha',(root/'a').read_text(encoding='utf-8'))

    def test_patch_and_overlay_paths_cannot_escape(self):
        with tempfile.TemporaryDirectory() as d:
            for name in ['../outside','a/../../outside','.',str(Path(d).parent/'outside')]:
                with self.assertRaises(ValueError):project.child(Path(d),name)

    def test_heartbeat_fixture_retains_and_strengthens_no_drain_contract(self):
        name='sdk/cliproxy/service_executionregistry_test.go'
        changes=json.loads((project.ROOT/'patches/cliproxyapi/changes.json').read_text())
        change=next(item for item in changes if item['path']==name)
        original=(project.ROOT/'upstream/cliproxyapi'/name).read_bytes()
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);target=root/name;target.parent.mkdir(parents=True)
            target.write_bytes(original);project.apply_changes(root,[change])
            text=target.read_text()
            signature='func TestServiceHeartbeatFailoverDoesNotDrainBlockingScope(t *testing.T) {'
            before=original.decode().split(signature,1)[1].split('\nfunc ',1)[0]
            after=text.split(signature,1)[1].split('\nfunc ',1)[0]
            self.assertEqual(1,before.count('heartbeat failover started draining the blocking scope'))
            self.assertEqual(5,after.count('heartbeat failover started draining the blocking scope'))
            self.assertEqual(1,after.count('time.After(3 * homeSubscriberRetryInitial)'))
            self.assertEqual(2,after.count('time.After(50 * time.Millisecond)'))
            self.assertNotIn('t.Skip',after)
            for message in ['registry was exposed before the replacement ACK',
                            'heartbeat failover replaced the registry containing the active scope',
                            'heartbeat failover canceled the service run',
                            'new subscription did not start while the old scope remained active']:
                self.assertIn(message,after)
            self.assertEqual(original.decode().split(signature,1)[0],text.split(signature,1)[0])
            self.assertEqual(original.decode().split(signature,1)[1].split('\nfunc ',1)[1],
                             text.split(signature,1)[1].split('\nfunc ',1)[1])

    def test_responses_interrupt_fixture_waits_for_asynchronous_stream_cancellation(self):
        name='sdk/api/handlers/openai/openai_responses_interrupt_test.go'
        changes=json.loads((project.ROOT/'patches/cliproxyapi/changes.json').read_text())
        change=next(item for item in changes if item['path']==name)
        original=(project.ROOT/'upstream/cliproxyapi'/name).read_bytes()
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);target=root/name;target.parent.mkdir(parents=True)
            target.write_bytes(original);project.apply_changes(root,[change])
            text=target.read_text()
            self.assertNotIn('t.Skip',text)
            self.assertIn('for deadline := time.Now().Add(2 * time.Second); !executor.canceled.Load() && time.Now().Before(deadline);',text)
            self.assertIn('t.Fatal("http upstream was not canceled")',text)

    def test_locked_official_source_trees_are_unchanged(self):
        lock=project.lockfile()
        for name,item in lock['projects'].items():
            self.assertEqual(item['tree_sha256'],project.tree_digest(project.ROOT/'upstream'/name),name)

if __name__ == '__main__': unittest.main()
