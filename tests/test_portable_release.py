"""Portable releases fail closed: no metadata-only publication or mutable assets."""
import hashlib
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest
import zipfile
from unittest import mock
sys.path.insert(0, str(Path(__file__).parents[1] / 'scripts'))
import portable_release as release

class PortableReleaseTests(unittest.TestCase):
    def test_tag_validation_is_strict(self):
        for value in ['v0.4.1', 'v10.2.3']:
            self.assertEqual(release.release_version(value), value[1:])
        for value in ['master', 'v0.4.1-rc1', '../v0.4.1', 'v0.4.1;echo', 'v01.4.1', 'v0.4.1\n']:
            with self.assertRaises(ValueError): release.release_version(value)

    def test_existing_assets_require_same_digest_and_no_unexpected_assets(self):
        files = {'one.zip': 'a' * 64, 'one.zip.sha256': 'b' * 64}
        self.assertEqual(release.missing_assets(files, [{'name':'one.zip','digest':'sha256:'+'a'*64}]), ['one.zip.sha256'])
        for assets in [[{'name':'one.zip','digest':'sha256:'+'c'*64}], [{'name':'private.log','digest':'sha256:'+'a'*64}], [{'name':'one.zip','digest':None}]]:
            with self.assertRaises(ValueError): release.missing_assets(files, assets)

    def test_unsafe_and_duplicate_archive_entries_are_rejected(self):
        for names in [['../data.key'], ['config.yaml'], ['data/admin.key'], ['build-info.json','BUILD-INFO.json']]:
            with self.subTest(names=names), tempfile.TemporaryDirectory() as temp:
                archive = Path(temp) / 'invalid.zip'
                with zipfile.ZipFile(archive, 'w') as output:
                    for name in names: output.writestr(name, b'{}')
                with self.assertRaises(ValueError):
                    release.inspect_package(archive, 'windows/amd64', '0.4.1', {}, '0'*64)

    def test_publication_requires_complete_platform_set(self):
        with tempfile.TemporaryDirectory() as temp:
            with self.assertRaisesRegex(ValueError, 'three native'):
                release.collect_assets(Path(temp), '0.4.1', 'a'*40, {})

    def test_workflow_is_manual_and_separates_write_token(self):
        root = Path(__file__).parents[1]
        text = (root / '.github/workflows/portable-release.yml').read_text()
        self.assertIn('workflow_dispatch:', text)
        self.assertNotIn('pull_request', text)
        self.assertEqual(text.count('contents: write'), 1)
        self.assertIn('needs: [preflight, build]', text)
        self.assertIn('persist-credentials: false', text)
        self.assertNotIn('dist/**', text)
        self.assertNotIn('--clobber', text)
        self.assertEqual(text.count('runner: '), 3)
        verify = (root / '.github/workflows/verify.yml').read_text()
        self.assertNotIn('dist/**', verify)
        self.assertNotIn('contents: write', verify)


