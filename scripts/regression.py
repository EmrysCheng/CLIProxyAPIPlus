#!/usr/bin/env python3
"""Run adapter regressions plus both preserved standalone entrypoint builds."""
import argparse,os,subprocess,sys
from pathlib import Path
import project

def main():
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--go',default='go');p.add_argument('--all-upstream',action='store_true');p.add_argument('--race',action='store_true');a=p.parse_args()
 project.prepare();env=os.environ.copy();env['GOWORK']=str(project.ROOT/'go.work');env['GOCACHE']=os.environ.get('CLIPROXY_PLUS_GOCACHE',str(project.BUILD/'go-cache'));env.setdefault('GOFLAGS','-p=4')
 env.pop('GOOS',None);env.pop('GOARCH',None)
 if not a.race:env.setdefault('CGO_ENABLED','0')
 else:env['CGO_ENABLED']='1'
 manager='github.com/seakee/cpa-manager-plus/apps/manager-server';core='github.com/router-for-me/CLIProxyAPI/v8'
 packages=[manager+'/internal/'+n for n in ['lifecycle','httpapi','service/proxy','service/collector','httpqueue','service/cpa','service/cpaauthfiles','service/managerconfig','repository/sqlite','store']]+[manager+'/pkg/embedded']+[core+'/internal/'+n for n in ['api','config','watcher']]+[core+'/sdk/cliproxy']
 if a.all_upstream:packages=[manager+'/...',core+'/...']
 def run(cmd):print('>',*cmd,flush=True);subprocess.run(cmd,cwd=project.ROOT,env=env,check=True)
 # Serial adapter gate limits timer jitter; full upstream mode retains normal concurrency.
 run([a.go,'test','-count=1',*([] if a.all_upstream else ['-p=1','-parallel=1']),*(['-race'] if a.race else []),*packages])
 if not a.all_upstream:run([a.go,'test','-count=1',manager+'/cmd/cpa-manager-plus','-run','TestWaitForBackfillReadiness|TestStopCodexInspectionWorker|TestServeHTTPServer|TestDerivedMigrationsStartAfterHTTPListenerIsBound|TestManagerDatabaseProcessLockPrecedesStoreOpen'])
 destination=project.BUILD/'regressions';destination.mkdir(parents=True,exist_ok=True)
 for name,pkg in [('core',core+'/cmd/server'),('manager',manager+'/cmd/cpa-manager-plus')]:run([a.go,'build','-buildvcs=false','-o',str(destination/(name+('.exe' if os.name=='nt' else ''))),pkg])
if __name__=='__main__':
 from workspace_lock import file_lock
 with file_lock(project.BUILD/'promotion.lock'):main()
