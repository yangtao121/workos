#!/usr/bin/env python3
"""Fixture-only project and binding setup through the public Gateway."""
import json, os, sys, time, socket, urllib.request, urllib.error
urllib.request.install_opener(urllib.request.build_opener(urllib.request.ProxyHandler({})))
root=os.environ['WORKOS_V2_DIR']
origin='http://127.0.0.1:'+os.environ['WORKOS_V2_GATEWAY_PORT']
def rpc(service, method, body):
    req=urllib.request.Request(origin+'/'+service+'/'+method, data=json.dumps(body).encode(), headers={'Content-Type':'application/json'})
    with urllib.request.urlopen(req,timeout=15) as response: return json.load(response)
for attempt in range(90):
    try:
        rpc('workos.project.v1.ProjectService','ListProjects',{})
        break
    except (OSError, urllib.error.HTTPError): time.sleep(1)
else: raise RuntimeError('isolated Gateway did not become ready')
if sys.argv[1]=='runtime-ready':
    for attempt in range(60):
        try:
            with socket.create_connection(('127.0.0.1',int(os.environ['WORKOS_V2_RUNTIME_PORT'])),timeout=1): sys.exit(0)
        except OSError: time.sleep(.2)
    raise RuntimeError('Runtime did not become ready')
if sys.argv[1]=='ready': sys.exit(0)
if sys.argv[1]=='project':
    project=rpc('workos.project.v1.ProjectService','CreateProject',{'name':'Development fixture','idempotencyKey':'v2-project'})['project']
    with open(root+'/project.json','w') as f: json.dump(project,f)
    print(project['id'])
else:
    with open(root+'/project.json') as f: project=json.load(f)
    service='workos.project.v1.ProjectWorkspaceService'
    sources=rpc(service,'ListAvailableWorkspaces',{'projectId':project['id']})['sources']
    binding=rpc(service,'BindWorkspace',{'projectId':project['id'],'workspaceSourceId':sources[0]['id'],'displayName':'Development workspace','idempotencyKey':'v2-workspace'})
    rpc('workos.project.v1.ProjectHarnessBindingService','SetProjectHarnessBinding',{'projectId':project['id'],'expectedRevision':project['revision'],'providerId':'deepseek'})
    with open(root+'/binding.json','w') as f: json.dump(binding,f)
