#!/usr/bin/env python3
"""Production HTTPS plus actual isolated LANs, routing and source NAT."""
import json, os, pathlib, re, secrets, signal, socket, subprocess, time

root = pathlib.Path(os.environ['WORKOS_V2_DIR'])
repo = pathlib.Path.cwd()
namespace = os.environ['WORKOS_V2_NAMESPACE']
user = os.environ['WORKOS_V2_USER']
image = 'workos-network-test:local'
compose_base = ['docker', 'compose', '-p', namespace, '-f', 'tools/v2-completion/compose.yaml']
networks, containers = [], []

def run(*args, capture=False, **kw):
    return subprocess.run(list(args), check=True, text=True, stdout=subprocess.PIPE if capture else None, **kw).stdout

def network(suffix, internal=False):
    name = namespace + '-' + suffix
    args = ['docker','network','create','--label','workos.network='+namespace]
    if internal: args.append('--internal')
    run(*args, name, capture=True); networks.append(name)
    value = json.loads(run('docker','network','inspect',name,capture=True))[0]['IPAM']['Config'][0]
    return name, value['Subnet'], value['Gateway']

def container(suffix, *args):
    name = namespace + '-' + suffix
    run('docker','run','-d','--name',name,'--label','workos.network='+namespace,*args,capture=True)
    containers.append(name)
    return name

def address(name, net):
    return json.loads(run('docker','inspect',name,capture=True))[0]['NetworkSettings']['Networks'][net]['IPAddress']

def wait_port(host, port):
    for _ in range(150):
        try:
            with socket.create_connection((host,port),timeout=.3): return
        except OSError: time.sleep(.2)
    raise RuntimeError('fixture listener did not start')

def browser(label, net, gateway, router=None):
    home = root/'network'/label; home.mkdir(parents=True)
    run('docker','run','--rm','--user',user,'-v',str(home)+':/home/node','-v',str(root/'tls')+':/certs:ro',image,
        'sh','-ec','mkdir -p /home/node/.pki/nssdb; certutil -N -d sql:/home/node/.pki/nssdb --empty-password; certutil -A -d sql:/home/node/.pki/nssdb -n WorkOS-fixture-CA -t C,, -i /certs/ca.crt')
    name = container(label,'--network',net,'--user',user,'-e','HOME=/home/node',
        '--add-host','workos.fixture:'+gateway,
        '-v',str(home)+':/home/node',image,'playwright','run-server','--host','0.0.0.0','--port','3000')
    if router:
        # Modify only this client's namespace; the browser itself has no NET_ADMIN.
        run('docker','run','--rm','--network','container:'+name,'--cap-add','NET_ADMIN',image,
            'ip','route','replace','default','via',router)
    host=address(name,net)
    wait_port(host,3000)
    return name, 'ws://'+host+':3000/'

def configure(mode, gateway, cidr, relay):
    settings={'WORKOS_RUNTIME_NATIVE_CANDIDATES':mode,
        'WORKOS_NATIVE_LAN_CIDRS':cidr if mode=='lan' else '',
        'WORKOS_NATIVE_UDP_PORTS':'52000-52063' if mode=='lan' else '',
        'WORKOS_NATIVE_TURN_URLS':'turn:'+relay+':3478?transport=udp' if mode=='relay' else '',
        'WORKOS_NATIVE_TURN_SECRET_FILE':'/fixture/turn/secret' if mode=='relay' else ''}
    overlay={'services':{'gateway':{'environment':{
        'WORKOS_HTTP_ADDRESS':gateway+':'+os.environ['WORKOS_V2_GATEWAY_PORT'],
        'WORKOS_DEV_AUTH_BYPASS':'false','WORKOS_HTTP_TLS_CERT_FILE':'/fixture/tls/leaf.crt',
        'WORKOS_HTTP_TLS_KEY_FILE':'/fixture/tls/leaf.key',
        'WORKOS_AUTH_PUBLIC_ORIGIN':'https://workos.fixture:'+os.environ['WORKOS_V2_GATEWAY_PORT'],
        'WORKOS_AUTH_ADMIN_SOCKET':'/run/workos/gateway-admin.sock'},
        'volumes':[str(root/'tls')+':/fixture/tls:ro',str(root/'run')+':/run/workos']},
        'runtime':{'environment':settings,'volumes':[str(root/'turn')+':/fixture/turn:ro']}}}
    baseline=os.environ.get('WORKOS_NETWORK_BASELINE_DIST')
    if baseline:
        overlay['services']['gateway']['volumes'].append(baseline+':/srv/workos/desktop:ro')
    (root/'network-compose.json').write_text(json.dumps(overlay))
    run(*compose_base,'-f',str(root/'network-compose.json'),'up','-d','--no-deps','--force-recreate','runtime','gateway')
    for _ in range(100):
        try:
            with socket.create_connection((gateway,int(os.environ['WORKOS_V2_GATEWAY_PORT'])),timeout=.5): return
        except OSError: time.sleep(.2)
    raise RuntimeError('TLS Gateway unavailable')

