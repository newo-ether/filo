// Filo official Claude Code Mod. No Node.js, npm or external runtime.
import { desktopMetadata, desktopRename } from './desktop.mjs'
// API evidence: https://code.claude.com/docs/en/plugins/mods/reference
const MAX_EVENTS = 96
const MAX_QUEUE_CHARS = 8 * 1024 * 1024
const MAX_TOOL_BYTES = 384 * 1024
const MAX_TEXT_BYTES = 24 * 1024
const BRIDGE_TIMEOUT_MS = 750
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

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
  const root = $.plugin.root
  if (!localAbsolutePath(root)) return
  const text = await readPrivateFile($, root.replace(/[\\/]$/, '') + '/filo-config.json', 8192)
  const value = JSON.parse(text)
  if (!Array.isArray(value.sources) || value.sources.length < 1 || value.sources.length > 2 ||
      value.sources.some(s => s !== 'claude-code' && s !== 'claude-desktop') ||
      new Set(value.sources).size !== value.sources.length) return
  const endpoint = new URL(value.url)
  if (endpoint.protocol !== 'http:' || endpoint.hostname !== '127.0.0.1' ||
      !endpoint.port || endpoint.username || endpoint.password || endpoint.search || endpoint.hash ||
      endpoint.pathname !== '/claude-mod/v1/') return
  if (!localAbsolutePath(value.tokenFile)) return
  const token = (await readPrivateFile($, value.tokenFile, 128)).trim()
  if (!/^[a-f0-9]{64}$/i.test(token)) return
  return { url: endpoint.href, token, sources: value.sources }
}

