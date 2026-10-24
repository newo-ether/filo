package claude

const controlModFixtureScript = `
for (const surface of ['terminal','desktop']) {
  let command
  const stopped=[]
  const controlled=fixture({surfaces:[surface],response:(action,body,response)=>
    action==='pull' && command ? {...response,commands:[command]} : response})
  controlled.$.turn={abort:async value=>{
    stopped.push(value)
    await controlled.call('turn.complete',{turnId:value.turnId,isAborted:true})
  }}
  await controlled.call('session.start')
  await settle()
  await controlled.call('turn.start',{turnId:'exact-turn'})
  command={id:'c'.repeat(43),operation:'stop',turnId:'wrong-turn'}
  await controlled.timers.at(-1).fn()
  assert.equal(stopped.length,0,'wrong turn was aborted')
  assert.deepEqual(controlled.requests.findLast(r=>r.action==='result').body.result,{id:command.id,ok:false})
  command={id:'d'.repeat(43),operation:'stop',turnId:'exact-turn'}
  await controlled.timers.at(-1).fn()
  assert.deepEqual(stopped,[{turnId:'exact-turn'}])
  assert.deepEqual(controlled.requests.findLast(r=>r.action==='result').body.result,{id:command.id,ok:true})
  assert(controlled.requests.findLast(r=>r.action==='push').body.events.some(e=>e.kind==='complete'&&e.turnId==='exact-turn'))
  await controlled.timers.at(-1).fn()
  assert.equal(stopped.length,1,'duplicate command replayed native abort')
}
let lostBinding
const lost=fixture({response:(action,body,response)=>{
  if(action==='pull') {
    lostBinding ??= body.nonce
    if (body.nonce === lostBinding) return {...response,commands:[{id:'e'.repeat(43),operation:'stop',turnId:'lost-turn'}]}
  }
  if(action==='result') throw new Error('lost acknowledgement')
  return response
}})
let abortCount=0
lost.$.turn={abort:async()=>{abortCount++}}
await lost.call('session.start')
await settle()
await lost.call('turn.start',{turnId:'lost-turn'})
// The server discards commands with the revoked binding, including refusals.
lost.$.turn.abort=async()=>{abortCount++}
await lost.timers.at(-1).fn()
assert.equal(abortCount,0,'command from a revoked registration was replayed')
for (const transition of ['none', 'complete', 'surface', 'session', 'generation']) {
  const early = fixture()
  const originalFetch = early.$.http.fetch
  let enterRegistration, finishRegistration
  const entered = new Promise(resolve => { enterRegistration = resolve })
  const release = new Promise(resolve => { finishRegistration = resolve })
  early.$.http.fetch = async (url, request) => {
    if (url.endsWith('/register')) { enterRegistration(); await release }
    return originalFetch(url, request)
  }
  await early.call('session.start')
  await entered
  await early.call('turn.start', { turnId: 'first-native-turn' })
  await early.call('turn.start', { turnId: 'child-turn', agentId: 'child' })
  if (transition === 'complete') await early.call('turn.complete', { turnId: 'first-native-turn' })
  if (transition === 'surface') early.setSurfaces(['desktop'])
  if (transition === 'session') early.setID(sessionB)
  if (transition === 'generation') await early.call('session.end', { sessionId: sessionA, reason: 'clear' })
  finishRegistration()
  await settle()
  const starts = early.requests.filter(request => request.action === 'push').flatMap(request => request.body.events).filter(event => event.kind === 'start')
  assert.deepEqual(starts, transition === 'none' ? [{kind:'start',turnId:'first-native-turn'}] : [], 'early native turn crossed a changed or completed binding: ' + transition)
}
const failedLifecycle = fixture()
await failedLifecycle.call('session.start')
await settle()
failedLifecycle.$.session.id = async () => { throw new Error('unavailable observation') }
for (const name of ['turn.start', 'turn.complete']) {
  let continued = 0
  await failedLifecycle.hooks.get(name)(failedLifecycle.$, {turnId:'native-turn'}, async e => { continued++; return e })
  assert.equal(continued, 1, 'native lifecycle was blocked by observation failure')
}
const lateStart = fixture()
await lateStart.call('session.start')
await settle()
const readNativeID = lateStart.$.session.id
let finishStartRead, enterStartRead
const startReading = new Promise(resolve => { enterStartRead = resolve })
lateStart.$.session.id = async () => {
  lateStart.$.session.id = readNativeID
  enterStartRead()
  await new Promise(resolve => { finishStartRead = resolve })
  return readNativeID()
}
const startingTurn = lateStart.call('turn.start', {turnId:'already-completed'})
await startReading
await lateStart.call('turn.complete', {turnId:'already-completed'})
finishStartRead()
await startingTurn
await lateStart.timers.at(-1).fn()
assert.equal(lateStart.requests.filter(request=>request.action==='push').flatMap(request=>request.body.events).some(event=>event.kind==='start'&&event.turnId==='already-completed'), false, 'late start observation resurrected completed turn')
console.log('official Mod native control fixtures passed')
`
