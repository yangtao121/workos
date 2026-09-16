#!/usr/bin/env python3
"""Two trusted device identities on the private Runtime listener; real PTY."""
import base64, json, os, pathlib, sys, time, urllib.request, urllib.error
urllib.request.install_opener(urllib.request.build_opener(urllib.request.ProxyHandler({})))
root=pathlib.Path(os.environ['WORKOS_V2_DIR']); project=os.environ['WORKOS_V2_PROJECT_ID']
origin='http://127.0.0.1:'+os.environ['WORKOS_V2_RUNTIME_PORT']
owner='01999999-9999-7999-8999-000000000b01'
a='01999999-9999-7999-8999-000000000b02'; b='01999999-9999-7999-8999-000000000b03'
def rpc(service,method,body,device=a):
    req=urllib.request.Request(origin+'/workos.surface.v1.'+service+'/'+method,data=json.dumps(body).encode(),headers={'Content-Type':'application/json','X-WorkOS-User-ID':owner,'X-WorkOS-Device-ID':device})
    with urllib.request.urlopen(req,timeout=30) as response:return json.load(response)
def denied(service,method,body,device=a):
    try: rpc(service,method,body,device)
    except urllib.error.HTTPError as e:
        assert json.load(e)['code']=='permission_denied'; return
    raise AssertionError('stale device operation was admitted')
def write(id,epoch,text,device=a):
    return rpc('PtySessionService','WritePtySession',{'sessionId':id,'controlGeneration':epoch,'input':base64.b64encode(text.encode()).decode()},device)
def wait_file(name):
    for _ in range(60):
        if (root/'project'/name).exists():return (root/'project'/name).read_text()
        time.sleep(.1)
    raise AssertionError('terminal file missing: '+name)
if sys.argv[1]=='before':
    item=rpc('PtySessionService','CreatePtySession',{'projectId':project,'idempotencyKey':'v2-continuity-verified','columns':90,'rows':26})['session']; id=item['id']
    first=rpc('SurfaceContinuityService','AttachSurface',{'workloadId':id,'idempotencyKey':id+'-a'})['attachment']
    epoch=first['controlGeneration']; assert first['controls']
    write(id,epoch,"export V2_MEMORY=preserved; (sleep 1; printf background > background.txt) &\n")
    rpc('SurfaceContinuityService','DetachSurface',{'surfaceSessionId':id})
    observer=rpc('SurfaceContinuityService','AttachSurface',{'workloadId':id,'idempotencyKey':id+'-b'},b)['attachment']; assert not observer.get('controls',False)
    takeover=rpc('SurfaceContinuityService','RequestSurfaceControl',{'surfaceSessionId':id},b)['attachment']; epoch_b=takeover['controlGeneration']
    denied('PtySessionService','WritePtySession',{'sessionId':id,'controlGeneration':epoch,'input':base64.b64encode(b'false\n').decode()})
    denied('PtySessionService','ResizePtySession',{'sessionId':id,'controlGeneration':epoch,'columns':20,'rows':20})
    write(id,epoch_b,'printf "%s" "$V2_MEMORY" > memory.txt; node --test calculate.test.cjs > terminal-tests.txt\n',b)
    assert wait_file('memory.txt')=='preserved'; assert wait_file('background.txt')=='background'
    # A reattaches without implicit takeover, then explicitly retakes. Its
    # previous epoch is still invalid even though device identity matches.
    late=rpc('SurfaceContinuityService','AttachSurface',{'workloadId':id,'idempotencyKey':id+'-a-again'})['attachment']; assert not late.get('controls',False)
    current=rpc('SurfaceContinuityService','RequestSurfaceControl',{'surfaceSessionId':id})['attachment']
    denied('PtySessionService','ResizePtySession',{'sessionId':id,'controlGeneration':epoch,'columns':20,'rows':20})
    write(id,current['controlGeneration'],'printf accepted > current-controller.txt\n'); assert wait_file('current-controller.txt')=='accepted'
    stopped=rpc('SurfaceContinuityService','StopSurfaceWorkload',{'workloadId':id,'actionKey':'stop'})['workload']; assert stopped['state']=='closed'
    restarted=rpc('SurfaceContinuityService','RestartSurfaceWorkload',{'workloadId':id,'actionKey':'restart'})['workload']; assert restarted['generation']=='2' and restarted['workloadId']==id
    duplicate=rpc('SurfaceContinuityService','StopSurfaceWorkload',{'workloadId':id,'actionKey':'stop'})['workload']; assert duplicate['generation']=='2' and duplicate['state']=='running'
    (root/'continuity.json').write_text(json.dumps({'id':id})); print('PTY_MEMORY_BACKGROUND_CONTROL_EPOCH_RESTART_PASS',id)
else:
    id=json.loads((root/'continuity.json').read_text())['id']
    facts=rpc('SurfaceContinuityService','GetSurfaceControl',{'workloadId':id})
    assert not facts.get('workloadRunning',False), 'restart reported dead program running'
    print('RUNTIME_RESTART_DEAD_PROGRAM_FENCED_PASS',id)
