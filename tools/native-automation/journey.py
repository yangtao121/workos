#!/usr/bin/env python3
"""Canonical public API -> native Harness -> two actual Docker Git worktrees."""
import json, os, pathlib, time, urllib.request, urllib.error
urllib.request.install_opener(urllib.request.build_opener(urllib.request.ProxyHandler({})))
root=pathlib.Path(os.environ['WORKOS_V2_DIR'])
origin='http://127.0.0.1:'+os.environ['WORKOS_V2_GATEWAY_PORT']
project=os.environ['WORKOS_V2_PROJECT_ID']
sessions='workos.agent.v1.AgentSessionService'
tasks='workos.agent.v1.AgentTaskService'
def rpc(service,method,body):
 req=urllib.request.Request(origin+'/'+service+'/'+method,data=json.dumps(body).encode(),headers={'Content-Type':'application/json'})
 with urllib.request.urlopen(req,timeout=30) as response: return json.load(response)
def wait(predicate,label):
 for _ in range(240):
  result=predicate()
  if result: return result
  time.sleep(.5)
 raise AssertionError('timeout: '+label)
def get_session(): return rpc(sessions,'GetSession',{'sessionId':session})['session']
def submit(key,**payload):
 body={'sessionId':session,'clientInputId':key,**payload}
 item=rpc(sessions,'SubmitSessionInput',body)['input']
 assert rpc(sessions,'SubmitSessionInput',body)['input']['id']==item['id']
 return wait(lambda:rpc(sessions,'GetSessionInput',{'sessionId':session,'clientInputId':key})['input'].get('taskId'), 'dispatch '+key)
def terminal(task):
 def check():
  t=rpc(tasks,'GetTask',{'taskId':task})['task']
  if t['state'] not in ['AGENT_TASK_STATE_COMPLETED','AGENT_TASK_STATE_FAILED','AGENT_TASK_STATE_CANCELLED']: return False
  assert t['state']=='AGENT_TASK_STATE_COMPLETED',str(t)
  return t
 result=wait(check,'task terminal')
 wait(lambda:not get_session().get('activeTaskId'),'idle session')
 return result
catalog=rpc('workos.harness.v1.HarnessCatalogService','GetHarnessCatalog',{})
provider=next(p for p in catalog['providers'] if p['id']=='deepseek')
assert provider['capabilities']['sessionGoals'] and provider['capabilities']['projectSkills']
assert provider['capabilities']['maxConcurrentSubagents']==2
session=rpc(sessions,'CreateSession',{'projectId':project,'idempotencyKey':os.environ.get('WORKOS_NATIVE_RUN','native-automation')})['session']['id']
terminal(submit('delegations',text='V2_NATIVE_DELEGATE PARENT_PRIVATE_CANARY'))
children=get_session()['delegations']
assert len(children)==2 and all(c['state']=='completed' and c.get('resultArtifactId') for c in children),children
assert len({c['worktreeId'] for c in children})==2
assert len({c['baseCommit'] for c in children})==1
for name in ['alpha.txt','beta.txt']: assert not (root/'project'/name).exists(), 'child changed parent'
for child in children:
 artifact=rpc('workos.artifact.v1.ArtifactService','GetArtifact',{'artifactId':child['resultArtifactId']})
 assert 'code.unified-diff.v1' in json.dumps(artifact),artifact
print('TWO_NATIVE_CHILDREN_REAL_WORKTREES_ARTIFACTS_PASS',flush=True)
terminal(submit('skill',text='V2_NATIVE_SKILL load the project skill'))
print('PROJECT_SKILL_NATIVE_REGISTRY_PASS',flush=True)
task=submit('goal-create',directive={'kind':'SESSION_DIRECTIVE_KIND_CREATE_GOAL','objective':'V2_NATIVE_GOAL check four rounds','maxRounds':4})
goal=wait(lambda:get_session().get('goal'), 'goal projection')
body={'sessionId':session,'idempotencyKey':'pause-native-goal','goalRef':goal['ref']}
rpc(sessions,'RequestSessionGoalPause',body);rpc(sessions,'RequestSessionGoalPause',body)
terminal(task)
goal=get_session()['goal']
assert goal['phase']=='paused' and not goal.get('armed',False) and goal['roundsStarted']<4,goal
(root/'native-automation.json').write_text(json.dumps({'session':session,'children':children,'goal':goal},indent=2))
print('GOAL_CREATE_PAUSE_DURABLE_REPLAY_PASS',flush=True)
if os.environ.get('WORKOS_NATIVE_RESUME')=='1':
 terminal(submit('goal-resume',directive={'kind':'SESSION_DIRECTIVE_KIND_RESUME_GOAL','goalRef':goal['ref'],'expectedRevision':goal['revision']}))
 goal=get_session()['goal']
 assert goal['phase']=='blocked' and goal['roundsStarted']==4 and not goal.get('armed',False),goal
 print('GOAL_RESUME_NATIVE_ROUND_LIMIT_PASS',flush=True)
