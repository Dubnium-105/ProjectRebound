import ctypes, hashlib, json, os, re, subprocess, sys, tempfile, time
from pathlib import Path
import argparse
parser=argparse.ArgumentParser(description='Exercise the actual updater file transaction using isolated synthetic nonexecutable files.')
parser.add_argument('--toolbox-repo',type=Path,required=True)
args=parser.parse_args()

class FILETIME(ctypes.Structure):
 _fields_=[('dwLowDateTime',ctypes.c_uint32),('dwHighDateTime',ctypes.c_uint32)]

def process_creation_filetime(pid: int) -> int:
 kernel32=ctypes.WinDLL('kernel32',use_last_error=True)
 kernel32.OpenProcess.argtypes=[ctypes.c_uint32,ctypes.c_int,ctypes.c_uint32]
 kernel32.OpenProcess.restype=ctypes.c_void_p
 kernel32.GetProcessTimes.argtypes=[ctypes.c_void_p,ctypes.POINTER(FILETIME),ctypes.POINTER(FILETIME),ctypes.POINTER(FILETIME),ctypes.POINTER(FILETIME)]
 kernel32.GetProcessTimes.restype=ctypes.c_int
 kernel32.CloseHandle.argtypes=[ctypes.c_void_p]
 handle=kernel32.OpenProcess(0x1000,False,pid)
 if not handle: raise OSError(ctypes.get_last_error(),'OpenProcess')
 creation=FILETIME(); exit_time=FILETIME(); kernel=FILETIME(); user=FILETIME()
 try:
  if not kernel32.GetProcessTimes(handle,ctypes.byref(creation),ctypes.byref(exit_time),ctypes.byref(kernel),ctypes.byref(user)):
   raise OSError(ctypes.get_last_error(),'GetProcessTimes')
  return (creation.dwHighDateTime << 32) | creation.dwLowDateTime
 finally:
  kernel32.CloseHandle(handle)

source=(args.toolbox_repo/'src/install/self_update.rs').read_text(encoding='utf-8')
script=re.search(r'const UPDATER_SCRIPT: &str = r#"(.*?)"#;',source,re.S).group(1)
results=[]
for case in ['tampered_stage','launch_failure_rollback','journal_scope_reject','parent_identity_mismatch','parent_already_exited','journal_old_metadata_recovery','journal_artifact_id_mismatch']:
 with tempfile.TemporaryDirectory(prefix='rebound-updater-test-') as temp:
  directory=Path(temp); staged=directory/'staged.exe'; target=directory/'target.exe'; updater=directory/'updater.ps1'
  staged.write_bytes(b'new synthetic invalid-PE fixture');target.write_bytes(b'previous complete strict fixture');updater.write_text(script,encoding='utf-8')
  if case=='journal_old_metadata_recovery':
   old_id='a'*32
   old_pending=directory/f'.rebound-toolbox-pending-{old_id}.exe'; old_pending.write_bytes(b'old pending candidate')
   (directory/'.rebound-toolbox-update-journal.json').write_text(json.dumps({'schema_version':1,'state':'pending','target':str(target.resolve()),'replacement':str(old_pending.resolve()),'backup':str((directory/f'.rebound-toolbox-backup-{old_id}.exe').resolve()),'update_id':old_id,'expected_size':len(b'old pending candidate'),'expected_sha256':hashlib.sha256(b'old pending candidate').hexdigest()}),encoding='utf-8')
  if case=='journal_artifact_id_mismatch':
   old_id='a'*32; wrong_id='b'*32
   (directory/'.rebound-toolbox-update-journal.json').write_text(json.dumps({'schema_version':1,'state':'pending','target':str(target.resolve()),'replacement':str((directory/f'.rebound-toolbox-pending-{wrong_id}.exe').resolve()),'backup':str((directory/f'.rebound-toolbox-backup-{old_id}.exe').resolve()),'update_id':old_id,'expected_size':1,'expected_sha256':'0'*64}),encoding='utf-8')
  parent=subprocess.Popen([sys.executable,'-c','import time; time.sleep(60)'],creationflags=subprocess.CREATE_NO_WINDOW)
  try:
   creation_time=process_creation_filetime(parent.pid)
   if case=='parent_already_exited':
    parent.kill(); parent.wait()
   digest=hashlib.sha256(staged.read_bytes()).hexdigest() if case in ('launch_failure_rollback','parent_identity_mismatch','parent_already_exited','journal_old_metadata_recovery') else '0'*64
   if case=='journal_artifact_id_mismatch': digest=hashlib.sha256(staged.read_bytes()).hexdigest()
   supplied_creation=creation_time+1 if case=='parent_identity_mismatch' else creation_time
   journal=directory/'.rebound-toolbox-update-journal.json' if case!='journal_scope_reject' else directory.parent/'.rebound-toolbox-update-journal.json'
   run=subprocess.run(['powershell.exe','-NoProfile','-NonInteractive','-ExecutionPolicy','Bypass','-File',str(updater),'-ParentProcessId',str(parent.pid),'-ParentProcessCreationFileTime',str(supplied_creation),'-Source',str(staged),'-Target',str(target),'-LogPath',str(directory/'update.log'),'-ExpectedSize',str(staged.stat().st_size),'-ExpectedSHA256',digest,'-JournalPath',str(journal),'-LockPath',str(directory/'.rebound-toolbox-update.lock')],capture_output=True,text=True,timeout=30,creationflags=subprocess.CREATE_NO_WINDOW)
   print(case, run.returncode, run.stderr, (directory/'update.log').read_text(encoding='utf-8-sig') if (directory/'update.log').exists() else 'no-log')
   assert run.returncode==1,(case,run.returncode,run.stderr)
   assert target.read_bytes()==b'previous complete strict fixture',case
   assert not list(directory.glob('.rebound-toolbox-pending-*')),case
   if case=='journal_artifact_id_mismatch': assert (directory/'.rebound-toolbox-update-journal.json').exists(),case
   else: assert not (directory/'.rebound-toolbox-update-journal.json').exists(),case
   if case in ('tampered_stage','journal_scope_reject','parent_identity_mismatch','journal_artifact_id_mismatch'): assert parent.poll() is None,f'{case} killed the parent'
   else:
    assert parent.poll() is not None,'controlled test parent was not stopped'
    assert 'UPDATE_ROLLED_BACK' in (directory/'update.log').read_text(encoding='utf-8-sig')
   if case=='parent_identity_mismatch': assert 'parent process identity changed' in (directory/'update.log').read_text(encoding='utf-8-sig').lower(),case
   results.append({'case':case,'status':'PASS','updater_exit_code':run.returncode,'target_old_complete':True,'test_scope':'actual PowerShell file transaction with synthetic nonexecutable bytes; no production application or signatures'})
  finally:
   if parent.poll() is None: parent.kill()
   parent.wait()

