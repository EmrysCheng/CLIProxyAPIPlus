import argparse
from contextlib import closing
import hashlib
import io
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from unittest import mock
import zipfile

sys.path.insert(0,str(Path(__file__).parents[1]/'scripts'))
import data
import project
import upstreams
import upgrade

class SnapshotTests(unittest.TestCase):
    def fixture(self,root):
        directory=root/'data';directory.mkdir();(directory/'auths').mkdir();(directory/'auths/account.json').write_text('{"token":"fixture"}',encoding='utf-8');(directory/'data.key').write_text('fixture-key',encoding='utf-8')
        with closing(sqlite3.connect(directory/'usage.sqlite',isolation_level=None)) as db:db.execute('create table fixture(value text)');db.execute("insert into fixture values('retained')")
        config=root/'config.yaml';config.write_text('host: 127.0.0.1\n',encoding='utf-8')
        return directory,config,{'auth_dir':str(directory/'auths'),'tls_enabled':False}
    def test_snapshot_restore_roundtrip_and_keys(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);directory,config,info=self.fixture(root);archive=root/'backup.zip'
            data.snapshot(config,directory,archive,info);data.verify(archive)
            with mock.patch.object(data,'run_binary',return_value=''):
                data.restore(archive,root/'restored',root/'restored.yaml',Path('unused'))
            self.assertEqual('fixture-key',(root/'restored/data.key').read_text())
            with closing(sqlite3.connect(root/'restored/usage.sqlite')) as db:self.assertEqual('retained',db.execute('select value from fixture').fetchone()[0])
    def test_restore_refuses_existing_data(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);directory,config,info=self.fixture(root);archive=root/'backup.zip';data.snapshot(config,directory,archive,info)
            with self.assertRaisesRegex(ValueError,'new data'):data.restore(archive,directory,root/'new.yaml',Path('unused'))
    def test_detects_tampering(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);directory,config,info=self.fixture(root);archive=root/'backup.zip';data.snapshot(config,directory,archive,info)
            bad=root/'bad.zip'
            with zipfile.ZipFile(archive) as source,zipfile.ZipFile(bad,'w') as target:
                for name in source.namelist():target.writestr(name,b'changed' if name=='data/data.key' else source.read(name))
            with self.assertRaisesRegex(ValueError,'checksum'):data.verify(bad)
    def test_active_process_lock_blocks_backup(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);directory,config,info=self.fixture(root)
            script="import sys;from pathlib import Path;sys.path.insert(0,sys.argv[1]);import data\nwith data.database_lock(Path(sys.argv[2])):\n print('ready',flush=True);sys.stdin.readline()"
            child=subprocess.Popen([sys.executable,'-c',script,str(Path(data.__file__).parent),str(directory)],stdin=subprocess.PIPE,stdout=subprocess.PIPE,text=True)
            try:
                self.assertEqual('ready',child.stdout.readline().strip())
                with self.assertRaisesRegex(ValueError,'locked'):data.snapshot(config,directory,root/'backup.zip',info)
            finally:
                child.communicate('\n',timeout=10)
    def test_rollback_relocates_external_auth_and_tls_into_paired_snapshot(self):
        with tempfile.TemporaryDirectory() as d:
            data_dir=Path(d)/'data'
            self.assertFalse(upgrade.restore_requires_relocation({'inspection':{'auth_dir':str(data_dir/'auths')}},data_dir))
            self.assertTrue(upgrade.restore_requires_relocation({'inspection':{'auth_dir':str(Path(d)/'external-auth')}},data_dir))
            self.assertTrue(upgrade.restore_requires_relocation({'inspection':{'auth_dir':str(data_dir/'auths'),'tls_enabled':True,'tls_cert':str(Path(d)/'external.crt'),'tls_key':str(Path(d)/'external.key')}},data_dir))

    def test_upgrade_dry_run_does_not_write(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);directory,config,_=self.fixture(root);binary=root/'current';binary.write_bytes(b'old');candidate=root/'candidate';candidate.write_bytes(b'new')
            args=argparse.Namespace(binary=binary,candidate=candidate,config=config,data_dir=directory,sha256=data.sha(candidate),apply=False,state_dir=root/'history')
            self.assertFalse(upgrade.install(args)['applied']);self.assertFalse((root/'history').exists());self.assertEqual(b'old',binary.read_bytes())
    def test_wrong_release_checksum_fails_closed(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);directory,config,_=self.fixture(root);binary=root/'current';binary.write_bytes(b'old');candidate=root/'candidate';candidate.write_bytes(b'new')
            with self.assertRaisesRegex(ValueError,'checksum'):upgrade.verify_plan(argparse.Namespace(binary=binary,candidate=candidate,config=config,data_dir=directory,sha256='0'*64))

