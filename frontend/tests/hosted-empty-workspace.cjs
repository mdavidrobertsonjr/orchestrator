// Run after npm run build. Browser API fixtures emulate a newly signed-in account;
// no real Google session, database, or ChatGPT credentials are used.
const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');

(async () => {
  const root = path.resolve(__dirname, '../dist');
  const server = http.createServer((req, res) => {
    const filename = path.resolve(root, '.' + new URL(req.url, 'http://localhost').pathname);
    if (!filename.startsWith(root + '/') && filename !== root) { res.writeHead(404).end(); return; }
    const file = filename === root ? path.join(root, 'index.html') : filename;
    res.setHeader('Content-Type', file.endsWith('.js') ? 'application/javascript' : file.endsWith('.css') ? 'text/css' : 'text/html');
    fs.createReadStream(file).on('error', () => res.writeHead(404).end()).pipe(res);
  });
  await new Promise(resolve => server.listen(0, '0.0.0.0', resolve));
  let browser;
  try {
    browser = await chromium.launch({ headless: true });
    const page = await browser.newPage();
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.route('**/auth/config', r => r.fulfill({ json: { hosted: true, google: true, email_signup: false } }));
    await page.route('**/auth/me', r => r.fulfill({ json: { id: 'test-user', name: 'New account', email: 'test@example.com' } }));
    await page.route('**/auth/chatgpt', r => r.fulfill({ json: { connected: false } }));
    await page.route('**/healthz', r => r.fulfill({ json: { status: 'ok' } }));
    const fields = { jobs: 'jobs', workers: 'workers', postings: 'postings', workflows: 'workflows', 'workflow-runs': 'runs', results: 'results' };
    let reads = 0;
    let invalidMetrics = false;
    await page.route('**/v1/**', r => {
      const endpoint = new URL(r.request().url()).pathname.split('/').pop();
      if (endpoint === 'events') return r.fulfill({ contentType: 'text/event-stream', body: ': connected\n\n' });
      if (endpoint === 'queue') return r.fulfill({ json: { queued: 0, capacity: 0 } });
      if (endpoint === 'metrics') return r.fulfill({ json: invalidMetrics ? {} : {
        generated_at: new Date().toISOString(), queue: { queued: 0, capacity: 0, utilization: 0 },
        jobs: { total: 0, by_status: {}, attempts: 0, retry_attempts: 0, leased: 0, expired_leases: 0 },
        workers: { total: 0, by_status: {}, active: 0, running: 0, heartbeat: 0 },
        workflows: { total: 0, enabled: 0, disabled: 0, due: 0, run_records: 0 },
        postings: { total: 0 }, results: { total: 0 }, notifications: { total: 0 }, alerts: null
      } });
      if (fields[endpoint]) { reads++; return r.fulfill({ json: { [fields[endpoint]]: null } }); }
      return r.fulfill({ status: 404, json: { error: 'Unknown test endpoint' } });
    });
    await page.goto(`http://127.0.0.1:${server.address().port}`);
    await page.getByRole('button', { name: 'Sign out', exact: true }).waitFor();
    await page.waitForTimeout(6000); // Includes a subsequent polling refresh.
    assert(reads >= 6, 'Dashboard did not load every collection');
    assert.deepEqual(errors, [], 'Empty API collections crashed the dashboard');
    for (const section of ['Jobs', 'Postings', 'Workflows', 'Results', 'Workers']) {
      // Match the sidebar by its visible label, including its descriptive subtitle.
      const button = page.locator('button.nav-item').filter({ hasText: section });
      assert.equal(await button.count(), 1, `Missing ${section} navigation`);
      await button.click();
    }
    assert.deepEqual(errors, []);
    assert(await page.getByRole('button', { name: 'Sign out', exact: true }).isVisible());
    console.log('PASS: signed-in empty workspace remains visible across refreshes.');
    invalidMetrics = true;
    await page.reload();
    await page.getByRole('button', { name: 'Reload workspace', exact: true }).waitFor();
    console.log('PASS: unexpected rendering errors show recovery UI instead of a blank page.');
  } finally {
    if (browser) await browser.close();
    await new Promise(resolve => server.close(resolve));
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
