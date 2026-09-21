#!/usr/bin/env python3
"""Kill authority while real delegated Docker commands are still running."""
import json, os, pathlib, subprocess, time, urllib.request
urllib.request.install_opener(urllib.request.build_opener(urllib.request.ProxyHandler({})))
root=pathlib.Path(os.environ['WORKOS_V2_DIR'])
origin='http://127.0.0.1:'+os.environ['WORKOS_V2_GATEWAY_PORT']
project=os.environ['WORKOS_V2_PROJECT_ID']
svc='workos.agent.v1.AgentSessionService'
def rpc(service,method,body):
 with urllib.request.urlopen(urllib.request.Request(origin+'/'+service+'/'+method,data=json.dumps(body).encode(),headers={'Content-Type':'application/json'}),timeout=30) as r:return json.load(r)
def wait(fn,label):
 for _ in range(480):
  value=fn()
  if value:return value
  time.sleep(.25)
 raise AssertionError('timeout: '+label)
def containers():
 return subprocess.check_output(['docker','ps','-q','--filter','label=workos.owner=runtime-workspace','--filter','label=workos.runtime='+os.environ['WORKOS_V2_NAMESPACE']],text=True).split()
for mode in ['cancel','harness-restart','revoke']:
 session=rpc(svc,'CreateSession',{'projectId':project,'idempotencyKey':'native-exception-'+mode})['session']['id']
 rpc(svc,'SubmitSessionInput',{'sessionId':session,'clientInputId':'hold','text':'V2_NATIVE_DELEGATE_HOLD PARENT_PRIVATE_CANARY'})
 def held():
  children=rpc(svc,'GetSession',{'sessionId':session})['session'].get('delegations',[])
  if len(children)==2 and all((root/'delegations'/c['id']/'tree/held.txt').exists() for c in children):return children
  return False
 children=wait(held,'two active commands')
 wait(lambda:len(containers())==2,'two execution containers')
 if mode=='cancel':rpc(svc,'CancelSessionExecution',{'sessionId':session,'reason':'fixture cancellation'})
 elif mode=='harness-restart':
  subprocess.run(['docker','compose','-p',os.environ['WORKOS_V2_NAMESPACE'],'-f','tools/v2-completion/compose.yaml','kill','harness'],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.STDOUT)
 else:
  bindings=rpc('workos.project.v1.ProjectWorkspaceService','ListProjectWorkspaces',{'projectId':project})['bindings']
  binding=next(b for b in bindings if b['state']=='WORKSPACE_BINDING_STATE_ACTIVE')
  updated=rpc('workos.project.v1.ProjectWorkspaceService','UpdateWorkspaceAccess',{'bindingId':binding['id'],'expectedRevision':binding['revision'],'readOnly':True})['binding']
 wait(lambda:not containers(),'container teardown after '+mode)
 if mode=='harness-restart':
  subprocess.run(['docker','compose','-p',os.environ['WORKOS_V2_NAMESPACE'],'-f','tools/v2-completion/compose.yaml','start','harness'],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.STDOUT)
 def settled():
  s=rpc(svc,'GetSession',{'sessionId':session})['session']
  return s if not s.get('activeTaskId') and all(c['state']=='needs_review' for c in s.get('delegations',[])) else False
 wait(settled,'durable review after '+mode)
 assert not (root/'project/held.txt').exists()
 if mode=='revoke':rpc('workos.project.v1.ProjectWorkspaceService','UpdateWorkspaceAccess',{'bindingId':binding['id'],'expectedRevision':updated['revision'],'readOnly':False})
 print('NATIVE_CHILDREN_'+mode.upper()+'_TEARDOWN_REVIEW_PASS',flush=True)
