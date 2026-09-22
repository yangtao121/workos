#!/usr/bin/env python3
"""Disposable six-process HTTPS + touch browsers + KVM Android APK acceptance."""
import hashlib, importlib.util, json, os, pathlib, subprocess, time

spec = importlib.util.spec_from_file_location('network_fixture', 'tools/network-continuity/topology.py')
n = importlib.util.module_from_spec(spec)
spec.loader.exec_module(n)
root, repo = n.root, n.repo
cache = pathlib.Path(os.environ.get('WORKOS_ANDROID_CACHE', str(pathlib.Path.home()/'.cache/workos-android')))

def adb(name, *args, capture=False):
    return n.run('docker','exec',name,'adb','-s','emulator-5554',*args,capture=capture)

def acceptance(emulator,wan,gateway):
    for _ in range(240):
        result=subprocess.run(['docker','exec',emulator,'adb','-s','emulator-5554','shell','getprop','sys.boot_completed'],text=True,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL)
        if result.returncode==0 and result.stdout.strip()=='1': break
        time.sleep(1)
    else: raise RuntimeError('Android emulator did not boot within 240 seconds')
    # This isolated Wi-Fi has no public captive-portal or private-DNS service.
    # Select it explicitly and wait for a real route + DNS answer; boot-complete
    # alone does not mean Android assigned a usable default network.
    adb(emulator,'shell','settings','put','global','captive_portal_mode','0')
    adb(emulator,'shell','settings','put','global','private_dns_mode','off')
    for state in ['disable','enable']:
        adb(emulator,'shell','svc','wifi',state)
        for _ in range(30):
            if ('Wifi is '+('disabled' if state=='disable' else 'enabled')) in adb(emulator,'shell','cmd','wifi','status',capture=True): break
            time.sleep(.5)
        else: raise RuntimeError('emulator Wi-Fi did not change state')
    adb(emulator,'shell','cmd','wifi','connect-network','AndroidWifi','open')
    for _ in range(45):
        result=subprocess.run(['docker','exec',emulator,'adb','-s','emulator-5554','shell','ping','-c','1','-W','1','workos.fixture'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=10)
        if result.returncode==0: break
        time.sleep(1)
    else: raise RuntimeError('Android fixture route and DNS unavailable')
    print('ANDROID_NETWORK_DNS_ROUTE_PASS',flush=True)
    for args in [('wm','size','390x844'),('wm','density','160'),('settings','put','global','window_animation_scale','0'),('settings','put','global','transition_animation_scale','0'),('settings','put','global','animator_duration_scale','0'),('input','keyevent','82')]: adb(emulator,'shell',*args)
    versions={key:adb(emulator,'shell','getprop',prop,capture=True).strip() for key,prop in [('release','ro.build.version.release'),('sdk','ro.build.version.sdk'),('fingerprint','ro.build.fingerprint')]}
    (root/'android-platform.json').write_text(json.dumps(versions,indent=2))
    print('KVM_ANDROID_BOOT_PASS',versions['release'],flush=True)
    n.run('python3','tools/android-acceptance/build.py',':app:assembleDebug',':app:assembleDebugAndroidTest',':app:assembleAcceptance',
        '-PworkosAcceptanceCA=/workspace/'+str((root/'tls/ca.crt').relative_to(repo)))
    apks={'debug':'debug/app-debug.apk','test':'androidTest/debug/app-debug-androidTest.apk','acceptance':'acceptance/app-acceptance.apk'}
    hashes={}
    for name,relative in apks.items():
        apk=repo/'apps/mobile-shell/android/app/build/outputs/apk'/relative
        hashes[name]=hashlib.sha256(apk.read_bytes()).hexdigest()
        adb(emulator,'install','-r','/workspace/'+str(apk.relative_to(repo)))
    (root/'android-apk-sha256.json').write_text(json.dumps(hashes,indent=2))
    result=adb(emulator,'shell','am','instrument','-w','-e','class','dev.workos.mobile.DeviceKeyVaultTest','dev.workos.mobile.test/androidx.test.runner.AndroidJUnitRunner',capture=True)
    (root/'android-keystore-tests.txt').write_text(result)
    assert 'OK (2 tests)' in result and 'FAILURES' not in result,result
    print('ANDROID_KEYSTORE_INSTRUMENTATION_PASS',flush=True)
    os.environ['WORKOS_MOBILE_BROWSER']='1'
    a=n.browser('touch-phone',wan,gateway); b=n.browser('touch-tablet',wan,gateway)
    n.phase('lan',a,b,gateway)
    native_browser(emulator,wan)

def native_browser(emulator,wan):
    env=['-e','WORKOS_ANDROID_ADB_HOST='+n.address(emulator,wan),'-e','WORKOS_ANDROID_CONTAINER='+emulator,
         '-e','WORKOS_V2_NAMESPACE','-e','WORKOS_V2_PROJECT_ID',
         '-e','WORKOS_ANDROID_ORIGIN=https://workos.fixture:'+os.environ['WORKOS_V2_GATEWAY_PORT'],
         '-e','WORKOS_E2E_OUTPUT_DIR=/workos-gate/android-results','-e','PLAYWRIGHT_JSON_OUTPUT_NAME=/workos-gate/android-results.json']
    n.run('docker','run','--rm','--network','host','--user',n.user,'--group-add',os.environ['WORKOS_V2_DOCKER_GID'],
        '-e','HOME=/tmp','-v','/var/run/docker.sock:/var/run/docker.sock','-v','/usr/bin/docker:/usr/local/bin/docker:ro',
        '-v',str(repo)+':/workspace','-v',str(root)+':/workos-gate','-w','/workspace/apps/desktop-web',
        *env,n.image,'node','node_modules/@playwright/test/cli.js','test','android-native.spec.ts','--workers=1','--reporter=line,json')
    stats=json.loads((root/'android-results.json').read_text())['stats']
    assert stats['expected']==1 and not any(stats[x] for x in ['unexpected','skipped','flaky']),stats
    print('MOBILE_BROWSER_ANDROID_APK_ACCEPTANCE_PASS',flush=True)

def main():
    try:
        if not (root/'journey.json').exists(): n.run('python3','tools/v2-completion/journey.py','first')
        (root/'tls').mkdir(exist_ok=True); (root/'turn').mkdir(exist_ok=True)
        if not (root/'tls/ca.crt').exists():
            n.run('docker','run','--rm','--user',n.user,'-e','HOME=/tmp','-e','GOMODCACHE=/go/pkg/mod',
                '-v','workos-go-cache:/go/pkg/mod','-v',str(repo)+':/workspace','-v',str(root/'tls')+':/certs',
                '-w','/workspace','golang:1.26.7-bookworm','go','run','./tests/lanpairing/gencert','-out','/certs','-hosts','workos.fixture')
        wan,cidr,gateway=n.network('android-lan')
        n.configure('lan',gateway,cidr,'')
        dns=n.container('android-dns','--network',wan,'--user',n.user,'-e','WORKOS_ANDROID_GATEWAY='+gateway,
            '-v',str(repo/'tools/android-acceptance/dns.mjs')+':/dns.mjs:ro',n.image,'node','/dns.mjs')
        home=root/'android';home.mkdir(exist_ok=True)
        emulator=n.container('android','--network',wan,'--user',n.user,
            '--device','/dev/kvm','--group-add',str(os.stat('/dev/kvm').st_gid),
            '-e','HOME=/home/android','-e','ANDROID_AVD_HOME=/home/android/avd',
            '-e','ANDROID_USER_HOME=/home/android/.android',
            '-v',str(cache/'sdk')+':/sdk:ro','-v',str(home)+':/home/android',
            '-v',str(repo)+':/workspace:ro','workos-android-test:local','sh','-ec',
            '''mkdir -p "$ANDROID_AVD_HOME" "$ANDROID_USER_HOME"
echo no | avdmanager create avd --force --name workos_fixture --package 'system-images;android-35;google_apis;x86_64' --device pixel_6
adb -a -P 5037 server nodaemon >/home/android/adb.log 2>&1 &
exec emulator -avd workos_fixture -port 5554 -no-window -no-audio -no-boot-anim -no-snapshot -no-metrics -cores 2 -memory 2048 -gpu swiftshader -dns-server "$1"
''','emulator',n.address(dns,wan))
        # Do not run an adb client before this listener exists: a polling
        # client would start its own localhost-only daemon and win the race.
        n.wait_port(n.address(emulator,wan),5037)
        (root/'android-fixture.json').write_text(json.dumps({'adbHost':n.address(emulator,wan),'container':emulator,'origin':'https://workos.fixture:'+os.environ['WORKOS_V2_GATEWAY_PORT']},indent=2))
        acceptance(emulator,wan,gateway)
    finally:
        if os.environ.get('WORKOS_V2_KEEP') != '1': n.cleanup()

if __name__=='__main__': main()