async function post($, state, action, payload) {
  // The native HTTP API has no timeout option. Race its result against a native
  // timer; a timed-out exchange is never retried on the old registration.
  if (state.httpPending) throw new Error('Filo exchange still pending')
  let timer
  try {
    const deadline = new Promise((resolve, reject) => {
      timer = $.clock.after(BRIDGE_TIMEOUT_MS, () => reject(new Error('Filo bridge timeout')))
    })
    state.httpPending = true
    let exchange
    try {
      exchange = $.http.fetch(state.config.url + action, {
        method: 'POST',
        headers: { Authorization: 'Bearer ' + state.config.token, 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      })
    } catch (error) {
      state.httpPending = false
      throw error
    }
    // The host cannot cancel HTTP. Keep at most one physical exchange alive,
    // including after timeout; late replies cannot deliver native controls.
    const response = await Promise.race([deadline, Promise.resolve(exchange).then(
      value => { state.httpPending = false; return value },
      error => { state.httpPending = false; throw error },
    )])
    if (!response?.ok || typeof response.text !== 'string' || response.text.length > 8192) throw new Error('Filo bridge unavailable')
    const value = JSON.parse(response.text)
    if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('Invalid Filo response')
    return value
  } finally {
    try { timer?.cancel() } catch {}
  }
}

function clearLocal(state, keepMetadata = false) {
  state.generation += 1
  state.binding = undefined
  state.bindingOrigin = undefined
  state.discoveryAttempt = undefined
  state.sequence = 0
  state.queue = []
  state.queueChars = 0
  state.dropped = false
  state.truncated = false
  state.activeTurn = undefined
  state.turnRevision = (state.turnRevision ?? 0) + 1
  state.pendingStart = undefined
  state.nativeStart = undefined
  state.commandID = undefined
  state.observedStep = undefined
  state.inputObservation = undefined
  state.nextSettings = undefined
  state.streamProjection = false
  state.toolBlocks = new Map()
  state.inflight = 0
  state.pendingMetadata = undefined
  if (!keepMetadata) state.knownMetadata = undefined
}

function enqueue(state, event) {
  if (!state.binding || state.terminal) return
  const size = JSON.stringify(event).length
  if (event.kind === 'block') {
    const b = event.block
    const index = state.queue.findLastIndex((item, at) => at >= state.inflight && item.kind === 'block' &&
      item.turnId === event.turnId && item.block.step === b.step && item.block.index === b.index)
    if (index >= 0) {
      const delta = size - JSON.stringify(state.queue[index]).length
      if (state.queueChars + delta <= MAX_QUEUE_CHARS) {
        state.queue[index] = event
        state.queueChars += delta
        return true
      }
    }
  }
  if (state.queue.length >= MAX_EVENTS || state.queueChars + size > MAX_QUEUE_CHARS) {
    state.dropped = true
    return
  }
  state.queue.push(event)
  state.queueChars += size
  return true
}

async function revoke($, state) {
  const previous = state.binding
  clearLocal(state)
  if (previous && state.config) {
    // Best-effort asynchronous revocation. Network failure cannot hold exit,
    // clear or resume; the authenticated lease also expires after its TTL.
    void post($, state, 'revoke', previous).catch(() => {})
  }
}

async function selectedOrigin($, state) {
  const surfaces = await $.session.surfaces()
  // This roster describes current clients, not immutable process provenance.
  // A mixed roster cannot identify one selected integration safely.
  if (!Array.isArray(surfaces) || surfaces.length !== 1) return
  const origin = surfaces[0]
  if (origin !== 'terminal' && origin !== 'desktop') return
  if (!state.config.sources.includes(origin === 'desktop' ? 'claude-desktop' : 'claude-code')) return
  return origin
}

async function matchesBinding($, state, current, stamp) {
  if (state.binding !== current || stamp !== state.generation || state.terminal) return false
  const origin = await selectedOrigin($, state)
  const nativeID = await $.session.id()
  if (state.binding !== current || stamp !== state.generation || state.terminal) return false
  if (nativeID !== current.sessionId || origin !== state.bindingOrigin) {
    await revoke($, state)
    return false
  }
  return true
}

async function registerSession($, state, nativeID, origin) {
  const stamp = state.generation
  const version = await $.session.version()
  const cwd = await $.session.cwd()
  if (stamp !== state.generation || state.terminal || await $.session.id() !== nativeID ||
      await selectedOrigin($, state) !== origin) return
  const response = await post($, state, 'register', {
    sessionId: nativeID, instance: state.instance, cwd, origin,
    version: version.version,
  })
  if (stamp !== state.generation || state.terminal || await $.session.id() !== nativeID ||
      await selectedOrigin($, state) !== origin) {
    try { await post($, state, 'revoke', { sessionId: nativeID, nonce: response.nonce }) } catch {}
    return
  }
  if (typeof response.nonce !== 'string' || !/^[A-Za-z0-9_-]{43}$/.test(response.nonce)) return
  state.binding = { sessionId: nativeID, nonce: response.nonce }
  state.bindingOrigin = origin
  state.sequence = 0
  // Native session may already be running. No idle state is inferred.
  enqueue(state, { kind: 'busy' })
  const early = state.pendingStart
  if (early?.sessionId === nativeID && early.origin === origin) {
    state.activeTurn = early.turnId
    enqueue(state, { kind: 'start', turnId: early.turnId, ...(early.text ? { text: early.text } : {}) })
  } else {
    state.observedStep = undefined
  }
  state.pendingStart = undefined
  const known = state.knownMetadata?.session_id === nativeID && state.knownMetadata.origin === origin ? state.knownMetadata : undefined
  const pending = state.pendingMetadata?.session_id === nativeID ? state.pendingMetadata : undefined
  state.pendingMetadata = undefined
  state.knownMetadata = known
  if (known || pending)
    void observeNativeTitle($, state, { ...known, ...pending }, state.binding, state.generation).catch(() => {})
}

function modelOptions(rows) {
  if (!Array.isArray(rows)) return
  const model = rows.find(row => row.key === 'model' && row.provider?.plugin === 'engine' && row.provider?.tier === 'core')
  const options = model?.options
  if (model?.kind !== 'choice' || !Array.isArray(options) || options.length > 32 ||
      new Set(options).size !== options.length || options.some(value => typeof value !== 'string' ||
        value.length === 0 || value.trim() !== value || new TextEncoder().encode(value).length > 256 ||
        /[\u0000-\u001f\u007f-\u009f]/.test(value))) return
  return options.slice()
}

async function observeRuntime($, state, current, stamp) {
  let model, usage, options
  try { model = state.observedStep?.model } catch {}
  // Read failures produce unknown fields, never a fabricated zero or a stale
  // reading. These calls do not request a token-count breakdown or model turn.
  try { model ??= await $.session.model() } catch {}
  if (!await matchesBinding($, state, current, stamp)) return
  try { usage = await $.session.usage() } catch {}
  if (!await matchesBinding($, state, current, stamp)) return
  try { options = modelOptions(await $.config.list()) } catch {}
  if (!await matchesBinding($, state, current, stamp)) return
  const snapshot = { kind: 'snapshot', canStop: true, canInput: true, canSettings: true }
  if (state.observedStep?.effort !== undefined) snapshot.effort = state.observedStep.effort
  if (options?.length) snapshot.modelOptions = options
  if (typeof model === 'string' && model.length > 0 && model.trim() === model &&
      new TextEncoder().encode(model).length <= 256 && !/[\u0000-\u001f\u007f-\u009f]/.test(model)) snapshot.model = model
  const context = usage?.context
  if (Number.isSafeInteger(context?.tokens) && context.tokens >= 0) snapshot.contextTokens = context.tokens
  if (Number.isSafeInteger(context?.window) && context.window > 0) snapshot.contextWindow = context.window
  enqueue(state, snapshot)
}

const DESKTOP_TOOL_NAMES = new Set([
  'list_sessions', 'get_session', 'list_events', 'set_session_title', 'archive_session', 'unarchive_session',
  'send_message', 'stop_session', 'set_session_model', 'set_session_effort', 'start_session', 'delete_session',
].map(name => 'mcp__ccd_session_mgmt__' + name))

async function observeDesktopTools($, state, current, stamp) {
  if (state.bindingOrigin !== 'desktop' || state.discoveryAttempt === current || state.discoveryPending) return
  state.discoveryAttempt = current
  let timer, status = 'unavailable', tools = []
  try {
    const deadline = new Promise((resolve, reject) => {
      timer = $.clock.after(BRIDGE_TIMEOUT_MS, () => reject(new Error('discovery timeout')))
    })
    state.discoveryPending = true
    let read
    try { read = $.tool.list() } catch (error) { state.discoveryPending = false; throw error }
    // A timed-out physical read remains the only read, even across registrations.
    const rows = await Promise.race([deadline, Promise.resolve(read).then(
      value => { state.discoveryPending = false; return value },
      error => { state.discoveryPending = false; throw error },
    )])
    status = 'invalid'
    if (Array.isArray(rows) && rows.length <= 512 && rows.every(row => row && typeof row.name === 'string' &&
        row.name.length <= 256 && typeof row.mcp === 'boolean')) {
      tools = rows.filter(row => row.mcp && DESKTOP_TOOL_NAMES.has(row.name)).map(row => row.name).sort()
      if (new Set(tools).size === tools.length) status = 'unverified'
      else tools = []
    }
  } catch (error) {
    if (error instanceof TypeError) return // Older hosts may lack the list method.
    if (String(error) === 'Error: discovery timeout') status = 'timeout'
  } finally {
    try { timer?.cancel() } catch {}
  }
  // Names and caller-supplied tool arguments cannot establish SDK provenance.
  if (await matchesBinding($, state, current, stamp))
    enqueue(state, { kind: 'discovery', discovery: { status, tools } })
}

function inputRefusal(error) {
  const message = String(error)
  if (message.includes('$.session.append: refused: main is a conversation no plugin may shape')) return 'append_read_only'
  if (message.includes('$.session.append:') && message.includes('keeps no conversation to append to')) return 'append_unbound'
  if (message.includes('$.session.append: refused: a uuid the engine did not mint')) return 'append_identity'
  if (message.includes('$.session.append: refused:')) return 'append_shape'
  return 'native_failure'
}

async function executeControl($, state, current, stamp, commands) {
  if (commands === undefined) return
  if (!Array.isArray(commands) || commands.length > 1) throw new Error('Invalid native control batch')
  for (const command of commands) {
    if (!await matchesBinding($, state, current, stamp)) return
    if (typeof command.id !== 'string' || !/^[A-Za-z0-9_-]{43}$/.test(command.id) ||
        !['stop', 'send', 'settings', 'desktop-metadata', 'desktop-rename'].includes(command.operation)) throw new Error('Invalid native control')
    // Mark before invoking. Neither bridge retries nor lifecycle changes replay it.
    if (state.commandID === command.id) continue
    state.commandID = command.id
    let ok = false
    let refusal, metadata, rename
    if (command.operation === 'desktop-metadata' || command.operation === 'desktop-rename') {
      const renaming = command.operation === 'desktop-rename'
      if (Object.keys(command).some(key => key !== 'id' && key !== 'operation' && key !== 'hostSessionId' && !(renaming && key === 'title')))
        throw new Error('Invalid Desktop diagnostic')
      if (state.bindingOrigin !== 'desktop') return
      const identity = { ...current, hostSessionId: command.hostSessionId }
      if (renaming) rename = await desktopRename($, state, identity, command.title, () => matchesBinding($, state, current, stamp))
      else metadata = await desktopMetadata($, state, identity)
      ok = renaming ? rename.status === 'applied' : metadata.status === 'matched'
    } else if (command.operation === 'stop') {
      if (typeof command.turnId !== 'string' || !/^[A-Za-z0-9_-]{1,256}$/.test(command.turnId)) throw new Error('Invalid turn')
      if (state.activeTurn === command.turnId) {
        try { await $.turn.abort({ turnId: command.turnId }); ok = true } catch {}
      }
    } else if (command.operation === 'settings') {
      const settings = command.settings
      if (!settings || Object.keys(settings).some(key => key !== 'model' && key !== 'effort') ||
          settings.model !== undefined && (typeof settings.model !== 'string' || !settings.model.length || settings.model.length > 256) ||
          settings.effort !== undefined && !['low', 'medium', 'high', 'xhigh', 'max'].includes(settings.effort)) throw new Error('Invalid settings')
      // Apply session-local settings from the next main turn; never write native config.
      state.nextSettings = { ...state.nextSettings, ...settings,
        afterTurn: state.nativeStart?.turnId ?? state.activeTurn, id: command.id }
      ok = true
    } else {
      if (typeof command.text !== 'string' || !command.text.length || new TextEncoder().encode(command.text).length > MAX_TEXT_BYTES ||
          (command.turnId ?? undefined) !== state.activeTurn) throw new Error('Invalid input')
      state.inputObservation = { id: command.id, text: command.text }
      try {
        if (state.activeTurn) {
          const result = await $.session.append({ message: { type: 'user', isMeta: true, content: [{ type: 'text', text: command.text }] } })
          ok = UUID.test(result.uuid ?? '') && result.message?.type === 'user'
          if (!ok && typeof result.deny === 'string') refusal = inputRefusal(result.deny)
          if (ok && await matchesBinding($, state, current, stamp)) appendRow(state, result)
        } else {
          const result = await $.prompt.submit({ text: command.text, asUser: true })
          ok = result.drop === undefined
        }
      } catch (error) { refusal = inputRefusal(error) }
      if (!ok) state.inputObservation = undefined
    }
    if (!await matchesBinding($, state, current, stamp)) return
    await post($, state, 'result', { ...current, result: { id: command.id, ok, ...(metadata ? { metadata } : {}), ...(rename ? { rename } : {}), ...(refusal ? { refusal } : {}) } })
  }
}

async function pump($, state) {
  if (state.pumping || state.terminal || !state.config) return
  state.pumping = true
  let generation = state.generation
  try {
    const origin = await selectedOrigin($, state)
    const nativeID = await $.session.id()
    if (generation !== state.generation || state.terminal) return
    if (state.knownMetadata && (state.knownMetadata.session_id !== nativeID || state.knownMetadata.origin !== origin))
      state.knownMetadata = undefined
    if (state.binding && (state.binding.sessionId !== nativeID || state.bindingOrigin !== origin)) {
      // revoke clears synchronously. Own exactly that generation increment;
      // external lifecycle changes during the await must stop this pump.
      const revokedGeneration = generation + 1
      await revoke($, state)
      if (state.generation !== revokedGeneration || state.terminal) return
      generation = revokedGeneration
    }
    if (!origin || !UUID.test(nativeID) || nativeID === state.endedSession) return
    state.endedSession = undefined
    if (!state.binding) await registerSession($, state, nativeID, origin)
    if (!state.binding) return
    const current = state.binding
    const stamp = state.generation
    // Revalidate session identity and current surface across asynchronous work.
    if (!await matchesBinding($, state, current, stamp)) return
    void observeDesktopTools($, state, current, stamp).catch(() => {})
    const pulled = await post($, state, 'pull', current)
    if (!await matchesBinding($, state, current, stamp)) return
    await executeControl($, state, current, stamp, pulled.commands)
    if (!await matchesBinding($, state, current, stamp)) return
    await observeRuntime($, state, current, stamp)
    if (!await matchesBinding($, state, current, stamp)) return
    if (state.truncated && state.queue.length < MAX_EVENTS) {
      enqueue(state, { kind: 'truncated' })
      state.truncated = false
    }
    if (state.dropped && state.queue.length < MAX_EVENTS) {
      state.queue.push({ kind: 'gap', dropped: true })
      state.dropped = false
    }
    if (!state.queue.length) return
    let bytes = 0
    const batch = []
    for (const event of state.queue) {
      const size = new TextEncoder().encode(JSON.stringify(event)).length
      if (batch.length === 24 || bytes + size > 3 * 1024 * 1024) break
      batch.push(event)
      bytes += size
    }
    state.inflight = batch.length
    const response = await post($, state, 'push', { ...current, sequence: state.sequence + 1, events: batch })
    if (!await matchesBinding($, state, current, stamp)) return
    if (response.sequence !== state.sequence + 1) throw new Error('Filo sequence mismatch')
    state.sequence += 1
    state.queue.splice(0, batch.length)
    state.queueChars = state.queue.reduce((sum, e) => sum + JSON.stringify(e).length, 0)
  } catch {
    // Never replay native control after an uncertain response. Observation
    // reconnects with a fresh binding and no pending commands. Only bounded
    // classic metadata survives transport loss; native lifecycle clears it.
    if (generation === state.generation) clearLocal(state, true)
  } finally { state.inflight = 0; state.pumping = false }
}

function observedInput(state, event, text) {
  const pending = state.inputObservation
  if (pending && typeof text === 'string' && text === pending.text) {
    event.observedCommand = pending.id
    event.text = pending.text
    state.inputObservation = undefined
  }
  return event
}

function appendRow(state, result) {
  const row = result.message
  if (!UUID.test(result.uuid ?? '') || row?.type !== 'user' && row?.type !== 'assistant') return
  if (row.isMeta && !(row.type === 'user' && state.inputObservation)) return
  // Model text is canonical within the current native step. A later step or
  // turn without text chunks must still retain its native append fallback.
  if (row.type === 'assistant' && state.streamProjection) return
  // Only the native returned row can confirm input, with full text and its UUID.
  const text = (row.content ?? []).filter(b => b.type === 'text' && typeof b.text === 'string').map(b => b.text).join('\n')
  if (!text) return
  if (new TextEncoder().encode(text).length > MAX_TEXT_BYTES) {
    enqueue(state, { kind: 'truncated' })
    return
  }
  enqueue(state, row.type === 'user' ? observedInput(state, { kind: 'row', uuid: result.uuid, role: row.type, text }, text) :
    { kind: 'row', uuid: result.uuid, role: row.type, text })
}

async function initialize($, state, stamp) {
  try {
    const config = await loadConfig($)
    if (stamp !== state.generation || state.terminal) return
    state.config = config
    if (config) {
      const early = state.nativeStart
      if (early) void observeStart($, state, early, stamp, state.turnRevision).catch(() => {})
      void pump($, state).catch(() => {})
      state.timer = $.clock.every(1000, () => pump($, state).catch(() => {}))
    }
  } catch {}
}

async function observeStart($, state, e, stamp, revision) {
  try {
    if (e.agentId || !state.config || state.terminal) return
    const nativeID = await $.session.id()
    const origin = await selectedOrigin($, state)
    if (stamp !== state.generation || revision !== state.turnRevision || state.terminal || !origin || !UUID.test(nativeID)) return
    state.observedStep = undefined
    if (state.binding?.sessionId === nativeID && state.bindingOrigin === origin) {
      state.activeTurn = e.turnId
      enqueue(state, observedInput(state, { kind: 'start', turnId: e.turnId,
        ...(typeof e.text === 'string' && e.text.trim() ? { text: e.text.slice(0, 4096) } : {}) }, e.text))
    } else if (!state.binding) {
      state.pendingStart = { sessionId: nativeID, origin, turnId: e.turnId,
        text: typeof e.text === 'string' ? e.text.slice(0, 4096) : undefined }
    }
  } catch {}
}

async function observeStep($, state, e, request, current, stamp, revision, selected, trace) {
  try {
    if (e.agentId || !state.config || state.terminal) return
    let matches = false
    if (current) {
      matches = await matchesBinding($, state, current, stamp) && state.activeTurn === e.turnId
    } else {
      const early = state.pendingStart
      if (early?.turnId === e.turnId) {
        const nativeID = await $.session.id()
        const origin = await selectedOrigin($, state)
        matches = nativeID === early.sessionId && origin === early.origin &&
          ((!state.binding && state.pendingStart === early) ||
            (state.binding?.sessionId === nativeID && state.bindingOrigin === origin && state.activeTurn === e.turnId))
      }
    }
    if (!matches || stamp !== state.generation || revision !== state.turnRevision || state.terminal) return
    const value = request.effort
    const effort = ['low', 'medium', 'high', 'xhigh', 'max'].includes(value) ? value :
      Number.isSafeInteger(value) && value >= 0 ? String(value) : undefined
    state.observedStep = { effort, model: request.model }
    const engine = trace?.findLast(link => link.plugin === 'engine' && link.tier === 'core' &&
      link.event === 'turn.step' && ['returned', 'passed'].includes(link.outcome))
    const actual = engine?.received
    if (current && actual?.turnId === e.turnId && actual.index === e.index && !actual.agentId &&
        engine.returned?.usage && typeof actual.model === 'string' && actual.model.length) {
      const value = actual.effort
      const effort = ['low', 'medium', 'high', 'xhigh', 'max'].includes(value) ? value :
        Number.isSafeInteger(value) && value >= 0 ? String(value) : undefined
      state.observedStep = { model: actual.model, effort }
      enqueue(state, { kind: 'request', turnId: e.turnId,
        observedCommand: selected && selected === state.nextSettings && e.turnId !== selected.afterTurn ? selected.id : undefined,
        model: actual.model, ...(effort !== undefined ? { effort } : {}) })
    }
  } catch {
    if (stamp === state.generation && revision === state.turnRevision) state.observedStep = undefined
  }
}

async function observeComplete($, state, e, stamp, revision) {
  try {
    if (e.agentId || !state.config || state.terminal) return
    const nativeID = await $.session.id()
    if (stamp !== state.generation || revision !== state.turnRevision || state.terminal) return
    if (state.pendingStart?.sessionId === nativeID && state.pendingStart.turnId === e.turnId) {
      state.pendingStart = undefined
      state.observedStep = undefined
    }
    if (state.binding?.sessionId === nativeID) {
      if (state.activeTurn === e.turnId) state.activeTurn = undefined
      enqueue(state, { kind: 'complete', turnId: e.turnId, aborted: e.isAborted === true })
    }
  } catch {}
}

async function observeAppend($, state, e, result, current, stamp) {
  try {
    if (!current || e.agentId || !UUID.test(result.uuid) || !await matchesBinding($, state, current, stamp)) return
    appendRow(state, result)
  } catch {}
}

function boundedObservation(state, text) {
  let value = text.slice(0, MAX_TEXT_BYTES)
  while (new TextEncoder().encode(value).length > MAX_TEXT_BYTES) value = value.slice(0, -256)
  if (value.length !== text.length) state.truncated = true
  return value
}

function boundedTool(state, value, depth = 0) {
  const suffix = '\n[Filo preview truncated; full output remains in Claude.]'
  const encode = value => JSON.stringify(value)
  const bound = (value, budget, level) => {
    if (level > 64) { state.truncated = true; return suffix }
    if (typeof value === 'string') {
      if (new TextEncoder().encode(value).length <= budget) return value
      try { return encode(bound(JSON.parse(value), budget, level + 1)) } catch {}
      state.truncated = true
      let end = Math.min(value.length, budget)
      while (new TextEncoder().encode(value.slice(0, end)).length > budget) end -= 256
      if (end > 0 && /[\uD800-\uDBFF]/.test(value[end - 1])) end--
      return value.slice(0, Math.max(0, end)) + suffix
    }
    if (Array.isArray(value)) {
      const result = value.slice(0, 4096).map(item => bound(item, budget, level + 1))
      if (value.length > 4096) { state.truncated = true; result.push(suffix) }
      return result
    }
    if (value && typeof value === 'object') {
      const entries = Object.entries(value)
      const result = Object.fromEntries(entries.slice(0, 4096).map(([key, item]) => [key, bound(item, budget, level + 1)]))
      if (entries.length > 4096) { state.truncated = true; result._filo_truncated = suffix }
      return result
    }
    return value
  }
  // Parse complete serialized values before reducing any leaf string.
  if (typeof value === 'string') { try { value = JSON.parse(value) } catch {} }
  for (let budget = 256 * 1024; budget >= 1; budget = Math.floor(budget / 2)) {
    const reduced = bound(value, budget, depth)
    const text = typeof reduced === 'string' ? reduced : encode(reduced)
    if (typeof text === 'string' && new TextEncoder().encode(text).length <= MAX_TOOL_BYTES) return text
  }
  state.truncated = true
  return encode({ _filo_truncated: suffix })
}

function observeChunk(state, e, chunk, current, stamp, blocks) {
  if (!current || current !== state.binding || stamp !== state.generation || state.terminal || e.agentId ||
      state.activeTurn !== e.turnId || !Number.isSafeInteger(e.index) || e.index < 0 || e.index > 1000000 ||
      !Number.isSafeInteger(chunk?.index) || chunk.index < 0 || chunk.index >= 128) return
  if (!['text', 'thinking', 'tool', 'input'].includes(chunk.kind)) return
  let block = blocks.get(chunk.index)
  if (!block) {
    if (blocks.size >= 128 || chunk.kind === 'input') return
    block = { step: e.index, index: chunk.index, type: chunk.kind === 'thinking' ? 'thought' : chunk.kind, state: 'running' }
    if (chunk.kind === 'tool') {
      if (typeof chunk.id !== 'string' || !/^[A-Za-z0-9_-]{1,256}$/.test(chunk.id) ||
          typeof chunk.name !== 'string' || !chunk.name.length || chunk.name.length > 256) return
      block.nativeId = chunk.id; block.toolName = chunk.name
    }
    blocks.set(chunk.index, block)
  }
  if ((chunk.kind === 'text' || chunk.kind === 'thinking') && typeof chunk.text === 'string')
    block.text = boundedObservation(state, (block.text ?? '') + chunk.text)
  if (chunk.kind === 'input' && block.type === 'tool' && typeof chunk.json === 'string')
    {
      const input = (block.arguments ?? '') + chunk.json
      if (new TextEncoder().encode(input).length <= MAX_TOOL_BYTES) block.arguments = input
      else { delete block.arguments; block.inputOverflow = true; state.truncated = true }
    }
  if (block.inputOverflow) delete block.arguments
  const { inputOverflow, ...wireBlock } = block
  const event = { kind: 'block', turnId: e.turnId, block: wireBlock }
  if (block.type === 'tool') {
    if (state.toolBlocks.size >= 128 && !state.toolBlocks.has(block.nativeId)) state.toolBlocks.delete(state.toolBlocks.keys().next().value)
    state.toolBlocks.set(block.nativeId, { ...event, block: { ...wireBlock } })
  }
  if (enqueue(state, event) && block.type === 'text') state.streamProjection = true
}

async function observeNativeTitle($, state, e, current, stamp) {
  if (stamp !== state.generation || state.terminal || !UUID.test(e.session_id ?? '')) return
  const metadata = { session_id: e.session_id }
  if (localAbsolutePath(e.transcript_path) && e.transcript_path.replace(/\\/g, '/').endsWith('/' + e.session_id + '.jsonl'))
    metadata.transcript_path = e.transcript_path
  if (typeof e.session_title === 'string' && new TextEncoder().encode(e.session_title).length <= 4096)
    metadata.session_title = e.session_title
  if (metadata.transcript_path === undefined && metadata.session_title === undefined) return
  if (!current) {
    state.pendingMetadata = metadata
    return
  }
  if (metadata.session_id !== current.sessionId || !await matchesBinding($, state, current, stamp)) return
  const known = state.knownMetadata?.session_id === current.sessionId && state.knownMetadata.origin === state.bindingOrigin ? state.knownMetadata : undefined
  state.knownMetadata = { ...known, ...metadata, origin: state.bindingOrigin }
  if (metadata.transcript_path !== undefined)
    enqueue(state, { kind: 'metadata', transcriptPath: metadata.transcript_path })
  if (metadata.session_title !== undefined)
    enqueue(state, { kind: 'title', title: metadata.session_title })
}

export function register(on) {
  const state = {
    config: undefined, instance: undefined, binding: undefined,
    bindingOrigin: undefined, sequence: 0, generation: 0, queue: [], queueChars: 0,
    pumping: false, terminal: false, timer: undefined, dropped: false, endedSession: undefined,
  }
  on('session.start', async ($, e, next) => {
    try {
      try { state.timer?.cancel() } catch {}
      state.timer = undefined
      state.terminal = false
      state.endedSession = undefined
      clearLocal(state)
      state.config = undefined
      state.instance = crypto.randomUUID()
      void initialize($, state, state.generation).catch(() => {})
    } catch {}
    return next(e)
  })

  on('session.end', async ($, e, next) => {
    try {
      state.terminal = e.reason !== 'clear' && e.reason !== 'resume'
      state.endedSession = e.sessionId
      // Clear local authority even if the timer handle is broken.
      void revoke($, state).catch(() => {})
      if (state.terminal) {
        try { state.timer?.cancel() } catch {}
      }
    } catch {}
    return next(e)
  })

  on('turn.start', async ($, e, next) => {
    try {
      const revision = e.agentId ? state.turnRevision : ++state.turnRevision
      if (!e.agentId) { state.nativeStart = e; state.streamProjection = false }
      void observeStart($, state, e, state.generation, revision).catch(() => {})
    } catch {}
    return next(e)
  })

  on('turn.step', async function* ($, e, next) {
    let request = e, current, stamp, revision, selected
    try {
      current = state.binding
      stamp = state.generation
      revision = state.turnRevision
      selected = state.nextSettings
      const mainTurn = !e.agentId && current && !state.terminal &&
        (state.nativeStart?.turnId ?? state.activeTurn) === e.turnId
      if (mainTurn) state.streamProjection = false
      // The native start hook precedes its asynchronous observation. Bind request
      // selection to that synchronous lifecycle evidence, without delaying the engine.
      // Lifecycle changes revoke authenticated settings before another request runs.
      if (mainTurn && state.config && selected && e.turnId !== selected.afterTurn) {
        const proposed = { ...e }
        if (selected.model !== undefined) proposed.model = selected.model
        if (selected.effort !== undefined) proposed.effort = selected.effort
        request = proposed
      }
    } catch {
      request = e
    }
    try {
      void observeStep($, state, e, request, current, stamp, revision, selected, undefined).catch(() => {})
    } catch {}
    // Native exceptions propagate as-is. Observe only bounded plain chunk data;
    // no host read, configuration or bridge request is awaited on this path.
    const source = next(request)
    const blocks = new Map()
    const observed = {
      [Symbol.asyncIterator]() { return this },
      async next(...args) {
        const item = await source.next(...args)
        if (!item.done) { try { observeChunk(state, e, item.value, current, stamp, blocks) } catch {} }
        return item
      },
      return(...args) { return source.return ? source.return(...args) : Promise.resolve({ done: true, value: args[0] }) },
      throw(...args) { return source.throw ? source.throw(...args) : Promise.reject(args[0]) },
    }
    const result = yield* observed
    try {
      void observeStep($, state, e, request, current, stamp, revision, selected, next.trace).catch(() => {})
    } catch {}
    return result
  })

  on('turn.complete', async ($, e, next) => {
    try {
      const revision = e.agentId ? state.turnRevision : ++state.turnRevision
      if (!e.agentId && state.nativeStart?.turnId === e.turnId) state.nativeStart = undefined
      void observeComplete($, state, e, state.generation, revision).catch(() => {})
    } catch {}
    return next(e)
  })

  on('tool.call', async ($, e, next) => {
    let block, current, stamp
    try {
      current = state.binding; stamp = state.generation
      block = !e.agentId && state.toolBlocks.get(e.tool_use_id)
      if (block && current && state.activeTurn === block.turnId) {
        const args = Object.fromEntries(Object.entries(e).filter(([key]) =>
          !['tool', 'tool_use_id', 'agentId', 'consent', 'requestMeta'].includes(key)))
        block.block.arguments = boundedTool(state, args)
        block.block.state = 'running'
        enqueue(state, { ...block, block: { ...block.block } })
      }
    } catch {}
    try {
      const result = await next(e)
      try {
        if (block && current === state.binding && stamp === state.generation && !state.terminal) {
          block.block.state = result?.isError || typeof result?.deny === 'string' ? 'failed' : 'succeeded'
          const observed = result?.text ?? result?.deny ??
            (result && typeof result === 'object' && (result.content !== undefined || result.output !== undefined) ? result : undefined)
          if (observed !== undefined) block.block.result = boundedTool(state, observed)
          enqueue(state, { ...block, block: { ...block.block } })
        }
      } catch {}
      return result
    } catch (error) {
      try {
        if (block && current === state.binding && stamp === state.generation) {
          block.block.state = 'failed'
          enqueue(state, { ...block, block: { ...block.block } })
        }
      } catch {}
      throw error
    }
  }).catch(async ($, e, next) => next(e))

  on('classic.SessionStart', async ($, e, next) => {
    try { void observeNativeTitle($, state, e, state.binding, state.generation).catch(() => {}) } catch {}
    return next(e)
  }).catch(async ($, e, next) => next(e))
  on('classic.UserPromptSubmit', async ($, e, next) => {
    try { void observeNativeTitle($, state, e, state.binding, state.generation).catch(() => {}) } catch {}
    return next(e)
  }).catch(async ($, e, next) => next(e))

  // Only native prompts and responses belong to this text observation stream.
  // Managed attachments and hook context intentionally bypass user Mods.
  on('session.append', { door: ['prompt', 'response'] }, async ($, e, next) => {
    const result = await next(e)
    try {
      void observeAppend($, state, e, result, state.binding, state.generation).catch(() => {})
    } catch {}
    return result
  }).catch(async ($, e, next) => {
    // Native compiler-required fallback. The engine reuses an already dispatched
    // native result here; all Filo observation errors are handled locally above.
    return next(e)
  })
}