with tempfile.TemporaryDirectory(prefix='rebound-updater-lock-test-') as temp:
 directory=Path(temp); staged=directory/'staged.exe'; target=directory/'target.exe'; updater=directory/'updater.ps1'; holder=directory/'holder.ps1'; ready=directory/'holder.ready'
 staged.write_bytes(b'new synthetic invalid-PE fixture'); target.write_bytes(b'previous complete strict fixture'); updater.write_text(script,encoding='utf-8')
 next_path=directory/'.rebound-toolbox-update-journal.next.json'; swap_path=directory/'.rebound-toolbox-update-journal.swap-backup.json'; lock_path=directory/'.rebound-toolbox-update.lock'; journal=directory/'.rebound-toolbox-update-journal.json'
 next_bytes=b'holder journal-next sentinel'; swap_bytes=b'holder journal-swap sentinel'; next_path.write_bytes(next_bytes); swap_path.write_bytes(swap_bytes)
 holder.write_text("param([string]$LockPath,[string]$ReadyPath)\n$stream=[IO.File]::Open($LockPath,[IO.FileMode]::OpenOrCreate,[IO.FileAccess]::ReadWrite,[IO.FileShare]::None)\nSet-Content -LiteralPath $ReadyPath -Value 'held' -Encoding UTF8\nStart-Sleep -Seconds 30\n$stream.Dispose()\n",encoding='utf-8')
 holder_process=subprocess.Popen(['powershell.exe','-NoProfile','-NonInteractive','-ExecutionPolicy','Bypass','-File',str(holder),'-LockPath',str(lock_path),'-ReadyPath',str(ready)],stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True,creationflags=subprocess.CREATE_NO_WINDOW)
 parent=subprocess.Popen([sys.executable,'-c','import time; time.sleep(60)'],creationflags=subprocess.CREATE_NO_WINDOW)
 try:
  deadline=time.time()+15
  while time.time()<deadline and not ready.exists(): time.sleep(0.1)
  assert ready.exists(),'lock holder did not acquire lock'
  creation_time=process_creation_filetime(parent.pid); digest=hashlib.sha256(staged.read_bytes()).hexdigest()
  run=subprocess.run(['powershell.exe','-NoProfile','-NonInteractive','-ExecutionPolicy','Bypass','-File',str(updater),'-ParentProcessId',str(parent.pid),'-ParentProcessCreationFileTime',str(creation_time),'-Source',str(staged),'-Target',str(target),'-LogPath',str(directory/'update.log'),'-ExpectedSize',str(staged.stat().st_size),'-ExpectedSHA256',digest,'-JournalPath',str(journal),'-LockPath',str(lock_path)],capture_output=True,text=True,timeout=30,creationflags=subprocess.CREATE_NO_WINDOW)
  assert run.returncode==1,run.stderr
  assert target.read_bytes()==b'previous complete strict fixture','lock contention changed target'
  assert parent.poll() is None,'lock contention killed parent'
  assert next_path.read_bytes()==next_bytes,'lock contention changed journal-next sentinel'
  assert swap_path.read_bytes()==swap_bytes,'lock contention changed journal-swap sentinel'
  assert not journal.exists(),'lock contention created canonical journal'
  assert not list(directory.glob('.rebound-toolbox-pending-*')),'lock contention left pending candidate'
  results.append({'case':'lock_contention_preserves_shared_files','status':'PASS','updater_exit_code':run.returncode,'target_old_complete':True,'test_scope':'actual PowerShell lock holder plus fixed journal-next/swap sentinels; losing updater cannot delete shared files or kill the parent'})
 finally:
  if holder_process.poll() is None: holder_process.kill()
  holder_process.wait()
  if parent.poll() is None: parent.kill()
  parent.wait()
print(json.dumps({'command':'python Tools/Release/test_updater_transactions.py --toolbox-repo <repo>','results':results},indent=2))
