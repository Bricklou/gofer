const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const vm = require('node:vm')

const page = fs.readFileSync(path.join(__dirname, '../assets/js/settings-page.js'), 'utf8')
const start = page.indexOf('function maybeGoAddContacts() {')
const end = page.indexOf('function syncWizardServiceContent(', start)
assert(start >= 0 && end > start)

for (const created of [false, true]) {
  const messages = []
  let syncs = 0
  const context = vm.createContext({
    addAccountCreated: () => created,
    syncAddContactStep() { syncs++ },
    addAccountTest: {saved(message) { messages.push(message) }},
    document: {getElementById: () => ({dataset: {mailService: 'jmap'}})},
  })
  vm.runInContext(page.slice(start, end) + '\nmaybeGoAddContacts()', context)
  assert.equal(messages.length, created ? 1 : 0)
  assert.equal(syncs, created ? 1 : 0)
  if (created) {
    assert.match(messages[0], /Mail is syncing now/)
    assert.doesNotMatch(messages[0], /unavailable|not available|isn't available/)
  }
}

// Run the actual dialog result handler with the JMAP service key rendered
// by SettingsAccountConnectionTestDialog and returned by the backend.
const settings = fs.readFileSync(path.join(__dirname, '../assets/js/settings.js'), 'utf8')
const resultStart = settings.indexOf('function accountTestDialog(')
const resultEnd = settings.indexOf('function _settingsEventElement(', resultStart)
assert(resultStart >= 0 && resultEnd > resultStart)
for (const success of [true, false]) {
  const status = {}, detail = {classList: {toggle() {}}}
  const section = {
    dataset: {accountTestService: 'jmap'},
    querySelector(selector) { return selector === '[data-account-test-service-status]' ? status : detail },
  }
  const retry = {disabled: true}
  const dialog = {
    dataset: {accountTestRun: '1'},
    querySelectorAll(selector) { return selector === '[data-account-test-service]' ? [section] : [retry] },
  }
  const context = vm.createContext({
    document: {getElementById: () => dialog},
    window: {setTimeout(callback) { callback() }},
    event: {detail: {
      elt: {getAttribute: () => 'test-account-acc-jmap'},
      xhr: {status: 200, responseText: JSON.stringify({results: [{service: 'jmap', success,
        message: success ? 'Connected to the JMAP server' : '', error: success ? '' : 'The server refused the login'}]})},
    }},
  })
  vm.runInContext(settings.slice(resultStart, resultEnd) + '\nhandleAccountTestResult(event)', context)
  assert.equal(section.dataset.state, success ? 'success' : 'error')
  assert.equal(status.textContent, success ? 'Connected to the JMAP server' : 'The server refused the login')
  assert.equal(retry.disabled, false)
}
console.log('JMAP settings feedback checks passed')
