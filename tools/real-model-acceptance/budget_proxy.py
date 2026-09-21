#!/usr/bin/env python3
"""Acceptance-only spending guard. No model loop, retries, or credential storage.

Peak Flash prices verified 2026-09-21: input 2/output 8 CNY per million.
https://api-docs.deepseek.com/zh-cn/quick_start/pricing/
Reserve worst-case byte-token input plus 32768 framing tokens and all requested
output BEFORE forwarding. Failed/unknown requests keep their reservation.
"""
import http.server
import fcntl
import json
import os
import pathlib
import threading
import urllib.request
import urllib.error

LIMIT_MICROCNY = 19_000_000
MAX_BODY = 128 * 1024
MAX_RESPONSE = 8 * 1024 * 1024

class Budget:
    def __init__(self, ledger):
        self.ledger = pathlib.Path(ledger)
        self.lock = threading.RLock()
        self.process_lock = open(str(self.ledger) + '.lock', 'a')
        os.chmod(str(self.ledger) + '.lock', 0o600)
        try:
            fcntl.flock(self.process_lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError:
            self.process_lock.close()
            raise
        self.spent = 0
        if self.ledger.exists():
            for line in self.ledger.read_text().splitlines():
                self.spent += json.loads(line).get('reserved_microcny', 0)

    def reserve(self, body):
        value = json.loads(body)
        tokens = value.get('max_tokens', value.get('max_completion_tokens'))
        if value.get('model') not in ('deepseek-v4-flash', 'deepseek-flash'):
            raise ValueError('unpriced model')
        if type(tokens) is not int or tokens < 1 or tokens > 8192 or len(body) > MAX_BODY:
            raise ValueError('unbounded request')
        # UTF-8 bytes overestimate text tokens; extra allowance covers chat and
        # tool framing. No cache-discount assumption and no reservation refunds.
        cost = (len(body) + 32768) * 2 + tokens * 8
        with self.lock:
            if self.spent + cost > LIMIT_MICROCNY:
                raise ValueError('acceptance budget exhausted')
            self.record({'reserved_microcny': cost, 'request_bytes': len(body), 'max_output_tokens': tokens})
            self.spent += cost

    def record(self, value):
        with self.lock, self.ledger.open('a') as stream:
            os.chmod(self.ledger, 0o600)
            stream.write(json.dumps(value) + '\n')
            stream.flush()
            os.fsync(stream.fileno())

class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        if self.path not in ('/chat/completions', '/v1/chat/completions'):
            self.send_error(404)
            return
        try:
            size = int(self.headers.get('Content-Length', '0'))
            if not 0 < size <= MAX_BODY:
                raise ValueError('request size')
            body = self.rfile.read(size)
            authorization = self.headers.get('Authorization', '')
            if not authorization.startswith('Bearer ') or '\n' in authorization:
                raise ValueError('missing credential')
            self.server.budget.reserve(body)
        except (ValueError, OSError):
            self.send_error(429, 'Acceptance budget or request refused')
            return
        request = urllib.request.Request('https://api.deepseek.com/chat/completions', data=body,
            headers={'Content-Type': 'application/json', 'Authorization': authorization})
        try:
            with urllib.request.urlopen(request, timeout=180) as response:
                content = response.read(MAX_RESPONSE + 1)
                if len(content) > MAX_RESPONSE:
                    raise ValueError('response size')
                # Store usage only. Never log request/response text or headers.
                records = [content] if response.headers.get('Content-Type', '').startswith('application/json') else [line[6:] for line in content.splitlines() if line.startswith(b'data: {')]
                for record in records:
                    try:
                        usage = json.loads(record).get('usage')
                        if usage:
                            safe = {key: value for key, value in usage.items() if key in ('prompt_tokens', 'completion_tokens', 'total_tokens', 'prompt_cache_hit_tokens', 'prompt_cache_miss_tokens') and type(value) is int}
                            self.server.budget.record({'usage': safe})
                    except ValueError:
                        pass
                self.send_response(response.status)
                self.send_header('Content-Type', response.headers.get('Content-Type', 'application/json'))
                self.send_header('Content-Length', str(len(content)))
                self.end_headers()
                self.wfile.write(content)
        except (OSError, ValueError, urllib.error.URLError):
            self.server.budget.record({'upstream_failed': True})
            self.send_error(502, 'Live provider request failed; reservation retained')

if __name__ == '__main__':
    import sys
    server = http.server.ThreadingHTTPServer(('127.0.0.1', int(sys.argv[1])), Handler)
    server.budget = Budget(sys.argv[2])
    server.serve_forever()