def router(label, wan, lan, gateway, relay):
    name=container(label,'--network',wan,'--cap-add','NET_ADMIN','--sysctl','net.ipv4.ip_forward=1',image,'sleep','infinity')
    run('docker','network','connect',lan,name)
    private=address(name,lan)
    script='''set -eu
wan=$(ip route show default | awk '{print $5}')
iptables -P FORWARD DROP
iptables -A FORWARD -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
iptables -A FORWARD -o "$wan" -d "$1" -p tcp --dport "$2" -j ACCEPT
iptables -A FORWARD -o "$wan" -d "$3" -p udp --dport 3478 -j ACCEPT
iptables -A FORWARD -o "$wan" -d "$3" -p udp --dport 49160:49223 -j ACCEPT
iptables -t nat -A POSTROUTING -o "$wan" -j MASQUERADE
'''
    run('docker','exec',name,'sh','-c',script,'router',gateway,os.environ['WORKOS_V2_GATEWAY_PORT'],relay)
    return name,private

def phase(mode, a, b, gateway):
    env=['-e','WORKOS_NETWORK_MODE='+mode,'-e','WORKOS_NETWORK_GATEWAY='+gateway,
         '-e','WORKOS_NETWORK_A_WS='+a[1],
         '-e','WORKOS_NETWORK_B_WS='+b[1],
         '-e','WORKOS_V2_PROJECT_ID','-e','WORKOS_V2_NAMESPACE',
         '-e','WORKOS_E2E_URL=https://workos.fixture:'+os.environ['WORKOS_V2_GATEWAY_PORT'],
         '-e','WORKOS_E2E_OUTPUT_DIR=/workos-gate/network-results-'+mode,
         '-e','PLAYWRIGHT_JSON_OUTPUT_NAME=/workos-gate/network-'+mode+'.json']
    run('docker','run','--rm','--network','host','--user',user,'--group-add',os.environ['WORKOS_V2_DOCKER_GID'],
        '-e','HOME=/tmp','-v','/var/run/docker.sock:/var/run/docker.sock','-v','/usr/bin/docker:/usr/local/bin/docker:ro',
        '-v',str(repo)+':/workspace','-v',str(root)+':/workos-gate','-w','/workspace/apps/desktop-web',
        *env,image,'node','node_modules/@playwright/test/cli.js','test','network-continuity.spec.ts','--workers=1','--reporter=line,json')
    stats=json.loads((root/('network-'+mode+'.json')).read_text())['stats']
    assert stats['expected']==1 and not any(stats[x] for x in ['unexpected','skipped','flaky']),stats

