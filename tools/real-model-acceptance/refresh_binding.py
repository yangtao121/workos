#!/usr/bin/env python3
import json, os, urllib.request
urllib.request.install_opener(urllib.request.build_opener(urllib.request.ProxyHandler({})))
origin='http://127.0.0.1:'+os.environ['WORKOS_V2_GATEWAY_PORT']
def rpc(service,method,body):
    with urllib.request.urlopen(urllib.request.Request(origin+'/'+service+'/'+method,data=json.dumps(body).encode(),headers={'Content-Type':'application/json'}),timeout=30) as response: return json.load(response)
project=rpc('workos.project.v1.ProjectService','GetProject',{'projectId':os.environ['WORKOS_V2_PROJECT_ID']})['project']
rpc('workos.project.v1.ProjectHarnessBindingService','SetProjectHarnessBinding',{'projectId':project['id'],'expectedRevision':project['revision'],'providerId':'deepseek'})