class PortableArchiveIntegrityTests(unittest.TestCase):
    def setUp(self):
        sha = lambda data:hashlib.sha256(data).hexdigest()
        lock = {'dependency_metadata_sha256':sha(json.dumps([],sort_keys=True).encode()),'public_notice_sha256':[sha(b'MIT')],'bundled_notice_sha256':sha(b'SDK notices'),'platforms':{target:{} for target in release.TARGETS}}
        patcher=mock.patch.object(release,'read_notices_lock',return_value=lock)
        patcher.start();self.addCleanup(patcher.stop)

    def fixture(self, target):
        binary = 'cliproxyapi-plus.exe' if target.startswith('windows/') else 'cliproxyapi-plus'
        content = {name:b'public documentation' for name in ['README_CN.md','THIRD_PARTY_NOTICES.md'] + ['docs/'+x for x in release.DOCS] + ['tools/'+x for x in release.TOOLS]}
        content.update({binary:b'fixture-program','upstream.lock.json':b'{"projects":{}}','update-tools.example.json':b'{}','compliance-report.json':b'{}','licenses-index.json':b'[]','sbom.spdx.json':b'{}','licenses/cliproxyapi-LICENSE.txt':b'MIT','licenses/cpa-manager-plus-LICENSE.txt':b'MIT','licenses/BUNDLED_TOOLS_CN.md':b'SDK notices'})
        sha = lambda value:hashlib.sha256(value).hexdigest()
        records = {name if not name.startswith('tools/') else name.replace('tools/','scripts/',1):sha(value) for name,value in content.items() if name.startswith(('docs/','tools/')) or name in ['README_CN.md','THIRD_PARTY_NOTICES.md','upstream.lock.json']}
        records.update({'upstream/cliproxyapi/LICENSE':sha(b'MIT'),'upstream/cpa-manager-plus/LICENSE':sha(b'MIT')})
        recipe = {'schema_version':1,'product_source_sha256':'a'*64,'upstream_lock_sha256':sha(content['upstream.lock.json']),'product_records':{},'records':records}
        for name,value in records.items():
            packaged = name.replace('scripts/','tools/',1) if name.startswith('scripts/') else name
            content['updater-source/'+name] = b'MIT' if name.startswith('upstream/') else content[packaged]
        content['updater-source/source-kit.json'] = json.dumps(recipe).encode()
        content['build-info.json'] = json.dumps({'target':target,'variant':'portable','version':'0.4.1','product_source_sha256':'a'*64,'upstream_lock_sha256':recipe['upstream_lock_sha256'],'binary_sha256':sha(content[binary])}).encode()
        content['checksums.txt'] = (sha(content[binary])+'  '+binary+'\n').encode()
        content['update-tools.json'] = json.dumps({'bundle':'./build-tools','go_proxy':'https://goproxy.cn|https://proxy.golang.org'}).encode()
        executable = 'go/bin/go.exe' if target.startswith('windows/') else 'go/bin/go'
        content['build-tools/'+executable] = b'fixture-sdk'
        sdk = {'target':target,'records':{executable:sha(b'fixture-sdk')},'executables':[executable]}
        content['build-tools/toolchain-manifest.json'] = json.dumps(sdk).encode()
        return content,recipe,sha(content['build-tools/toolchain-manifest.json']),{binary,'build-tools/'+executable}

    def write_archive(self, path, content, executable, tar=False):
        if not tar:
            with zipfile.ZipFile(path,'w') as output:
                for name,value in content.items():output.writestr(name,value)
        else:
            import tarfile
            with tarfile.open(path,'w:gz') as output:
                for name,value in content.items():
                    item=tarfile.TarInfo(name);item.size=len(value);item.mode=0o755 if name in executable else 0o644
                    output.addfile(item,io.BytesIO(value))

    def test_all_three_archive_formats_validate_without_extraction(self):
        for target in release.TARGETS:
            with self.subTest(target=target), tempfile.TemporaryDirectory() as temp:
                content,recipe,pin,executables=self.fixture(target)
                path=Path(temp)/release.archive_name('0.4.1',target)
                self.write_archive(path,content,executables,tar=target.startswith('linux/'))
                checked=release.inspect_package(path,target,'0.4.1',recipe,pin)
                self.assertEqual(checked['target'],target)
                self.assertEqual(checked['archive_sha256'],release.digest(path))

    def test_sdk_recipe_program_and_default_config_tampering_fail(self):
        for name in ['build-tools/go/bin/go.exe','updater-source/README_CN.md','updater-source/source-kit.json','cliproxyapi-plus.exe','update-tools.json','README_CN.md']:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temp:
                content,recipe,pin,executables=self.fixture('windows/amd64')
                content[name]=b'{}'
                path=Path(temp)/'tampered.zip';self.write_archive(path,content,executables)
                with self.assertRaises(ValueError):release.inspect_package(path,'windows/amd64','0.4.1',recipe,pin)

    def test_linux_executable_bits_are_required(self):
        with tempfile.TemporaryDirectory() as temp:
            content,recipe,pin,_=self.fixture('linux/amd64')
            path=Path(temp)/'wrong-mode.tar.gz';self.write_archive(path,content,set(),tar=True)
            with self.assertRaisesRegex(ValueError,'permissions'):release.inspect_package(path,'linux/amd64','0.4.1',recipe,pin)