def stop_signal(sig,_): raise SystemExit(130 if sig==signal.SIGINT else 143)
signal.signal(signal.SIGINT,stop_signal);signal.signal(signal.SIGTERM,stop_signal)
try:
    (root/'tls').mkdir();(root/'turn').mkdir()
    run('docker','run','--rm','--user',user,'-e','HOME=/tmp','-e','GOMODCACHE=/go/pkg/mod',
        '-v','workos-go-cache:/go/pkg/mod','-v',str(repo)+':/workspace','-v',str(root/'tls')+':/certs',
        '-w','/workspace','golang:1.26.7-bookworm','go','run','./tests/lanpairing/gencert','-out','/certs','-hosts','workos.fixture')
    wan,cidr,gateway=network('wan')
    secret=secrets.token_hex(32)
    (root/'turn/secret').write_text(secret);(root/'turn/secret').chmod(0o600)
    # A separate TURN namespace preserves relay source addresses during hairpin
    # traffic; a host bridge address is subject to Docker's host MASQUERADE rules.
    turn=container('turn','--network',wan,'--user',user,'-v',str(root/'turn')+':/run/turn:ro',image,
        'sh','-ec','while [ ! -f /run/turn/ready ]; do sleep .1; done; exec turnserver -c /run/turn/config')
    relay=address(turn,wan)
    # Allow only this task's actual relay address.
    (root/'turn/config').write_text('\n'.join(['use-auth-secret','userdb=/tmp/turn.sqlite','relay-threads=2','static-auth-secret='+secret,'realm=workos.fixture','fingerprint',
        'listening-ip='+relay,'relay-ip='+relay,'listening-port=3478','no-tls','no-dtls','no-cli','no-tcp-relay','no-multicast-peers',
        'min-port=49160','max-port=49223','max-allocate-lifetime=600','user-quota=2','total-quota=64','max-bps=2000000','bps-capacity=32000000','verbose',
        'denied-peer-ip=0.0.0.0-255.255.255.255','allowed-peer-ip='+relay,'log-file=stdout','pidfile=/tmp/coturn.pid'])+'\n')
    (root/'turn/config').chmod(0o600)
    (root/'turn/ready').touch()
    wait_port(relay,3478)
    configure('lan',gateway,cidr,relay)
    run('docker','run','--rm','--network','host','--user',user,'-e','HOME=/tmp','-e','GOMODCACHE=/go/pkg/mod','-e','GOCACHE=/tmp/go-cache',
        '-e','WORKOS_TURN_TEST_ADDRESS='+relay+':3478','-e','WORKOS_TURN_TEST_SECRET_FILE=/turn/secret',
        '-v','workos-go-cache:/go/pkg/mod','-v',str(repo/'tmp/go-build-cache')+':/tmp/go-cache',
        '-v',str(repo)+':/workspace','-v',str(root/'turn')+':/turn:ro','-w','/workspace','golang:1.26.7-bookworm',
        'go','test','-count=1','-run','TestRealCoturnCapabilityExpiryAndForgery','-v','./internal/runtime/nativehost/adapters/turnauth')
    if os.environ.get('WORKOS_NETWORK_DEBUG_RELAY_ONLY') != '1':
        a=browser('lan-a',wan,gateway);b=browser('lan-b',wan,gateway)
        phase('lan',a,b,gateway)
        if os.environ.get('WORKOS_NETWORK_BASELINE_DIST'):
            print('NETWORK_UI_BASELINE_LAN_PASS',flush=True)
            raise SystemExit(0)
        for name in [a[0],b[0]]:run('docker','rm','-f',name,capture=True);containers.remove(name)
    left,_,_=network('private-a',True);right,_,_=network('private-b',True)
    ra,ripa=router('router-a',wan,left,gateway,relay);rb,ripb=router('router-b',wan,right,gateway,relay)
    configure('relay',gateway,cidr,relay)
    a=browser('relay-a',left,gateway,ripa);b=browser('relay-b',right,gateway,ripb)
    (root/'network-topology.json').write_text(json.dumps({'wan':cidr,'gateway':gateway,'relay':relay,'clientA':address(a[0],left),'clientB':address(b[0],right),'routerA':address(ra,wan),'routerB':address(rb,wan)},indent=2))
    phase('relay',a,b,gateway)
    for name in [ra,rb]:
        counters=run('docker','exec',name,'iptables','-t','nat','-L','POSTROUTING','-nvx',capture=True)
        line=next(line for line in counters.splitlines() if 'MASQUERADE' in line)
        assert int(line.split()[0])>0,'source NAT did not carry traffic'
        (root/(name+'-nat.txt')).write_text(counters)
    print('HTTPS_RELAY_DEBUG_PASS' if os.environ.get('WORKOS_NETWORK_DEBUG_RELAY_ONLY') == '1' else 'HTTPS_LAN_NAT_TURN_CONTINUITY_PASS',flush=True)
finally:
    for name in containers:
        # Store only fixture diagnostics; omit ephemeral TURN user capabilities.
        result=subprocess.run(['docker','logs',name],text=True,stdout=subprocess.PIPE,stderr=subprocess.STDOUT)
        safe=re.sub(r'\b\d{10}:[0-9a-f-]{36}\b','[temporary-turn-user]',result.stdout)
        if 'secret' in globals(): safe=safe.replace(secret,'[fixture-secret]')
        (root/(name+'-diagnostics.log')).write_text(safe)
        if '-router-' in name:
            result=subprocess.run(['docker','exec',name,'iptables','-L','FORWARD','-nvx'],text=True,stdout=subprocess.PIPE,stderr=subprocess.STDOUT)
            (root/(name+'-forward.txt')).write_text(result.stdout)
    for name in reversed(containers): subprocess.run(['docker','rm','-f',name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    for name in reversed(networks): subprocess.run(['docker','network','rm',name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
