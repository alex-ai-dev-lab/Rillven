/* Real browser saves against an isolated local database; no production access. */
const { spawn, spawnSync } = require('node:child_process')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const crypto = require('node:crypto')
const assert = require('node:assert/strict')
const { chromium } = require('playwright')
const { createDisposableEnvironment } = require('./disposable-environment.cjs')
const binary = process.env.QA_BINARY
const output = path.resolve(process.env.QA_OUT || 'qa-artifacts/model-pricing')
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'rillven-pricing-'))
const baseURL = 'http://127.0.0.1:39127'
const report = { checks: [], errors: [] }
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms))
let child, browser, log, page
;(async () => {
  assert(binary && fs.existsSync(binary), 'Set QA_BINARY to the local build')
  fs.mkdirSync(output, { recursive: true })
  const env = { ...createDisposableEnvironment(process.env, temporary, crypto.randomBytes(32).toString('hex')), PORT: '39127' }
  env.RILLVEN_STATE_KEYS = JSON.stringify({ qa: crypto.randomBytes(32).toString('base64') })
  env.RILLVEN_STATE_ACTIVE_KEY = 'qa'
  const migration = spawnSync(binary, ['migrate', '--up'], { cwd: temporary, env, encoding: 'utf8', windowsHide: true })
  assert.equal(migration.status, 0, migration.stderr)
  log = fs.openSync(path.join(temporary, 'backend.log'), 'w')
  child = spawn(binary, [], { cwd: temporary, env, stdio: ['ignore', log, log], windowsHide: true })
  for (let n = 0; n < 100; n++) {
    try { if ((await fetch(baseURL + '/api/setup')).ok) break } catch {}
    assert.equal(child.exitCode, null, 'Local backend exited')
    await sleep(250)
  }
  browser = await chromium.launch({ executablePath: process.env.QA_CHROME || 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: true })
  const context = await browser.newContext({ baseURL, locale: 'en-US', viewport: { width: 1600, height: 900 }, reducedMotion: 'reduce' })
  await context.addInitScript(() => localStorage.setItem('i18nextLng', 'en'))
  page = await context.newPage()
  page.on('pageerror', error => report.errors.push(error.message))
  const json = async response => { const body = await response.json(); assert(response.ok() && body.success, JSON.stringify(body)); return body }
  const password = 'Qa!' + crypto.randomBytes(12).toString('hex')
  await json(await context.request.post('/api/setup', { data: { username: 'pricing_qa', password, confirmPassword: password, SelfUseModeEnabled: false, DemoSiteEnabled: false } }))
  await page.goto('/sign-in', { waitUntil: 'networkidle' })
  await page.getByRole('button', { name: 'Continue with password', exact: true }).click()
  await page.locator('input[name="username"]').fill('pricing_qa')
  await page.locator('input[name="password"]').fill(password)
  const login = page.waitForResponse(r => r.url().includes('/api/user/login') && r.request().method() === 'POST')
  await page.locator('form button[type="submit"]').click()
  const user = (await json(await login)).data
  await page.waitForURL(/\/dashboard/)
  const headers = { 'New-Api-User': String(user.id) }
  const readStatus = async () => (await json(await context.request.get('/api/status'))).data
  assert.equal((await readStatus()).system_name, 'Rillven')
  await page.waitForFunction(() => document.title.includes('Rillven'))
  await page.locator('a[href="https://github.com/alex-ai-dev-lab/rillven"]').first().waitFor()
  for (const name of ['New API', 'RenewAPI', 'QA Custom Gateway']) {
    await json(await context.request.put('/api/option/bulk', { headers, data: { options: { SystemName: name } } }))
    const expected = name === 'QA Custom Gateway' ? name : 'Rillven'
    assert.equal((await readStatus()).system_name, expected)
    const options = (await json(await context.request.get('/api/option/', { headers }))).data
    assert.equal(options.find(option => option.key === 'SystemName').value, expected)
    await page.reload({ waitUntil: 'networkidle' })
    await page.waitForFunction(name => document.title.includes(name), expected)
  }
  await json(await context.request.put('/api/option/bulk', { headers, data: { options: { SystemName: 'Rillven' } } }))
  await page.evaluate(() => {
    const cached = JSON.parse(localStorage.getItem('system-config-storage'))
    cached.state.config.systemName = 'RenewAPI'
    localStorage.setItem('system-config-storage', JSON.stringify(cached))
    const status = JSON.parse(localStorage.getItem('status'))
    status.system_name = 'RenewAPI'
    localStorage.setItem('status', JSON.stringify(status))
  })
  // Cached defaults must upgrade even before a successful status refresh.
  await page.route('**/api/status', route => route.fulfill({ status: 503, body: 'offline for cache check' }))
  await page.reload({ waitUntil: 'networkidle' })
  await page.waitForFunction(() => document.title.includes('Rillven'))
  assert.equal(await page.locator('meta[name="title"]').getAttribute('content'), 'Rillven')
  await page.unroute('**/api/status')
  report.checks.push('Rillven default, repository link, legacy defaults and cached name upgrade; custom name preserved')
  await json(await context.request.put('/api/option/bulk', { headers, data: { options: { ModelPrice: '{"qa-pricing":0.01,"qa-other":0.02}' } } }))
  const readPrices = async () => {
    const opts = (await json(await context.request.get('/api/option/', { headers }))).data
    return JSON.parse(opts.find(o => o.key === 'ModelPrice').value)
  }
  const open = async name => {
    await page.goto('/system-settings/billing/model-pricing', { waitUntil: 'networkidle' })
    await page.getByPlaceholder('Search models...', { exact: true }).fill(name)
    await page.getByRole('row').filter({ hasText: name }).first().click()
    await page.getByLabel('Fixed price', { exact: true }).waitFor()
  }
  const save = async () => {
    const response = page.waitForResponse(r => r.url().includes('/api/option/bulk') && r.request().method() === 'PUT')
    await page.getByRole('button', { name: 'Save model prices', exact: true }).first().click()
    await json(await response)
    await page.getByRole('button', { name: 'Save model prices', exact: true }).first().waitFor()
  }
  await open('qa-pricing')
  await page.getByLabel('Fixed price', { exact: true }).fill('0')
  await save()
  assert.equal((await readPrices())['qa-pricing'], 0)
  await open('qa-pricing')
  assert.equal(await page.getByLabel('Fixed price', { exact: true }).inputValue(), '0')
  report.checks.push('top save includes open editor and explicit zero persists after reload')
  await page.getByLabel('Model name', { exact: true }).fill('qa-renamed')
  await page.getByRole('button', { name: 'Export JSON', exact: true }).click()
  await save()
  const renamed = await readPrices()
  assert.equal(renamed['qa-renamed'], 0)
  assert.equal(renamed['qa-pricing'], undefined)
  assert.equal(renamed['qa-other'], 0.02)
  report.checks.push('rename, export staging and subsequent save preserve other prices')
  await open('qa-renamed')
  await page.getByLabel('Fixed price', { exact: true }).fill('0.123')
  await page.route('**/api/option/bulk', route => route.fulfill({ status: 500, contentType: 'application/json', body: '{"success":false,"message":"injected save failure"}' }))
  const rejected = page.waitForResponse(r => r.url().includes('/api/option/bulk'))
  await page.getByRole('button', { name: 'Save model prices', exact: true }).first().click()
  assert.equal((await rejected).status(), 500)
  await page.getByRole('button', { name: 'Save model prices', exact: true }).first().waitFor()
  assert.equal(await page.getByLabel('Fixed price', { exact: true }).inputValue(), '0.123')
  assert.equal((await readPrices())['qa-renamed'], 0)
  await page.unroute('**/api/option/bulk')
  await save()
  assert.equal((await readPrices())['qa-renamed'], 0.123)
  report.checks.push('HTTP 500 retains draft and retry persists the exact value')
  let writes = 0
  await page.route('**/api/option/bulk', async route => { writes++; await sleep(250); await route.continue() })
  await page.getByLabel('Fixed price', { exact: true }).fill('0.456')
  const doubleSave = page.waitForResponse(r => r.url().includes('/api/option/bulk'))
  await page.getByRole('button', { name: 'Save model prices', exact: true }).first().evaluate(button => { button.click(); button.click() })
  await json(await doubleSave)
  await sleep(400)
  assert.equal(writes, 1)
  assert.equal((await readPrices())['qa-renamed'], 0.456)
  await page.getByLabel('Model name', { exact: true }).fill('qa-other')
  await page.getByRole('button', { name: 'Save model prices', exact: true }).first().click()
  await page.getByText('A model with this name already exists', { exact: true }).waitFor()
  assert.equal(writes, 1, 'collisions must not send a save')
  assert.equal((await readPrices())['qa-other'], 0.02)
  await page.unroute('**/api/option/bulk')
  await page.getByLabel('Model name', { exact: true }).fill('qa-renamed')
  await page.getByLabel('Fixed price', { exact: true }).fill('0.789')
  await page.getByRole('button', { name: 'Switch to JSON', exact: true }).click()
  await page.getByRole('button', { name: 'Switch to Visual', exact: true }).click()
  await save()
  assert.equal((await readPrices())['qa-renamed'], 0.789)
  report.checks.push('double click sends one save; rename collision is rejected; edit-mode changes retain drafts')
  for (const viewport of [{width:1600,height:900},{width:1366,height:768},{width:1093,height:614},{width:390,height:844}]) {
    await page.setViewportSize(viewport)
    await open('qa-renamed')
    const footer = page.locator('form').filter({has:page.getByLabel('Model name',{exact:true})}).getByRole('button',{name:'Save model prices',exact:true})
    await footer.scrollIntoViewIfNeeded()
    const box = await footer.boundingBox()
    assert(box && box.width > 0 && box.height > 0 && box.x >= 0 && box.x + box.width <= viewport.width + 1 && box.y >= 0 && box.y + box.height <= viewport.height + 1, JSON.stringify({viewport,box}))
    await footer.click({trial:true})
    await page.screenshot({path:path.join(output,`pricing-${viewport.width}.png`)})
    report.checks.push(`editor footer reachable at ${viewport.width}x${viewport.height}`)
  }
  await page.setViewportSize({ width: 1600, height: 900 })
  for (const name of ['qa-identity-a', 'qa-identity-b']) {
    await json(await context.request.post('/api/channel/', { headers, data: { mode: 'single', channel: { type: 1, name, key: 'isolated-test-key', models: 'qa-model', group: 'default', base_url: 'http://127.0.0.1:1', setting: JSON.stringify({ client_identity_mode: 'per_channel' }) } } }))
  }
  const channels = (await json(await context.request.get('/api/channel/?p=1&page_size=10', { headers }))).data.items
  const a = channels.find(c => c.name === 'qa-identity-a')
  const b = channels.find(c => c.name === 'qa-identity-b')
  assert(a && b)
  const preview = async channel => (await json(await context.request.post(`/api/channel/${channel.id}/client_identity/preview`, { headers, data: {} }))).data
  const original = await preview(a)
  const other = await preview(b)
  assert.notEqual(original.slots[0].installation_id, other.slots[0].installation_id)
  await page.goto('/channels', { waitUntil: 'networkidle' })
  await page.getByRole('row').filter({ hasText: a.name }).getByRole('button', { name: 'Edit', exact: true }).click()
  const identityButton = page.getByRole('button', { name: 'Preview channel identity', exact: true })
  if (!(await identityButton.isVisible())) await page.getByRole('button', { name: /Advanced Settings/ }).last().click()
  await identityButton.click()
  const explicit = crypto.randomUUID()
  await page.getByLabel('Installation ID (empty uses a stable generated value)', { exact: true }).fill(explicit)
  const identitySave = page.waitForResponse(r => r.url().endsWith(`/api/channel/${a.id}/client_identity`) && r.request().method() === 'PUT')
  await page.getByRole('button', { name: 'Save channel identity', exact: true }).click()
  await json(await identitySave)
  assert.equal((await preview(a)).slots[0].installation_id, explicit)
  assert.equal((await preview(b)).slots[0].installation_id, other.slots[0].installation_id)
  const choose = async (label, name) => { await page.getByLabel(label, { exact: true }).click(); await page.getByRole('option', { name, exact: true }).click() }
  await choose('Outbound connection policy', 'Direct connection')
  await choose('Conversation recovery', 'Automatic conversation recovery')
  await choose('Response delivery', 'Buffer until the turn completes')
  await page.getByRole('switch', { name: 'Coordinate upstream account limits', exact: true }).check()
  const channelSave = page.waitForResponse(r => new URL(r.url()).pathname === `/api/channel/${a.id}/config` && r.request().method() === 'PUT')
  await page.getByRole('button', { name: 'Update Channel', exact: true }).click()
  await json(await channelSave)
  const stored = (await json(await context.request.get(`/api/channel/${a.id}`, { headers }))).data
  const setting = JSON.parse(stored.setting)
  assert.equal(setting.client_identity_mode, 'per_channel')
  assert.equal(setting.egress_policy, 'direct')
  assert.equal(setting.conversation_recovery, 'managed')
  assert.equal(setting.stream_commit_mode, 'atomic_turn')
  assert.equal(setting.upstream_coordination, true)
  report.checks.push('channel identity is independent, explicit ID saves, and recovery/egress/queue settings persist through the browser')
  assert.deepEqual(report.errors, [])
})().catch(async error => {
  report.errors.push(error.stack)
  if (page) { await page.screenshot({path:path.join(output,'failure.png')}); report.url=page.url(); report.body=(await page.locator('body').innerText()).slice(0,4000) }
  process.exitCode = 1
}).finally(async () => {
  if (browser) await browser.close()
  if (child && child.exitCode === null) { child.kill(); await Promise.race([new Promise(resolve => child.once('exit',resolve)),sleep(3000)]) }
  if (log !== undefined) fs.closeSync(log)
  fs.mkdirSync(output,{recursive:true})
  fs.writeFileSync(path.join(output,'report.json'),JSON.stringify(report,null,2))
  console.log(JSON.stringify(report,null,2))
})