class PortableReviewRegressions(PortableArchiveIntegrityTests):
    def test_unlisted_license_payload_is_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            content,recipe,pin,executables=self.fixture('windows/amd64')
            content['licenses/runtime-private.log']=b'PRIVATE_CANARY'
            path=Path(temp)/'private.zip';self.write_archive(path,content,executables)
            with self.assertRaises(ValueError):release.inspect_package(path,'windows/amd64','0.4.1',recipe,pin)

    def test_linux_setuid_bit_is_rejected(self):
        import tarfile
        with tempfile.TemporaryDirectory() as temp:
            content,recipe,pin,executables=self.fixture('linux/amd64')
            path=Path(temp)/'setuid.tar.gz'
            with tarfile.open(path,'w:gz') as output:
                for name,value in content.items():
                    item=tarfile.TarInfo(name);item.size=len(value);item.mode=0o755 if name in executables else 0o644
                    if name=='cliproxyapi-plus':item.mode=0o4755
                    output.addfile(item,io.BytesIO(value))
            with self.assertRaises(ValueError):release.inspect_package(path,'linux/amd64','0.4.1',recipe,pin)

    def test_extra_receipt_fields_cannot_be_exported(self):
        with tempfile.TemporaryDirectory() as temp:
            directory=Path(temp);checked=[]
            for target in release.TARGETS:
                name=release.archive_name('0.4.1',target)
                (directory/name).write_bytes(b'archive')
                sha=release.digest(directory/name)
                (directory/(name+'.sha256')).write_text(sha+'  '+name+'\n')
                report={'target':target,'archive':name,'archive_sha256':sha,'binary_sha256':'b'*64,'product_source_sha256':'a'*64,'version':'0.4.1','file_count':1,'unpacked_bytes':7}
                checked.append(report)
                receipt={**report,'source_commit':'a'*40,'passed':True,'native_checks':['binary-smoke-report.json','maintenance-smoke-report.json','source-update-kit-report.json','bundled-tools-report.json'],'private_log':'PRIVATE_CANARY'}
                (directory/(target.replace('/','-')+'.json')).write_text(json.dumps(receipt))
            with mock.patch.object(release,'inspect_package',side_effect=checked):
                with self.assertRaises(ValueError):release.collect_assets(directory,'0.4.1','a'*40,{})

    def test_draft_assets_are_queried_by_release_id(self):
        with tempfile.TemporaryDirectory() as temp:
            directory=Path(temp)
            for target in release.TARGETS:(directory/release.archive_name('0.4.1',target)).write_bytes(b'archive')
            paths=[]
            def fake_gh(*args):
                if args[:2]==('release','list'):return '[]'
                if args[:2]==('release','view'):return json.dumps({'isDraft':True,'databaseId':123})
                return ''
            def fake_api(path):
                paths.append(path)
                if path!='/repos/'+release.REPO+'/releases/123':raise ValueError('draft not found by published-tag endpoint')
                return {'assets':[{'name':p.name,'digest':'sha256:'+release.digest(p)} for p in directory.iterdir()]}
            with mock.patch.object(release,'preflight'),mock.patch.object(release.source_kit,'manifest',return_value={}),mock.patch.object(release,'collect_assets',return_value=[]),mock.patch.object(release,'gh',side_effect=fake_gh),mock.patch.object(release,'api',side_effect=fake_api):
                release.publish('v0.4.1','a'*40,directory)
            self.assertEqual(paths,['/repos/'+release.REPO+'/releases/123']*2)

