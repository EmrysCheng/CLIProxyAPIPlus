#!/usr/bin/env python3
"""Generate an SPDX 2.3 dependency inventory and preserve available license notices."""
from urllib.parse import quote
import argparse
import datetime
import hashlib
import json
from pathlib import Path
import re
import shutil
import subprocess
import uuid
import project

def objects(text):
    decoder=json.JSONDecoder();position=0
    while position<len(text):
        while position<len(text) and text[position].isspace():position+=1
        if position>=len(text):return
        value,position=decoder.raw_decode(text,position);yield value

def collect_notices(directory:Path,destination:Path):
    copied=[]
    if not directory.is_dir():return copied
    base=directory.resolve()
    for file in sorted(directory.iterdir()):
        if file.is_symlink() or not file.is_file() or not file.resolve().is_relative_to(base):continue
        if file.name.upper().startswith(('LICENSE','LICENCE','COPYING','NOTICE')):
            destination.mkdir(parents=True,exist_ok=True);target=destination/file.name;shutil.copyfile(file,target)
            copied.append({'file':str(target),'sha256':hashlib.sha256(target.read_bytes()).hexdigest()})
    return copied

def notice_modules(go):
    def listing():
        return list(objects(subprocess.check_output([go,'list','-m','-json','all'],cwd=project.ROOT,text=True)))
    modules=listing()
    lock=json.loads((project.ROOT/'internal/sourceupdate/portable-notices.lock.json').read_text(encoding='utf-8'))
    required={key for rules in lock['platforms'].values() for key,rule in rules.items() if rule['required']}
    missing=[]
    for module in modules:
        effective=module.get('Replace',module)
        version=effective.get('Version',module.get('Version','workspace'))
        key=hashlib.sha256(('go:'+module['Path']+'@'+version).encode()).hexdigest()[:24]
        # Fetch only pinned notices required by the reviewed inventory. Downloading
        # the entire graph would add previously unreviewed notices from test modules.
        if key in required and effective.get('Version') and not effective.get('Dir'):
            missing.append(effective['Path']+'@'+effective['Version'])
    if missing:
        subprocess.run([go,'mod','download',*sorted(set(missing))],cwd=project.ROOT,check=True)
        modules=listing()
    return modules

def main():
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--go',default='go');parser.add_argument('--output',required=True);args=parser.parse_args()
    output=Path(args.output).resolve();output.mkdir(parents=True,exist_ok=True)
    components=[]
    for module in notice_modules(args.go):
        effective=module.get('Replace',module);name=module['Path'];version=effective.get('Version',module.get('Version','workspace'))
        components.append({'ecosystem':'go','name':name,'version':version,'license':'NOASSERTION','directory':effective.get('Dir',module.get('Dir','')),'download':'https://'+effective['Path'] if effective.get('Path','').startswith(('github.com/','golang.org/','modernc.org/')) else 'NOASSERTION'})
    front=json.loads((project.BUILD/'frontend-current.json').read_text(encoding='utf-8'));work=Path(front['work'])
    lock=json.loads((work/'package-lock.json').read_text(encoding='utf-8'))
    for path,package in lock.get('packages',{}).items():
        if not path or package.get('link'):continue
        name=package.get('name') or path.rsplit('node_modules/',1)[-1]
        declared=package.get('license','NOASSERTION');declared=declared if isinstance(declared,str) and declared else 'NOASSERTION'
        components.append({'ecosystem':'npm','name':name,'version':package.get('version','workspace'),'license':declared,'directory':str(project.child(work,path)),'download':package.get('resolved','NOASSERTION'),'dev':package.get('dev',False)})
    unique={}
    for component in components:
        identity=(component['ecosystem'],component['name'],component['version'])
        if identity not in unique:
            unique[identity]={**component,'directories':[]}
        unique[identity]['directories'].append(component['directory'])
        if unique[identity]['license']!=component['license']:unique[identity]['license']='NOASSERTION'
    components=list(unique.values())
    packages=[];notices=[];missing=[]
    for item in sorted(components,key=lambda c:(c['ecosystem'],c['name'],c['version'])):
        identity=item['ecosystem']+':'+item['name']+'@'+item['version'];key=hashlib.sha256(identity.encode()).hexdigest()[:24]
        copied=[]
        for i,directory in enumerate(sorted(set(item['directories']))):
            if directory:copied.extend(collect_notices(Path(directory),output/'licenses/dependencies'/key/str(i)))
        notices.append({**{k:v for k,v in item.items() if k not in ['directory','directories']},'notices':[{'path':Path(n['file']).relative_to(output).as_posix(),'sha256':n['sha256']} for n in copied]})
        if not copied:missing.append(identity)
        packages.append({'SPDXID':'SPDXRef-'+key,'name':item['name'],'versionInfo':item['version'],'downloadLocation':item['download'],'filesAnalyzed':False,'licenseConcluded':'NOASSERTION','licenseDeclared':item['license'],'copyrightText':'NOASSERTION','externalRefs':[{'referenceCategory':'PACKAGE-MANAGER','referenceType':'purl','referenceLocator':'pkg:'+('golang' if item['ecosystem']=='go' else 'npm')+'/'+quote(item['name'],safe='/')+('@'+quote(item['version'],safe='') if item['version']!='workspace' else '')}]})
    if len({p['SPDXID'] for p in packages})!=len(packages):raise ValueError('Duplicate SPDX package identity')
    document={'spdxVersion':'SPDX-2.3','dataLicense':'CC0-1.0','SPDXID':'SPDXRef-DOCUMENT','name':'CLIProxyAPIPlus-'+project.VERSION,'documentNamespace':'https://spdx.org/spdxdocs/cliproxyapi-plus-'+str(uuid.uuid4()),'creationInfo':{'creators':['Tool: CLIProxyAPIPlus-sbom'],'created':datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')},'documentDescribes':[p['SPDXID'] for p in packages],'packages':packages}
    (output/'sbom.spdx.json').write_text(json.dumps(document,indent=2)+'\n',encoding='utf-8')
    (output/'licenses-index.json').write_text(json.dumps(notices,indent=2)+'\n',encoding='utf-8')
    report={'schema_version':1,'components':len(packages),'missing_notice_files':missing,'manual_license_review_required':True,'notes':'Metadata collection is not a legal license approval. Missing notices and NOASSERTION values must be reviewed before publication.'}
    (output/'compliance-report.json').write_text(json.dumps(report,indent=2)+'\n',encoding='utf-8')
    print('SPDX components:',len(packages),'components without a copied notice:',len(missing))
if __name__=='__main__':main()
