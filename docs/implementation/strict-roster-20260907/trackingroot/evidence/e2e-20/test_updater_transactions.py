import hashlib, json, os, re, subprocess, sys, tempfile
from pathlib import Path
import argparse
parser=argparse.ArgumentParser(description='Exercise the actual updater file transaction using isolated synthetic nonexecutable files.')
parser.add_argument('--toolbox-repo',type=Path,required=True)
args=parser.parse_args()
source=(args.toolbox_repo/'src/install/self_update.rs').read_text(encoding='utf-8')
script=re.search(r'const UPDATER_SCRIPT: &str = r#"(.*?)"#;',source,re.S).group(1)
results=[]
for case in ['tampered_stage','launch_failure_rollback']:
 with tempfile.TemporaryDirectory(prefix='rebound-updater-test-') as temp:
  directory=Path(temp); staged=directory/'staged.exe'; target=directory/'target.exe'; updater=directory/'updater.ps1'
  staged.write_bytes(b'new synthetic invalid-PE fixture');target.write_bytes(b'previous complete strict fixture');updater.write_text(script,encoding='utf-8')
  parent=subprocess.Popen([sys.executable,'-c','import time; time.sleep(60)'],creationflags=subprocess.CREATE_NO_WINDOW)
  try:
   digest=hashlib.sha256(staged.read_bytes()).hexdigest() if case=='launch_failure_rollback' else '0'*64
   run=subprocess.run(['powershell.exe','-NoProfile','-NonInteractive','-ExecutionPolicy','Bypass','-File',str(updater),'-ParentProcessId',str(parent.pid),'-Source',str(staged),'-Target',str(target),'-LogPath',str(directory/'update.log'),'-ExpectedSize',str(staged.stat().st_size),'-ExpectedSHA256',digest],capture_output=True,text=True,timeout=30,creationflags=subprocess.CREATE_NO_WINDOW)
   print(case, run.returncode, run.stderr, (directory/'update.log').read_text(encoding='utf-8-sig') if (directory/'update.log').exists() else 'no-log')
   assert run.returncode==1,(case,run.returncode,run.stderr)
   assert target.read_bytes()==b'previous complete strict fixture',case
   assert not list(directory.glob('.rebound-toolbox-pending-*')),case
   if case=='tampered_stage': assert parent.poll() is None,'tampered staging killed the parent'
   else:
    assert parent.poll() is not None,'controlled test parent was not stopped'
    assert 'UPDATE_ROLLED_BACK' in (directory/'update.log').read_text(encoding='utf-8-sig')
   results.append({'case':case,'status':'PASS','updater_exit_code':run.returncode,'target_old_complete':True,'test_scope':'actual PowerShell file transaction with synthetic nonexecutable bytes; no production application or signatures'})
  finally:
   if parent.poll() is None: parent.kill()
   parent.wait()
print(json.dumps({'command':'python Tools/Release/test_updater_transactions.py --toolbox-repo <repo>','results':results},indent=2))