class PortableFinalReviewRegressions(PortableArchiveIntegrityTests):
    def notices_example(self):
        content,recipe,pin,executables=self.fixture('windows/amd64')
        index=[];rules={};global_hashes=[]
        for name,value in [('component-a',b'MIT'),('component-b',b'BSD')]:
            identity='npm:'+name+'@1.0.0';key=hashlib.sha256(identity.encode()).hexdigest()[:24]
            path='licenses/dependencies/'+key+'/0/LICENSE';sha=hashlib.sha256(value).hexdigest()
            index.append({'ecosystem':'npm','name':name,'version':'1.0.0','notices':[{'path':path,'sha256':sha}]})
            content[path]=value;global_hashes.append(sha);rules[key]={'required':{path:sha},'optional':{}}
        metadata=[{k:v for k,v in e.items() if k!='notices'} for e in index]
        lock={'dependency_metadata_sha256':hashlib.sha256(json.dumps(metadata,sort_keys=True).encode()).hexdigest(),'public_notice_sha256':global_hashes,'bundled_notice_sha256':hashlib.sha256(b'SDK notices').hexdigest(),'platforms':{'windows/amd64':rules}}
        return content,recipe,pin,executables,index,lock

    def test_component_inventory_accepts_required_files_and_explicit_optional_omission(self):
        content,recipe,pin,executables,index,lock=self.notices_example()
        with tempfile.TemporaryDirectory() as temp,mock.patch.object(release,'read_notices_lock',return_value=lock):
            path=Path(temp)/'valid.zip';content['licenses-index.json']=json.dumps(index).encode();self.write_archive(path,content,executables)
            self.assertEqual(release.inspect_package(path,'windows/amd64','0.4.1',recipe,pin)['target'],'windows/amd64')
            notice=index[1]['notices'][0];key=notice['path'].split('/')[2];rule=lock['platforms']['windows/amd64'][key]
            rule['optional']=rule['required'];rule['required']={};del content[notice['path']];index[1]['notices']=[]
            content['licenses-index.json']=json.dumps(index).encode();self.write_archive(path,content,executables)
            self.assertEqual(release.inspect_package(path,'windows/amd64','0.4.1',recipe,pin)['target'],'windows/amd64')

    def test_duplicate_json_members_are_rejected_recursively(self):
        with self.assertRaises(ValueError):release.public_json('{"outer":{"value":"PRIVATE","value":"public"}}')

    def test_required_notices_cannot_be_omitted(self):
        content,recipe,pin,executables,index,lock=self.notices_example()
        for entry in index:
            for notice in entry['notices']:del content[notice['path']]
            entry['notices']=[]
        content['licenses-index.json']=json.dumps(index).encode()
        with tempfile.TemporaryDirectory() as temp,mock.patch.object(release,'read_notices_lock',return_value=lock):
            path=Path(temp)/'omitted.zip';self.write_archive(path,content,executables)
            with self.assertRaises(ValueError):release.inspect_package(path,'windows/amd64','0.4.1',recipe,pin)

    def test_another_components_public_notice_cannot_be_substituted(self):
        content,recipe,pin,executables,index,lock=self.notices_example()
        notice=index[0]['notices'][0];notice['sha256']=hashlib.sha256(b'BSD').hexdigest();content[notice['path']]=b'BSD'
        content['licenses-index.json']=json.dumps(index).encode()
        with tempfile.TemporaryDirectory() as temp,mock.patch.object(release,'read_notices_lock',return_value=lock):
            path=Path(temp)/'substituted.zip';self.write_archive(path,content,executables)
            with self.assertRaises(ValueError):release.inspect_package(path,'windows/amd64','0.4.1',recipe,pin)

    def test_duplicate_members_in_packaged_index_are_rejected(self):
        content,recipe,pin,executables,index,lock=self.notices_example()
        raw=json.dumps(index).replace('"notices":','"notices":"PRIVATE_INDEX_CANARY","notices":',1)
        content['licenses-index.json']=raw.encode()
        with tempfile.TemporaryDirectory() as temp,mock.patch.object(release,'read_notices_lock',return_value=lock):
            path=Path(temp)/'duplicated.zip';self.write_archive(path,content,executables)
            with self.assertRaises(ValueError):release.inspect_package(path,'windows/amd64','0.4.1',recipe,pin)

    def test_duplicate_members_in_receipts_are_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            directory=Path(temp);checked=[]
            for target in release.TARGETS:
                name=release.archive_name('0.4.1',target);(directory/name).write_bytes(b'archive');sha=release.digest(directory/name)
                (directory/(name+'.sha256')).write_text(sha+'  '+name+'\n')
                report={'target':target,'archive':name,'archive_sha256':sha,'binary_sha256':'b'*64,'product_source_sha256':'a'*64,'version':'0.4.1','file_count':1,'unpacked_bytes':7};checked.append(report)
                receipt={**report,'source_commit':'a'*40,'passed':True,'native_checks':['binary-smoke-report.json','maintenance-smoke-report.json','source-update-kit-report.json','bundled-tools-report.json']}
                (directory/(target.replace('/','-')+'.json')).write_text('{"source_commit":"PRIVATE_CANARY",'+json.dumps(receipt)[1:])
            with mock.patch.object(release,'inspect_package',side_effect=checked):
                with self.assertRaises(ValueError):release.collect_assets(directory,'0.4.1','a'*40,{})

if __name__ == '__main__': unittest.main()
