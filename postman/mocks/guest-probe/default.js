// Owned experimental API: derived from api/guest-resources.openapi.yaml.
// Stateless read-only dependency. Not a stand-in for the Relay under test.
const http = require('http');
http.createServer((req, res) => {
  const url = new URL(req.url, 'http://192.0.2.10');
  if (req.method === 'GET' && url.pathname === '/api/v1/guest/clock' && !url.search) {
    const received = process.hrtime.bigint();
    const scenario = req.headers['x-kno-mock-clock-case']; // Test-only fixture header.
    const clock = {
      domain: 'guest', clock: 'CLOCK_MONOTONIC', podUid: 'uid',
      instanceId: '1'.repeat(32), requestId: req.headers['x-kno-request-id'],
      identity: {bootId: '00112233-4455-6677-8899-aabbccddeeff', timeNamespace: 'unavailable'},
      receivedNs: received.toString(), sentNs: process.hrtime.bigint().toString()
    };
    switch (scenario) {
      case 'identity': clock.podUid = 'wrong'; break;
      case 'nonce': clock.requestId = '0'.repeat(32); break;
      case 'busy': res.writeHead(429); res.end(); return;
      case 'html-error': res.writeHead(500, {'Content-Type':'text/html'}); res.end('<html>failed</html>'); return;
      case 'json-error': res.writeHead(503, {'Content-Type':'application/json'}); res.end('{"code":"clock_unavailable"}'); return;
      case 'oversize': res.writeHead(200, {'Content-Type':'application/json'}); res.end(' '.repeat(8193)); return;
      case 'redirect': res.writeHead(302, {Location:'http://127.0.0.1:1/private'}); res.end(); return;
      case 'timeout': {const timer = setTimeout(() => res.end('{}'), 7000); res.on('close', () => clearTimeout(timer)); return;}
    }
    res.writeHead(200, {'Content-Type':'application/json','Cache-Control':'no-store'});
    res.end(JSON.stringify(clock)); return;
  }
  if (req.method !== 'GET' || url.pathname !== '/api/v1/guest/resources') {
    res.writeHead(501); res.end('Unmodelled operation'); return;
  }
  const scenario = url.searchParams.get('comm');
  const body = {
    domain: 'guest', podUid: 'uid', kernelRelease: '6.18.56',
    bootId: '00112233-4455-6677-8899-aabbccddeeff', instanceId: '1'.repeat(32),
    requestId: req.headers['x-kno-request-id'], startedAt: new Date().toISOString(),
    durationSeconds: 0.1, totalItems: 0, processes: []
  };
  const send = () => { res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify(body)); };
  if (url.searchParams.get('timing') === 'true' && scenario !== 'timing-missing') {
    const span = (a, b) => ({beforeNs: a.toString(), afterNs: b.toString()});
    body.timing = {
      clock: 'CLOCK_MONOTONIC', identity: {bootId: body.bootId, timeNamespace: 'unavailable'},
      start: {cpu: span(10n, 11n), runqlat: span(12n, 13n)},
      end: {cpu: span(100000020n, 100000021n), runqlat: span(100000022n, 100000023n)}
    };
    if (scenario === 'timing-order') body.timing.end.cpu.beforeNs = '1';
    if (scenario === 'timing-boot') body.timing.identity.bootId = '00112233-4455-6677-8899-aabbccddeefa';
  }
  switch (scenario) {
    case 'busy': res.writeHead(429, {'Content-Type': 'application/json'}); res.end('{"code":"measurement_busy"}'); return;
    case 'json-error': res.writeHead(503, {'Content-Type': 'application/json'}); res.end('{"code":"measurement_unavailable"}'); return;
    case 'html-error': res.writeHead(500, {'Content-Type': 'text/html'}); res.end('<html>failed</html>'); return;
    case 'identity': body.podUid = 'wrong'; break;
    case 'nonce': body.requestId = '0'.repeat(32); break;
    case 'oversize': res.writeHead(200, {'Content-Type': 'application/json'}); res.end(' '.repeat((2 << 20) + 1)); return;
    case 'redirect': res.writeHead(302, {Location: 'http://127.0.0.1:1/private'}); res.end(); return;
    case 'timeout': setTimeout(send, 7000); return;
  }
  send();
}).listen(process.env.PORT);
