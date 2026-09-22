#!/usr/bin/env python3
"""Build only WorkOS APKs, using the operator's external SDK/Gradle cache."""
import os, pathlib, subprocess, sys, urllib.parse

repo = pathlib.Path(__file__).resolve().parents[2]
cache = pathlib.Path(os.environ.get('WORKOS_ANDROID_CACHE', str(pathlib.Path.home()/'.cache/workos-android')))
(cache/'home').mkdir(parents=True, exist_ok=True, mode=0o700)
options = ''
proxy = urllib.parse.urlsplit(os.environ.get('HTTPS_PROXY', ''))
if proxy.hostname:
    if proxy.username or proxy.password:
        raise SystemExit('Use a credential-free build proxy or configure Gradle outside the repository')
    options = f'-Dhttp.proxyHost={proxy.hostname} -Dhttp.proxyPort={proxy.port or 80} -Dhttps.proxyHost={proxy.hostname} -Dhttps.proxyPort={proxy.port or 80} -Dhttp.nonProxyHosts=localhost|127.*'
args = ['docker','run','--rm','--network','host','--user',f'{os.getuid()}:{os.getgid()}',
        '-e','HOME=/build-home','-e','JAVA_TOOL_OPTIONS=-Duser.home=/build-home',
        '-e','GRADLE_USER_HOME=/gradle','-e','GRADLE_OPTS='+options,
        '-v',str(cache/'home')+':/build-home',
        '-v',str(cache/'sdk')+':/sdk','-v',str(cache/'gradle')+':/gradle',
        '-v',str(repo)+':/workspace','-w','/workspace/apps/mobile-shell/android',
        'workos-android-test:local','sh','gradlew','--no-daemon','--max-workers=4']
subprocess.run(args + (sys.argv[1:] or [':app:assembleDebug', ':app:assembleDebugAndroidTest']), check=True)
