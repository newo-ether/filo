// Fixed Desktop diagnostics. The caller retains registration and surface checks.
const DESKTOP_METADATA_TOOL = 'mcp__ccd_session_mgmt__get_session'
const DESKTOP_RENAME_TOOL = 'mcp__ccd_session_mgmt__set_session_title'
const DESKTOP_METADATA_UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const DESKTOP_METADATA_DEADLINE_MS = 2000
const DESKTOP_METADATA_MAX_BYTES = 64 * 1024

function expectedIdentity(current) {
  return DESKTOP_METADATA_UUID.test(current.sessionId ?? '') &&
    typeof current.hostSessionId === 'string' && current.hostSessionId.startsWith('local_') &&
    DESKTOP_METADATA_UUID.test(current.hostSessionId.slice(6))
}

function invalid(reason) { return { status: 'invalid', reason } }

function nativeRefused(result) {
  return result?.isError === true || typeof result?.deny === 'string'
}

function metadataReadback(result, current) {
  if (!result || typeof result !== 'object' || Array.isArray(result)) return invalid('result-shape')
  if (nativeRefused(result)) return { status: 'refused' }
  if (typeof result.text !== 'string') return invalid('text-missing')
  if (new TextEncoder().encode(result.text).length > DESKTOP_METADATA_MAX_BYTES) return invalid('text-too-large')
  let metadata
  try { metadata = JSON.parse(result.text) } catch { return invalid('text-json') }
  if (!metadata || typeof metadata !== 'object' || Array.isArray(metadata)) return invalid('metadata-shape')
  if (typeof metadata.sessionId !== 'string' || metadata.sessionId !== current.hostSessionId)
    return invalid('host-session-mismatch')
  const readback = { status: 'matched', sessionId: current.sessionId, hostSessionId: metadata.sessionId }
  if (metadata.title !== undefined && metadata.title !== null) {
    if (typeof metadata.title !== 'string' || new TextEncoder().encode(metadata.title).length > 4096)
      return invalid('title-shape')
    readback.title = metadata.title
  }
  return readback
}

export function desktopMetadata($, state, current) {
  if (!expectedIdentity(current)) return Promise.resolve(invalid('mapping-unavailable'))
  return desktopOperation($, state, async () => metadataReadback(
    await $.tool.call({ tool: DESKTOP_METADATA_TOOL, session_id: 'self' }), current), false)
}

export function desktopRename($, state, current, title, matches) {
  if (!expectedIdentity(current)) return Promise.resolve(invalid('mapping-unavailable'))
  if (typeof title !== 'string' || !title.trim() || title !== title.trim() || title.length > 200)
    return Promise.resolve({ status: 'invalid' })
  return desktopOperation($, state, async active => {
    if (!active() || !await matches() || !active()) return { status: 'unknown' }
    const before = metadataReadback(await $.tool.call({ tool: DESKTOP_METADATA_TOOL, session_id: 'self' }), current)
    if (!active() || !await matches() || !active()) return { status: 'unknown' }
    if (before.status !== 'matched') return before
    // The native tool owns title validation and all consent. Never supply _consent
    // or infer an applied mutation from its acknowledgement or proposed title.
    const renamed = await $.tool.call({ tool: DESKTOP_RENAME_TOOL, session_id: 'self', title })
    if (!active() || !await matches() || !active()) return { status: 'unknown' }
    if (nativeRefused(renamed)) return { status: 'refused' }
    if (!renamed || typeof renamed !== 'object' || Array.isArray(renamed)) return { status: 'unknown' }
    const after = metadataReadback(await $.tool.call({ tool: DESKTOP_METADATA_TOOL, session_id: 'self' }), current)
    if (!active() || !await matches() || !active()) return { status: 'unknown' }
    if (after.status !== 'matched' || after.title !== title) return { status: 'unknown' }
    return { ...after, status: 'applied' }
  }, true)
}

async function desktopOperation($, state, operation, mutation) {
  if (state.metadataPhysical) return { status: 'busy' }
  let timer, expired = false
  const slot = {}
  state.metadataPhysical = slot
  try {
    const deadline = new Promise(resolve => {
      timer = $.clock.after(DESKTOP_METADATA_DEADLINE_MS, () => {
        expired = true
        resolve({ status: mutation ? 'unknown' : 'timeout' })
      })
    })
    // A timeout cannot cancel native permission handling or the physical call.
    const physical = Promise.resolve().then(() => operation(() => !expired)).catch(() => (
      { status: mutation ? 'unknown' : 'unavailable' }
    )).finally(() => {
      if (state.metadataPhysical === slot) state.metadataPhysical = undefined
    })
    return await Promise.race([deadline, physical])
  } finally {
    try { timer?.cancel() } catch {}
  }
}
