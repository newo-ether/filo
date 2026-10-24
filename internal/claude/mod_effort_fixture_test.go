package claude

const effortModFixtureScript = `
const effortFixture=fixture()
await effortFixture.call('session.start')
await settle()
async function nativeStep(fixture, event) {
  const chunks=[{kind:'engine',ref:1},{kind:'text',text:'unchanged',index:0,ref:2}]
  const result={turnId:event.turnId,index:event.index,answer:'unchanged',toolUses:[],stopReason:'end_turn',usage:null}
  let calls=0
  const stream=fixture.hooks.get('turn.step')(fixture.$,event,async function* (input) {
    calls++
    assert.equal(input,event,'observation rewrote a native model request')
    yield* chunks
    return result
  })
  const forwarded=[]
  let final
  while(true) {
    const value=await stream.next()
    if(value.done) {final=value.value;break}
    forwarded.push(value.value)
  }
  assert.equal(calls,1,'observation dispatched a duplicate native model request')
  assert.deepEqual(forwarded,chunks,'native stream chunks changed')
  assert.equal(final,result,'native response result changed')
}
await effortFixture.call('turn.start',{turnId:'effort-turn'})
for (const [value,expected] of [['max','max'],['xhigh','xhigh'],[8192,'8192'],[0,'0'],[undefined,undefined],[-1,undefined],[1.5,undefined],['invalid',undefined]]) {
  await nativeStep(effortFixture,{turnId:'effort-turn',index:0,model:'native-model',effort:value,messageCount:1})
  await effortFixture.timers.at(-1).fn()
  assert.equal(lastRuntimeSnapshot(effortFixture).effort,expected)
}
await nativeStep(effortFixture,{turnId:'effort-turn',index:1,model:'native-model',effort:'high',messageCount:1})
await nativeStep(effortFixture,{turnId:'subagent-turn',index:0,model:'other-model',effort:'low',messageCount:1,agentId:'child'})
await nativeStep(effortFixture,{turnId:'stale-turn',index:0,model:'other-model',effort:'low',messageCount:1})
await effortFixture.timers.at(-1).fn()
assert.equal(lastRuntimeSnapshot(effortFixture).effort,'high','other native turn contaminated main request effort')
await effortFixture.call('turn.start',{turnId:'next-turn'})
await effortFixture.timers.at(-1).fn()
assert.equal(lastRuntimeSnapshot(effortFixture).effort,undefined,'new turn inherited the previous request effort')
await nativeStep(effortFixture,{turnId:'next-turn',index:0,model:'native-model',effort:'high',messageCount:1})
await effortFixture.call('session.end',{sessionId:sessionA,reason:'clear'})
effortFixture.setID(sessionB)
await effortFixture.timers.at(-1).fn()
assert.equal(lastRuntimeSnapshot(effortFixture).effort,undefined,'request effort crossed native session binding')
await effortFixture.call('turn.start',{turnId:'read-failure-turn'})
await nativeStep(effortFixture,{turnId:'read-failure-turn',index:0,model:'native-model',effort:'high'})
const originalSurfaces=effortFixture.$.session.surfaces
effortFixture.$.session.surfaces=async()=>{throw new Error('observation unavailable')}
await nativeStep(effortFixture,{turnId:'read-failure-turn',index:1,model:'native-model',effort:'low'})
effortFixture.$.session.surfaces=originalSurfaces
await effortFixture.timers.at(-1).fn()
assert.equal(lastRuntimeSnapshot(effortFixture).effort,undefined,'failed observation retained stale request effort')
effortFixture.$.session.surfaces=async()=>{
  effortFixture.$.session.surfaces=originalSurfaces
  await effortFixture.call('turn.start',{turnId:'replacement-turn'})
  return ['terminal']
}
await nativeStep(effortFixture,{turnId:'read-failure-turn',index:2,model:'native-model',effort:'max'})
effortFixture.$.session.surfaces=originalSurfaces
await effortFixture.timers.at(-1).fn()
assert.equal(lastRuntimeSnapshot(effortFixture).effort,undefined,'turn changed during binding read but accepted stale request effort')
const nativeFailure=new Error('native downstream failure')
let failedDispatches=0
const failedStream=effortFixture.hooks.get('turn.step')(effortFixture.$,
  {turnId:'replacement-turn',index:0,model:'native-model',effort:'high'},async function* () {
    failedDispatches++
    throw nativeFailure
  })
await assert.rejects(failedStream.next(),error=>error===nativeFailure)
assert.equal(failedDispatches,1,'observation retried a failed native request')
for (const transition of ['none', 'complete', 'surface', 'session', 'generation', 'new-turn']) {
  const earlyEffort=fixture()
  const originalFetch=earlyEffort.$.http.fetch
  let enterRegistration, finishRegistration
  const entered=new Promise(resolve=>{enterRegistration=resolve})
  const release=new Promise(resolve=>{finishRegistration=resolve})
  earlyEffort.$.http.fetch=async(url,request)=>{
    if(url.endsWith('/register')) {enterRegistration();await release}
    return originalFetch(url,request)
  }
  await earlyEffort.call('session.start')
  await entered
  await earlyEffort.call('turn.start',{turnId:'first-effort-turn'})
  await nativeStep(earlyEffort,{turnId:'first-effort-turn',index:0,model:'native-model',effort:'max'})
  await nativeStep(earlyEffort,{turnId:'child-turn',index:0,effort:'low',agentId:'child'})
  if(transition==='complete') await earlyEffort.call('turn.complete',{turnId:'first-effort-turn'})
  if(transition==='surface') earlyEffort.setSurfaces(['desktop'])
  if(transition==='session') earlyEffort.setID(sessionB)
  if(transition==='generation') await earlyEffort.call('session.end',{sessionId:sessionA,reason:'clear'})
  if(transition==='new-turn') await earlyEffort.call('turn.start',{turnId:'replacement-effort-turn'})
  finishRegistration()
  await settle()
  await earlyEffort.timers.at(-1).fn()
  assert.equal(lastRuntimeSnapshot(earlyEffort)?.effort,transition==='none'?'max':undefined,
    'early request effort was lost or crossed a completed/changed native identity: '+transition)
}
console.log('official Mod effort observation fixtures passed')
`
