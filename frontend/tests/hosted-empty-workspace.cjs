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
    const fields = { jobs: 'jobs', workers: 'workers', postings: 'postings', workflows: 'workflows', 'workflow-runs': 'runs', results: 'results', notifications: 'notifications' };
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
    for (const section of ['Postings', 'Monitors', 'Commands']) {
      // Match the sidebar by its visible label, including its descriptive subtitle.
      const button = page.locator('button.nav-item').filter({ hasText: section });
      assert.equal(await button.count(), 1, `Missing ${section} navigation`);
      await button.click();
    }
    // Results and workers are intentionally consolidated into Monitors and Overview.
    assert.equal(await page.locator('button.nav-item').filter({ hasText: 'Results' }).count(), 0);
    assert.equal(await page.locator('button.nav-item').filter({ hasText: 'Workers' }).count(), 0);
    assert.deepEqual(errors, []);
    assert(await page.getByRole('button', { name: 'Sign out', exact: true }).isVisible());
    console.log('PASS: signed-in empty workspace remains visible across refreshes.');
    // A disconnected account can still create manual schedules.
    await page.locator('button.nav-item').filter({ hasText: 'Commands' }).click();
    assert(await page.getByRole('button', { name: 'Run Command', exact: true }).isDisabled());
    assert(await page.getByRole('button', { name: 'Save schedule', exact: true }).isEnabled());
    await page.route('**/auth/chatgpt', r => r.fulfill({ json: { connected: true } }));
    await page.reload();
    await page.locator('button.nav-item').filter({ hasText: 'Commands' }).click();
    await page.waitForFunction(() => [...document.querySelectorAll('button')].some(b => b.textContent === 'Run Command' && !b.disabled));
    let finishPlan;
    const pendingPlan = new Promise(resolve => { finishPlan = resolve; });
    let commandRequests = 0;
    await page.route('**/v1/commands/natural', async r => {
      commandRequests++;
      await pendingPlan;
      await r.fulfill({ json: { action: 'workflow', workflow: { id: 'planned', name: 'Hourly role monitor', job_type: 'jobs.monitor.new_grad', interval_seconds: 3600, enabled: true } } });
    });
    await page.getByRole('button', { name: 'Run Command', exact: true }).click();
    await page.locator('.planning-status').waitFor();
    assert(await page.getByRole('button', { name: 'Generating plan…', exact: true }).isDisabled());
    assert(await page.getByRole('textbox', { name: 'Instructions for Orchestrator' }).isDisabled());
    assert(await page.getByRole('button', { name: 'Save schedule', exact: true }).isEnabled());
    await page.setViewportSize({ width: 390, height: 844 });
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), 'Commands page overflows mobile viewport');
    await page.screenshot({ path: '/tmp/orchestrator-planning-mobile.png', fullPage: true });
    await page.locator('button.nav-item').filter({ hasText: 'Monitors' }).click();
    assert(await page.locator('.planning-status').isVisible(), 'Planning feedback disappeared on navigation');
    finishPlan();
    await page.locator('.success-note').waitFor();
    assert((await page.locator('.success-note').innerText()).includes('Hourly role monitor'));
    assert.equal(await page.locator('h1').innerText(), 'Monitors');
    assert.equal(commandRequests, 1);
    await page.locator('.planning-status').waitFor({ state: 'detached' });
    await page.getByRole('button', { name: 'Dismiss confirmation' }).click();
    await page.route('**/v1/commands/natural', r => r.fulfill({ status: 502, json: { error: 'Planning temporarily unavailable. Try again.' } }));
    await page.locator('button.nav-item').filter({ hasText: 'Commands' }).click();
    await page.getByRole('button', { name: 'Run Command', exact: true }).click();
    await page.getByRole('alert').waitFor();
    await page.getByRole('button', { name: 'Refresh', exact: true }).click();
    await page.waitForTimeout(3000);
    assert((await page.getByRole('alert').innerText()).includes('Planning temporarily unavailable'));
    assert(await page.getByRole('button', { name: 'Run Command', exact: true }).isEnabled());
    await page.getByRole('button', { name: 'Dismiss error', exact: true }).click();
    assert.equal(await page.getByRole('alert').count(), 0);
    assert.deepEqual(errors, []);
    console.log('PASS: connected planning feedback, independent forms, navigation, success, and retryable errors.');

    const now = Date.now();
    const timestamp = offset => new Date(now + offset).toISOString();
    const olderRole = { id: 'older-role', company: 'Example', title: 'Graduate Engineer', url: 'https://example.com/jobs/older', source: 'ashby', dedupe_key: 'older', first_seen_at: timestamp(-86400000), last_seen_at: timestamp(0), match_score: 90 };
    const newerRole = { ...olderRole, id: 'newer-role', title: 'New Grad Software Engineer', url: 'https://example.com/jobs/newer', first_seen_at: timestamp(-60000), match_score: 30 };
    let appliedRole = false;
    await page.route('**/v1/postings**', r => {
      if (r.request().method() === 'PATCH') { appliedRole = true; return r.fulfill({ json: { ...newerRole, applied_at: timestamp(0) } }); }
      return r.fulfill({ json: { postings: appliedRole ? [olderRole] : [olderRole, newerRole] } });
    });
    const successfulCheck = { id: 'successful-check', name: 'Role check', type: 'jobs.monitor.new_grad', status: 'succeeded', created_at: timestamp(-600000), updated_at: timestamp(-300000), finished_at: timestamp(-300000), attempts: 1, max_attempts: 2 };
    const failedCheck = { ...successfulCheck, id: 'failed-check', name: 'Failed source check', status: 'failed' };
    await page.route('**/v1/jobs', r => r.fulfill({ json: { jobs: [successfulCheck, failedCheck] } }));
    const monitor = { id: 'active-monitor', name: 'Hourly monitor', job_type: 'jobs.monitor.new_grad', enabled: true, interval_seconds: 3600, next_run_at: timestamp(3600000), last_job_id: 'successful-check', created_at: timestamp(-86400000), updated_at: timestamp(0), max_attempts: 2 };
    await page.route('**/v1/workflows', r => r.fulfill({ json: { workflows: [monitor, { ...monitor, id: 'paused-monitor', name: 'Paused monitor', enabled: false, next_run_at: timestamp(-3600000), last_job_id: 'failed-check' }] } }));
    await page.reload();
    await page.locator('.match-card').first().waitFor();
    assert.equal(await page.locator('.match-card h3').first().innerText(), 'New Grad Software Engineer', 'Newest match should be first, regardless of score');
    assert((await page.locator('.monitor-summary').innerText()).includes('In 1 hour'), 'Paused schedules must not determine the next check');
    assert.equal(await page.locator('.monitor-summary strong').first().innerText(), '1');
    assert(await page.getByRole('button', { name: 'Paused monitor — view failed check' }).isVisible());
    assert.equal(await page.locator('.runtime-details').getAttribute('open'), null);
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), 'Match overview overflows mobile viewport');
    await page.screenshot({ path: '/tmp/orchestrator-matches-mobile.png', fullPage: true });
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.screenshot({ path: '/tmp/orchestrator-matches-desktop.png', fullPage: true });
    await page.getByRole('button', { name: 'Mark New Grad Software Engineer at Example as applied' }).click();
    await page.waitForFunction(() => document.querySelectorAll('.match-card').length === 1);
    assert.equal(await page.locator('.match-card h3').innerText(), 'Graduate Engineer');
    await page.getByRole('button', { name: 'View all matches', exact: true }).click();
    assert.equal(await page.locator('h1').innerText(), 'Postings');
    assert.deepEqual(errors, []);
    console.log('PASS: match overview ordering, active schedules, failed checks, application updates, and mobile layout.');

    invalidMetrics = true;
    await page.reload();
    await page.getByRole('button', { name: 'Reload workspace', exact: true }).waitFor();
    console.log('PASS: unexpected rendering errors show recovery UI instead of a blank page.');
  } finally {
    if (browser) await browser.close();
    await new Promise(resolve => server.close(resolve));
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
