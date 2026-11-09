// Owned experimental API: derived from api/guest-resources.openapi.yaml.
// Stateless read-only dependency. Not a stand-in for the Relay under test.
const http = require('http');
http.createServer((req, res) => {
  const url = new URL(req.url, 'http://192.0.2.10');
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
