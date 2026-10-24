import { expect, mock, test } from 'claude-code/testing'

const sessionId = '10000000-0000-4000-8000-000000000001'
const rowId = '20000000-0000-4000-8000-000000000001'

for (const surface of ['terminal', 'desktop']) {
  test(`native loader registers and revokes the ${surface} observer`, async ($, on) => {
    mock.clock(on)
    const config = JSON.stringify({
      url: 'http://127.0.0.1:7438/claude-mod/v1/',
      tokenFile: '/fixture/token',
      sources: ['claude-code', 'claude-desktop'],
    })
    const token = 'b'.repeat(64)
    const requests = []
    let finishPush
    const pushed = new Promise(resolve => { finishPush = resolve })
    on('fs.stat', ($, e) => ({ value: {
      kind: 'file', size: e.path.endsWith('filo-config.json') ? config.length : token.length,
      isLink: false, mtimeMs: 1, realPath: e.path,
    } }))
    on('fs.read', ($, e) => ({ value: e.path.endsWith('filo-config.json') ? config : token }))
    on('session.id', () => ({ value: sessionId }))
    on('session.surfaces', () => ({ value: [surface] }))
    on('session.version', () => ({ value: { version: 'native-test' } }))
    on('session.cwd', () => ({ value: '/fixture' }))
    on('session.model', () => ({ value: 'native-model' }))
    on('session.usage', () => ({ value: { context: { tokens: 12, window: 100 }, rateLimits: [] } }))
    on('http.fetch', ($, e) => {
      const action = e.url.split('/').at(-1)
      const body = JSON.parse(e.init.body)
      expect(e.init.headers.Authorization).toBe('Bearer ' + token)
      requests.push({ action, body })
      const response = { readOnly: true }
      if (action === 'register') response.nonce = 'a'.repeat(43)
      if (action === 'push') {
        response.sequence = body.sequence
        finishPush()
      }
      return { value: { ok: true, status: 200, headers: {}, text: JSON.stringify(response) } }
    })
    on('session.start', () => ({ cwd: '/fixture' }))
    on('session.end', () => ({ sessionId }))

    await $.session.start({ surface, isInteractive: true, cwd: '/fixture' })
    await pushed
    // Observation and best-effort revocation are detached from native hooks.
    for (let spin = 0; spin < 100; spin++) await Promise.resolve()
    expect(requests.map(request => request.action)).toEqual(['register', 'pull', 'push'])
    expect(requests[0].body.origin).toBe(surface)
    expect(requests[0].body.sessionId).toBe(sessionId)
    expect(requests[2].body.events).toEqual([
      { kind: 'busy' },
      { kind: 'snapshot', canStop: true, canInput: true, canSettings: true, model: 'native-model', contextTokens: 12, contextWindow: 100 },
    ])
    await $.session.end({ sessionId, reason: 'prompt_input_exit', resume: { sessionId } })
    expect(requests.at(-1).action).toBe('revoke')
    expect(requests.at(-1).body.sessionId).toBe(sessionId)
  })
}

const policy = {
  name: 'fixture-security-policy',
  tier: 'prepend',
  register(on) {
    on('session.append', { door: ['attachment', 'hook-context', 'compaction'] }, async ($, e, next) => next.to(e, 'append'))
  },
}

test('managed context bypasses the observer while prompts and responses retain native storage', { plugins: [policy] }, async ($, on) => {
  let identityReads = 0
  const session = mock.session(on)
  on('session.id', () => { identityReads += 1; return { value: sessionId } })
  for (const door of ['attachment', 'hook-context', 'compaction', 'notice', 'prompt', 'response']) {
    const event = { uuid: rowId, door, message: { type: door === 'response' ? 'assistant' : 'user', content: [{ type: 'text', text: 'native text' }] } }
    const result = await $.session.append(event)
    expect(result).toEqual({ uuid: rowId, message: event.message })
  }
  expect(session.appended().length).toBe(6)
  expect(identityReads).toBe(0)
})

for (const failure of ['throw', 'hang']) {
  test(`native storage continues when private config ${failure}s`, async ($, on) => {
    mock.clock(on)
    const session = mock.session(on)
    on('fs.stat', () => {
      if (failure === 'throw') throw new Error('private file unavailable')
      return new Promise(() => {})
    })
    on('session.start', () => ({ cwd: '/fixture' }))
    await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/fixture' })
    const event = { uuid: rowId, door: 'response', message: { type: 'assistant', content: [{ type: 'text', text: 'native unchanged' }] } }
    const result = await $.session.append(event)
    expect(result).toEqual({ uuid: rowId, message: event.message })
    expect(session.appended().length).toBe(1)
  })
}
