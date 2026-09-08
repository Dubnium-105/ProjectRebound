"""Import numeric product-counter observations with their real test-only inputs."""
import hashlib
import json
import re
from pathlib import Path

repo=Path('C:/wksp/ProjectRebound')
root=repo/'docs/implementation/strict-roster-20260907'
read=lambda p:json.loads(p.read_text(encoding='utf-8-sig'))
sha=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
records=read(root/'evidence/final-supplements/retained-files.json')['files']
def retained(path,expected=None):
    path=Path(path)
    digest=sha(path)
    if expected and expected.lower()!=digest: raise ValueError('Counter evidence changed')
    match=next(r for r in reversed(records) if Path(r['original_path']).resolve()==path.resolve() and r['sha256']==digest)
    out=Path(match['path'])
    if sha(out)!=digest: raise ValueError('Retained counter evidence changed')
    return out
base=repo/'.tmp/strict-roster-20260907/bp014-counter-20260908T083620Z'
entries=[]
refs=[]
for filename in ('bp014-counter-receipt.json','bp014-toolbox-route-loop-receipt.json'):
    receipt_path=retained(base/filename)
    data=read(receipt_path)
    log=retained(data['log_path'],data['log_sha256'])
    source=retained(data['test_source_copy'],data['test_source_sha256'])
    refs += [receipt_path,log,source]
    is_rust='baseline_toolbox_commit' in data
    execution_pair=({'Toolbox':data['baseline_toolbox_commit']} if is_rust else
        {'ProjectRebound':data['repositories']['ProjectRebound']['head'],
         'Toolbox':data['repositories']['ProjectReboundToolbox']['head']})
    entry={**data,'id':data['id'],'status':'PASS','skip_names':[],
        'log_path':str(log),'log_sha256':sha(log),'execution_log_path':data['log_path'],
        'receipt_path':str(receipt_path),'receipt_sha256':sha(receipt_path),
        'test_source_copy':str(source),'execution_source_pair':execution_pair,
        'source_dirty':{'Toolbox':True} if is_rust else {'ProjectRebound':True,'Toolbox':None},
        'source_scope':'Temporary test code was inserted into the actual product module. Original product bytes were restored; relevant-path cleanliness in the older receipt is not whole-repository execution cleanliness.',
        'issue_ids':['BP-014']}
    if is_rust:
        full=retained(data['temporary_full_source_copy'],data['temporary_full_source_sha256'])
        refs.append(full)
        entry['temporary_full_source_copy']=str(full)
        entry['source_files']={'temporary_controller.rs':data['temporary_full_source_sha256']}
        expected={key:1 for key in ('host_datagrams_sent','host_datagrams_received','member_datagrams_sent','member_datagrams_received')}
        if any(data['observed_product_stats'].get(k)!=v for k,v in expected.items()): raise ValueError('Product counters lack exact observations')
    entries.append(entry)
    for index, prior in enumerate(data.get('prior_attempts',[])):
        prior_log=retained(prior['path'],prior['sha256'])
        refs.append(prior_log)
        entries.append({'id':data['id']+'-history-'+str(index),'status':'PASS' if prior['exit_code']==0 else 'EXECUTED_FAIL',
            'exit_code':prior['exit_code'],'command':None,'command_note':'Only the actual outcome and log were retained for this historical attempt.',
            'log_path':str(prior_log),'log_sha256':sha(prior_log),'skip_names':[],
            'execution_source_pair':None,'source_dirty':True,'scope':prior['classification']})
for filename in ('cargo-test-toolbox-route-loop.log','go-test-toolbox-private-counters.log',
                 'go-test-toolbox-private-counters-rerun.log','go-test-toolbox-private-counters-final.log'):
    log=retained(base/filename)
    values=re.findall(r'^EXIT_CODE=(\d+)\s*$',log.read_text(encoding='utf-8-sig',errors='replace'),re.M)
    if len(values)!=1: raise ValueError('Historical counter invocation lacks actual exit footer')
    code=int(values[0])
    refs.append(log)
    entries.append({'id':'bp014-counter-history-'+filename,'exit_code':code,
        'status':'PASS_HELPER_ONLY' if code==0 else 'EXECUTED_FAIL',
        'command':None,'command_note':'Complete historical shell command unavailable; actual outcome retained in the log footer.',
        'log_path':str(log),'log_sha256':sha(log),'skip_names':[],
        'execution_source_pair':None,'source_dirty':True,
        'scope':'Historical timeout/compile failures or a helper-only test. The helper-only success did not invoke production route_loop and is not used as counter-wiring acceptance.'})
for owner in ('root','backend','toolbox'):
    path=root/(owner+'-report.json')
    report=read(path)
    ids={entry['id'] for entry in entries}
    report['tests']=[entry for entry in report.get('tests',[]) if entry.get('id') not in ids]+entries
    for issue in report.get('issues',[]):
        if issue['id']=='BP-014':
            issue['evidence']=list(dict.fromkeys(issue.get('evidence',[])+[str(p) for p in refs]))
            issue['note']='Actual product route_loop ran on separate HOST/MEMBER threads with bidirectional real UDP and all four LegacyRouteStatus counters equal to 1. Separate actual Go Edge Metrics.Snapshot observed received +6, forwarded +2 and two successful binds through the Rust driver. Earlier error/replay/MTU tests retain their separate source/logs; temporary test inputs are explicitly dirty and restored.'
    path.write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
print(json.dumps({'counter_invocations_imported':len(entries),'current_candidate_acceptance_promoted':False}))
