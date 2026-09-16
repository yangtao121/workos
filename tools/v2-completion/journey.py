#!/usr/bin/env python3
"""Real Gateway/Core/official Harness/Runtime flow with model-response fixtures."""
import base64, json, os, pathlib, sys, time, urllib.request, urllib.error
urllib.request.install_opener(urllib.request.build_opener(urllib.request.ProxyHandler({})))
root=pathlib.Path(os.environ['WORKOS_V2_DIR'])
origin='http://127.0.0.1:'+os.environ['WORKOS_V2_GATEWAY_PORT']
project=os.environ['WORKOS_V2_PROJECT_ID']
session_service='workos.agent.v1.AgentSessionService'
task_service='workos.agent.v1.AgentTaskService'
files_service='workos.project.v1.ProjectWorkspaceService'
question_service='workos.agent.v1.AgentInteractionService'
def rpc(service,method,body,origin_override=None,device=None):
    headers={'Content-Type':'application/json'}
    if device:
        headers['X-WorkOS-User-ID']='01999999-9999-7999-8999-000000000b01'
        headers['X-WorkOS-Device-ID']=device
    req=urllib.request.Request((origin_override or origin)+'/'+service+'/'+method,data=json.dumps(body).encode(),headers=headers)
    with urllib.request.urlopen(req,timeout=30) as response: return json.load(response)
def run_input(session,key,text,question=False):
    body={'sessionId':session,'clientInputId':key,'text':text}
    submitted=rpc(session_service,'SubmitSessionInput',body)['input']
    replay=rpc(session_service,'SubmitSessionInput',body)['input']
    assert replay['id']==submitted['id'], 'input replay changed identity'
    for attempt in range(180):
        item=rpc(session_service,'GetSessionInput',{'sessionId':session,'clientInputId':key})['input']
        task_id=item.get('taskId')
        if task_id:
            if question:
                rows=rpc(question_service,'ListTaskInteractions',{'taskId':task_id}).get('interactions',[])
                for row in rows:
                    if row['state']=='pending':
                        body={'interactionId':row['id'],'idempotencyKey':'fixture-answer','answers':[{'questionId':'direction','selected':['Continue']}]}
                        rpc(question_service,'RespondExecutionInteraction',body)
                        rpc(question_service,'RespondExecutionInteraction',body)
            task=rpc(task_service,'GetTask',{'taskId':task_id})['task']
            if task['state'] in ['AGENT_TASK_STATE_COMPLETED','AGENT_TASK_STATE_FAILED','AGENT_TASK_STATE_CANCELLED']:
                assert task['state']=='AGENT_TASK_STATE_COMPLETED', f'task failed: {task_id} {task["state"]}'
                print(key,task_id,task['state'],flush=True)
                return task_id
        time.sleep(.5)
    raise AssertionError('task did not finish')
phase=sys.argv[1]
state_path=root/'journey.json'
if phase=='first':
    session=rpc(session_service,'CreateSession',{'projectId':project,'idempotencyKey':'v2-journey'})['session']['id']
    first=run_input(session,'first','V2_DEVELOP_1 implement and test double')
    assert (root/'project/calculate.cjs').read_text()=='module.exports = n => n * 2;\n'
    state_path.write_text(json.dumps({'session':session,'firstTask':first}))
    print('FIRST_TURN_PASS',session,flush=True)
else:
    state=json.loads(state_path.read_text()); session=state['session']
    assert rpc(session_service,'GetSession',{'sessionId':session})['session']['id']==session
    assert rpc(task_service,'GetTask',{'taskId':state['firstTask']})['task']['state']=='AGENT_TASK_STATE_COMPLETED'
    run_input(session,'second','V2_DEVELOP_2 change to triple and update tests')
    assert (root/'project/calculate.cjs').read_text()=='module.exports = n => n * 3;\n'
    # The user file API reads the exact file changed by the isolated command.
    page=rpc(files_service,'ReadWorkspaceFile',{'projectId':project,'path':'calculate.cjs'})
    assert 'n * 3' in json.dumps(page), 'owner file view does not match the workspace'
    run_input(session,'artifact','V2_ARTIFACT publish the development review')
    artifacts=rpc('workos.artifact.v1.ArtifactService','ListArtifacts',{'projectId':project})
    assert 'Development review' in json.dumps(artifacts)
    (root/'project/preview.cjs').write_text("const http=require('node:http');let count=0;http.createServer((q,s)=>{s.setHeader('Content-Type','text/html');s.end('<h1>Development preview</h1><p>Request '+(++count)+'</p>')}).listen(process.env.PORT,'127.0.0.1');\n")
    run_input(session,'preview','V2_PREVIEW start the project server')
    previews=rpc('workos.surface.v1.WorkspacePreviewService','ListProjectWorkspacePreviews',{'projectId':project})['previews']
    assert len(previews)==1 and previews[0]['state']=='running'
    # Request identity and server memory survive independent HTTP clients.
    url=origin+previews[0]['url']
    with urllib.request.urlopen(url) as response: before=response.read()
    with urllib.request.urlopen(url) as response: after=response.read()
    assert before!=after and b'Development preview' in after
    run_input(session,'question','SESSION_QUESTION ask the fixture direction',True)
    state['preview']=previews[0]['id']; state_path.write_text(json.dumps(state))
    print('TWO_NATIVE_TURNS_RESTART_FILES_ARTIFACT_PREVIEW_QUESTION_PASS',session,flush=True)