class SourceUpdateTests(unittest.TestCase):
    def test_archive_rejects_traversal_and_symlinks(self):
        for name,mode in [('repo/../escape',None),('repo/.git/config',None),('repo/link',0o120777),('repo/.GIT/config',None),('repo/.. /escape',None),('repo/NUL',None),('repo/file:stream',None)]:
            archive=io.BytesIO()
            with zipfile.ZipFile(archive,'w') as out:
                info=zipfile.ZipInfo(name)
                if mode:info.external_attr=mode<<16
                out.writestr(info,b'payload')
            with tempfile.TemporaryDirectory() as d:
                with self.assertRaises(ValueError):upstreams.safe_extract(archive.getvalue(),Path(d))
    def test_archive_preserves_bytes(self):
        archive=io.BytesIO()
        with zipfile.ZipFile(archive,'w') as out:out.writestr('repo/file',b'LF\nCRLF\r\n')
        with tempfile.TemporaryDirectory() as d:
            upstreams.safe_extract(archive.getvalue(),Path(d));self.assertEqual(b'LF\nCRLF\r\n',(Path(d)/'file').read_bytes())
    def test_prepared_cache_detects_extra_go_file(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);(root/'source.go').write_text('package fixture',encoding='utf-8');before=project.source_inventory(root);(root/'injected.go').write_text('package fixture',encoding='utf-8');self.assertNotEqual(before,project.source_inventory(root))
    def test_unvalidated_candidate_cannot_promote(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);candidate=root/'.build/candidates/fixture';candidate.mkdir(parents=True)
            (candidate/'candidate.json').write_text('{}');(candidate/'validation.json').write_text('{"passed":false}')
            with mock.patch.object(upstreams,'ROOT',root):
                with self.assertRaises(ValueError):upstreams.promote(argparse.Namespace(candidate=str(candidate)))


class PromotionTransactionTests(unittest.TestCase):
    def fixture(self,root):
        candidate=root/'.build/candidates/fixture';source=candidate/'source';source.mkdir(parents=True)
        old={'schema_version':1,'projects':{}};new={'schema_version':1,'projects':{}}
        for name in ['cliproxyapi','cpa-manager-plus']:
            for base,content in [(root,'old-'+name),(source,'new-'+name)]:
                p=base/'upstream'/name;p.mkdir(parents=True);(p/'fixture').write_text(content)
            old['projects'][name]={'tree_sha256':project.tree_digest(root/'upstream'/name)}
            new['projects'][name]={'tree_sha256':project.tree_digest(source/'upstream'/name)}
        upstreams.save_json(root/'upstream.lock.json',old);upstreams.save_json(source/'upstream.lock.json',new)
        digest=upstreams.product_digest(root)
        upstreams.save_json(candidate/'candidate.json',{'product_digest':digest,'base_lock_sha256':upstreams.sha(root/'upstream.lock.json')})
        upstreams.save_json(candidate/'validation.json',{'passed':True,'candidate_lock_sha256':upstreams.sha(source/'upstream.lock.json')})
        return candidate,old,new
    def test_atomic_success_promotes_both_snapshots_and_lock(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);candidate,old,new=self.fixture(root)
            with mock.patch.object(upstreams,'ROOT',root):upstreams.promote(argparse.Namespace(candidate=str(candidate)))
            self.assertEqual(new,upstreams.read_json(root/'upstream.lock.json'))
            for name in old['projects']:self.assertEqual('new-'+name,(root/'upstream'/name/'fixture').read_text())
    def test_second_swap_failure_restores_both_snapshots_and_lock(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);candidate,old,new=self.fixture(root);rename=Path.rename
            def fail_second(path,target):
                if path.name=='new-cpa-manager-plus':raise OSError('injected second swap failure')
                return rename(path,target)
            with mock.patch.object(upstreams,'ROOT',root),mock.patch.object(Path,'rename',fail_second):
                with self.assertRaisesRegex(OSError,'injected'):upstreams.promote(argparse.Namespace(candidate=str(candidate)))
            self.assertEqual(old,upstreams.read_json(root/'upstream.lock.json'))
            for name in old['projects']:self.assertEqual('old-'+name,(root/'upstream'/name/'fixture').read_text())
            journal=next((root/'.build/promotions').glob('*/journal.json'));self.assertEqual('rolled_back',upstreams.read_json(journal)['state'])
    def test_new_product_change_rejects_promotion(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);candidate,old,new=self.fixture(root);(root/'VERSION').write_text('changed')
            with mock.patch.object(upstreams,'ROOT',root):
                with self.assertRaisesRegex(ValueError,'Product/adapter changed'):upstreams.promote(argparse.Namespace(candidate=str(candidate)))
            self.assertEqual(old,upstreams.read_json(root/'upstream.lock.json'))

if __name__=='__main__':unittest.main()
