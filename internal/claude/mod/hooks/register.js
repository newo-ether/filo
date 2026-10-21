// Filo read-only official Claude Code Mod. No Node.js, npm or external runtime.
// API evidence: https://code.claude.com/docs/en/plugins/mods/reference
// session.append uuid is native. session.messages has no stable row IDs.
const MAX_EVENTS = 96
const MAX_QUEUE_CHARS = 128 * 1024
const MAX_TEXT_BYTES = 24 * 1024
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

export function register(on) {
  let config
  let instance = crypto.randomUUID()
  let binding
  let sequence = 0
  let generation = 0
  let queue = []
  let queueChars = 0
  let pumping = false
  let terminal = false
  let timer
  let dropped = false
  let endedSession

  async function readPrivateFile($, path, limit) {
    // Official fs.read has a hard 4 MiB bound but no per-call maxBytes option.
    // Check a regular non-symlink file before allocating; check it again after
    // reading to reject a replaced/expanded config. A same-user race remains
    // bounded by the native 4 MiB cap, not an invented maxBytes argument.
    const before = await $.fs.stat(path, { resolve: true })
    if (before.kind !== 'file' || before.isLink || !Number.isSafeInteger(before.size) ||
        before.size < 1 || before.size > limit || !before.realPath) throw new Error('Invalid private file')
    const text = await $.fs.read(path)
    const after = await $.fs.stat(path, { resolve: true })
    if (typeof text !== 'string' || new TextEncoder().encode(text).length > limit ||
        after.kind !== 'file' || after.isLink || after.size !== before.size ||
        after.mtimeMs !== before.mtimeMs || after.realPath !== before.realPath) throw new Error('Private file changed')
    return text
  }

  function localAbsolutePath(path) {
    return typeof path === 'string' && path.length <= 4096 && !path.includes('\u0000') &&
      /^(?:[A-Za-z]:[\\/]|\/(?!\/))/.test(path)
  }

  async function loadConfig($) {
    const path = await $.env.get('FILO_CLAUDE_CONFIG')
    if (!localAbsolutePath(path)) return
    const text = await readPrivateFile($, path, 8192)
    const value = JSON.parse(text)
    const endpoint = new URL(value.url)
    if (endpoint.protocol !== 'http:' || endpoint.hostname !== '127.0.0.1' ||
        !endpoint.port || endpoint.username || endpoint.password || endpoint.search || endpoint.hash ||
        endpoint.pathname !== '/claude-mod/v1/') return
    if (!localAbsolutePath(value.tokenFile)) return
    const token = (await readPrivateFile($, value.tokenFile, 128)).trim()
    if (!/^[a-f0-9]{64}$/i.test(token)) return
    return { url: endpoint.href, token }
  }

  async function post($, action, payload) {
    const response = await $.http.fetch(config.url + action, {
      method: 'POST',
      headers: { Authorization: 'Bearer ' + config.token, 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    })
    if (!response.ok || response.text.length > 8192) throw new Error('Filo bridge unavailable')
    return JSON.parse(response.text)
  }

  function clearLocal() {
    generation += 1
    binding = undefined
    sequence = 0
    queue = []
    queueChars = 0
    dropped = false
  }

  function enqueue(event) {
    if (!binding || terminal) return
    const size = JSON.stringify(event).length
    if (queue.length >= MAX_EVENTS || queueChars + size > MAX_QUEUE_CHARS) {
      dropped = true
      return
    }
    queue.push(event)
    queueChars += size
  }

  async function revoke($) {
    const previous = binding
    clearLocal()
    if (previous && config) {
      // Best-effort asynchronous revocation. Network failure cannot hold exit,
      // clear or resume; the authenticated lease also expires after its TTL.
      void post($, 'revoke', previous).catch(() => {})
    }
  }

  async function registerSession($, nativeID) {
    const surfaces = await $.session.surfaces()
    if (!Array.isArray(surfaces) || !surfaces.some(s => s === 'terminal' || s === 'desktop')) return
    const version = await $.session.version()
    const stamp = generation
    const response = await post($, 'register', {
      sessionId: nativeID, instance, cwd: await $.session.cwd(),
      origin: surfaces.includes('desktop') ? 'desktop' : 'terminal',
      version: version.version,
    })
    if (stamp !== generation || terminal || await $.session.id() !== nativeID) {
      try { await post($, 'revoke', { sessionId: nativeID, nonce: response.nonce }) } catch {}
      return
    }
    if (typeof response.nonce !== 'string' || !/^[A-Za-z0-9_-]{43}$/.test(response.nonce)) return
    binding = { sessionId: nativeID, nonce: response.nonce }
    sequence = 0
    // Native session may already be running. No idle state is inferred.
    enqueue({ kind: 'busy' })
  }

  async function pump($) {
    if (pumping || terminal || !config) return
    pumping = true
    try {
      const nativeID = await $.session.id()
      if (!UUID.test(nativeID) || nativeID === endedSession) return
      endedSession = undefined
      if (binding && binding.sessionId !== nativeID) await revoke($)
      if (!binding) await registerSession($, nativeID)
      if (!binding) return
      const current = binding
      const stamp = generation
      // Revalidate exact session identity after every asynchronous operation.
      if (await $.session.id() !== current.sessionId) { await revoke($); return }
      await post($, 'pull', current)
      if (binding !== current || stamp !== generation || await $.session.id() !== current.sessionId) return
      if (dropped && queue.length < MAX_EVENTS) {
        queue.push({ kind: 'gap', dropped: true })
        dropped = false
      }
      if (!queue.length) return
      let bytes = 0
      const batch = []
      for (const event of queue) {
        const size = new TextEncoder().encode(JSON.stringify(event)).length
        if (batch.length === 24 || bytes + size > 48 * 1024) break
        batch.push(event)
        bytes += size
      }
      const response = await post($, 'push', { ...current, sequence: sequence + 1, events: batch })
      if (binding !== current || stamp !== generation || await $.session.id() !== current.sessionId) return
      if (response.sequence !== sequence + 1) throw new Error('Filo sequence mismatch')
      sequence += 1
      queue.splice(0, batch.length)
      queueChars = queue.reduce((sum, e) => sum + JSON.stringify(e).length, 0)
    } catch {
      // Never replay a mutation. These are read-only observation batches only.
      // A lost/expired bridge binding is re-created on the next poll, with a new
      // canonical identity; no account continuity is assumed.
      clearLocal()
    } finally { pumping = false }
  }

  on('session.start', async ($, e, next) => {
    timer?.cancel()
    terminal = false
    endedSession = undefined
    clearLocal()
    instance = crypto.randomUUID()
    try { config = await loadConfig($) } catch { config = undefined }
    if (config) {
      // Bridge reachability never delays the native first prompt.
      void pump($)
      timer = $.clock.every(1000, () => pump($))
    }
    return next(e)
  })

  on('session.end', async ($, e, next) => {
    terminal = e.reason !== 'clear' && e.reason !== 'resume'
    endedSession = e.sessionId
    if (terminal) timer?.cancel()
    // /branch uses reason resume. The timer registers the new session even
    // though session.start is not emitted again after clear/resume/branch.
    await revoke($)
    return next(e)
  })

  on('turn.start', async ($, e, next) => {
    if (!e.agentId && binding && await $.session.id() === binding.sessionId) enqueue({ kind: 'start', turnId: e.turnId })
    return next(e)
  })

  on('turn.complete', async ($, e, next) => {
    if (!e.agentId && binding && await $.session.id() === binding.sessionId) {
      enqueue({ kind: 'complete', turnId: e.turnId, aborted: e.isAborted === true })
    }
    return next(e)
  })

  on('session.append', async ($, e, next) => {
    const current = binding
    const nativeID = await $.session.id()
    const result = await next(e)
    if (!current || binding !== current || e.agentId || nativeID !== current.sessionId ||
        await $.session.id() !== current.sessionId || !UUID.test(result.uuid)) return result
    const row = result.message
    if (row?.type !== 'user' && row?.type !== 'assistant') return result
    if (row.isMeta) return result
    // Preserve only native text rows for which the event supplies stable UUID.
    // Thinking/media/tool blocks are not mislabeled as complete text history.
    const text = (row.content ?? []).filter(b => b.type === 'text' && typeof b.text === 'string').map(b => b.text).join('\n')
    if (!text) return result
    if (new TextEncoder().encode(text).length > MAX_TEXT_BYTES) {
      enqueue({ kind: 'gap', dropped: true })
      return result
    }
    enqueue({ kind: 'row', uuid: result.uuid, role: row.type, text })
    return result
  })
}
