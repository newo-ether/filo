package claude

// These fixtures execute the shipped observer with native read APIs. The legacy
// fixtures omit these methods to cover unknown snapshots on graceful read failure.
const runtimeModFixtureScript = `
function lastRuntimeSnapshot(f) {
  return f.requests.filter(r => r.action === 'push').at(-1)?.body.events.findLast(e => e.kind === 'snapshot')
}
const runtime=fixture()
let currentModel='native-model', contextReading={tokens:1234,window:200000}
const usageArgs=[]
runtime.$.session.model=async()=>currentModel
runtime.$.session.usage=async(...args)=>{usageArgs.push(args);return {context:contextReading,rateLimits:[]}}
await runtime.call('session.start')
await settle()
assert.deepEqual(lastRuntimeSnapshot(runtime),{kind:'snapshot',canStop:true,canInput:true,canSettings:true,model:'native-model',contextTokens:1234,contextWindow:200000})
assert.deepEqual(usageArgs,[[]],'observer must not request a paid context breakdown')
currentModel='next-model'
contextReading={window:1000000}
await runtime.timers.at(-1).fn()
assert.deepEqual(lastRuntimeSnapshot(runtime),{kind:'snapshot',canStop:true,canInput:true,canSettings:true,model:'next-model',contextWindow:1000000},'unknown tokens must stay absent')
contextReading={tokens:0,window:1000000}
await runtime.timers.at(-1).fn()
assert.equal(lastRuntimeSnapshot(runtime).contextTokens,0,'native zero token count must be preserved')
runtime.$.session.model=async()=>{throw new Error('model unavailable')}
runtime.$.session.usage=async()=>{throw new Error('usage unavailable')}
await runtime.call('turn.start',{turnId:'runtime-error-turn'})
await runtime.timers.at(-1).fn()
assert.deepEqual(lastRuntimeSnapshot(runtime),{kind:'snapshot',canStop:true,canInput:true,canSettings:true},'failed reads must clear stale native values')
assert(runtime.requests.filter(r=>r.action==='push').at(-1).body.events.some(e=>e.kind==='start'),'read failure lost the native turn event')
assert.equal(runtime.requests.filter(r=>r.action==='register').length,1,'read errors must not discard the registration')
for (const [model,context,expected] of [
  ['',{},{}],
  [' native-model',{tokens:-1,window:0},{}],
  ['bad\nmodel',{tokens:1.5,window:2.5},{}],
  ['m'.repeat(257),{tokens:Number.MAX_SAFE_INTEGER+1,window:Number.MAX_SAFE_INTEGER+1},{}],
  ['模'.repeat(86),{tokens:23,window:100},{contextTokens:23,contextWindow:100}],
  ['native-model',{tokens:101,window:100},{model:'native-model',contextTokens:101,contextWindow:100}],
]) {
  runtime.$.session.model=async()=>model
  runtime.$.session.usage=async()=>({context,rateLimits:[]})
  await runtime.timers.at(-1).fn()
  assert.deepEqual(lastRuntimeSnapshot(runtime),{kind:'snapshot',canStop:true,canInput:true,canSettings:true,...expected},'invalid native reading was not filtered')
}
const modelOnly=fixture()
modelOnly.$.session.model=async()=>'only-model'
await modelOnly.call('session.start')
await settle()
assert.deepEqual(lastRuntimeSnapshot(modelOnly),{kind:'snapshot',canStop:true,canInput:true,canSettings:true,model:'only-model'})
const usageOnly=fixture()
usageOnly.$.session.usage=async()=>({context:{window:100}})
await usageOnly.call('session.start')
await settle()
assert.deepEqual(lastRuntimeSnapshot(usageOnly),{kind:'snapshot',canStop:true,canInput:true,canSettings:true,contextWindow:100})
assert.deepEqual(lastRuntimeSnapshot(f),{kind:'snapshot',canStop:true,canInput:true,canSettings:true},'missing native methods must leave runtime values unknown')
for (const method of ['model','usage']) {
  for (const change of ['session','surface','generation']) {
    const racing=fixture()
    racing.$.session.model=async()=>'must-not-cross-binding'
    racing.$.session.usage=async()=>({context:{tokens:12,window:100}})
    await racing.call('session.start')
    await settle()
    let releaseRead, markEntered
    const entered=new Promise(resolve=>markEntered=resolve)
    racing.$.session[method]=async()=>{
      markEntered()
      return await new Promise(resolve=>releaseRead=resolve)
    }
    const requestBoundary=racing.requests.length
    const pumping=racing.timers.at(-1).fn()
    await entered
    if (change==='session') racing.setID(sessionB)
    if (change==='surface') racing.setSurfaces(['terminal','desktop'])
    if (change==='generation') await racing.call('session.end',{sessionId:sessionA,reason:'clear'})
    releaseRead(method==='model' ? 'must-not-cross-binding' : {context:{tokens:42,window:100}})
    await pumping
    assert.equal(racing.requests.slice(requestBoundary).some(r=>r.action==='push'),false,method+' read crossed '+change+' binding change')
    assertEnvelopes(racing)
  }
}
// Native hook completion is independent of a still-pending usage read.
const pending=fixture()
let enteredUsage, finishUsage
const usageEntered=new Promise(resolve=>enteredUsage=resolve)
pending.$.session.usage=async()=>{enteredUsage();return await new Promise(resolve=>finishUsage=resolve)}
let startContinued=false
const starting=pending.hooks.get('session.start')(pending.$,{},async e=>{startContinued=true;return e})
await usageEntered
assert.equal(startContinued,true,'runtime read blocked native session start')
await starting
finishUsage({context:{window:100}})
await settle()
assert.deepEqual(lastRuntimeSnapshot(pending),{kind:'snapshot',canStop:true,canInput:true,canSettings:true,contextWindow:100})
const catalog=fixture()
let catalogRows=[{key:'model',kind:'choice',options:['default','sonnet','opus[1m]'],provider:{plugin:'engine',tier:'core'},isLocked:false}]
catalog.$.config={list:async()=>catalogRows}
await catalog.call('session.start')
await settle()
assert.deepEqual(lastRuntimeSnapshot(catalog).modelOptions,['default','sonnet','opus[1m]'])
for (const row of [
  {...catalogRows[0],provider:{plugin:'other',tier:'plugin'}},
  {...catalogRows[0],kind:'text'},
  {...catalogRows[0],options:['same','same']},
  {...catalogRows[0],options:['bad\nmodel']},
  {...catalogRows[0],options:['模'.repeat(86)]},
  {...catalogRows[0],options:Array(33).fill('model')},
]) {
  catalogRows=[row]
  await catalog.timers.at(-1).fn()
  assert.equal(lastRuntimeSnapshot(catalog).modelOptions,undefined,'untrusted or invalid model row became a catalog')
}
catalog.$.config.list=async()=>{throw new Error('unavailable')}
await catalog.timers.at(-1).fn()
assert.equal(lastRuntimeSnapshot(catalog).modelOptions,undefined,'catalog read failure retained stale options')
console.log('official Mod runtime snapshot fixtures passed')
`
