package claude

// Faults run against the shipped hooks. Native outputs use reference identity,
// and bounded completion races detect accidental waits, duplicate dispatch or loss.
const failOpenModFixtureScript = `
const never = () => new Promise(() => {})
const fault = () => { throw new Error('injected Filo failure') }
async function boundedNative(value, label) {
  let handle
  try {
    return await Promise.race([value, new Promise((resolve,reject)=>{
      handle=setTimeout(()=>reject(new Error(label+' blocked native continuation')),100)
    })])
  } finally { clearTimeout(handle) }
}
async function faultNativeHook(f, name, event, result) {
  let calls=0
  const next=async input=>{calls++;assert.equal(input,event);return result}
  const started=performance.now()
  assert.equal(await boundedNative(f.hooks.get(name)(f.$,event,next),name),result)
  assert.equal(calls,1,name+' dispatched next more than once')
  assert(performance.now()-started<100,name+' added latency')
}
async function faultNativeStream(f,event,nextFault=false) {
  let calls=0
  const chunks=[{kind:'text',text:'first'},{kind:'tool',id:'native-tool'},{kind:'text',text:'last'}]
  const result={native:true,usage:{tokens:3}}
  const nativeError=new Error('native stream failure')
  const next=async function*(input){
    calls++;assert.equal(input,event)
    yield chunks[0]
    if(nextFault) throw nativeError
    yield chunks[1];yield chunks[2]
    return result
  }
  const stream=f.hooks.get('turn.step')(f.$,event,next)
  const values=[]
  try {
    while(true) {
      const item=await boundedNative(stream.next(),'turn.step')
      if(item.done){assert.equal(item.value,result);break}
      values.push(item.value)
    }
    assert.equal(nextFault,false,'native error was swallowed')
  } catch(error) {if(!nextFault)throw error;assert.equal(error,nativeError)}
  assert.equal(calls,1,'native stream replayed')
  assert.equal(values.length,nextFault?1:chunks.length)
  values.forEach((item,index)=>assert.equal(item,chunks[index],'native chunk cloned or replaced'))
}
for(const failure of ['unreachable','hang','500','invalid-json','null','missing-token','unreadable-token','bad-config','config-hang']) {
  const broken=fixture()
  const read=broken.$.fs.read, stat=broken.$.fs.stat
  if(failure==='missing-token') broken.$.fs.stat=async path=>path.endsWith('/token')?fault():stat(path)
  if(failure==='unreadable-token') broken.$.fs.read=async path=>path.endsWith('/token')?fault():read(path)
  if(failure==='bad-config') broken.$.fs.read=async()=>'{garbage'
  if(failure==='config-hang') broken.$.fs.stat=never
  let exchanges=0, timeoutFn, timeoutCancelled=false
  if(['unreachable','hang','500','invalid-json','null'].includes(failure)) {
    broken.$.http.fetch=async()=>{
      exchanges++
      if(failure==='unreachable')fault()
      if(failure==='hang')return never()
      return {ok:failure!=='500',status:failure==='500'?500:200,text:failure==='null'?'null':'{garbage'}
    }
  }
  broken.$.clock.after=(ms,fn)=>{
    assert.equal(ms,750,'bridge timeout changed unexpectedly')
    timeoutFn=fn
    return {cancel(){timeoutCancelled=true}}
  }
  await faultNativeHook(broken,'session.start',{}, {native:'started'})
  await settle()
  if(failure==='hang') {
    assert.equal(exchanges,1)
    assert.equal(typeof timeoutFn,'function','HTTP hang has no timeout')
    timeoutFn()
    await settle()
    assert(timeoutCancelled,'expired timeout was not released')
    for(let spin=0;spin<3;spin++)await broken.timers.at(-1).fn()
    assert.equal(exchanges,1,'HTTP timeout accumulated uncancellable native requests')
  }
  await faultNativeHook(broken,'turn.start',{turnId:'fault-turn'}, {native:'started-turn'})
  await faultNativeStream(broken,{turnId:'fault-turn',index:0,model:'native',effort:'high'})
  const row={uuid:rowID,message:{type:'assistant',content:[{type:'text',text:'unchanged'}]}}
  await faultNativeHook(broken,'session.append',{},row)
  await faultNativeHook(broken,'turn.complete',{turnId:'fault-turn'}, {native:'completed'})
  await faultNativeHook(broken,'session.end',{sessionId:sessionA,reason:'prompt_input_exit'}, {native:'ended'})
  await settle()
  if(['missing-token','unreadable-token','bad-config','config-hang'].includes(failure))assert.equal(broken.requests.length,0)
}
for(const name of ['session.start','session.end','turn.start','turn.step','turn.complete','session.append']) {
  for(const mode of ['throw','hang']) {
    const broken=fixture()
    await broken.call('session.start');await settle()
    await broken.call('turn.start',{turnId:'injected-turn'});await settle()
    const failure=mode==='throw'?fault:never
    if(name==='session.start')broken.$.fs.stat=failure
    if(name==='session.end')broken.timers.at(-1).cancel=fault
    if(['turn.start','turn.complete','session.append'].includes(name))broken.$.session.id=failure
    if(name==='turn.step')broken.$.session.surfaces=failure
    if(name==='turn.step') {
      await faultNativeStream(broken,{turnId:'injected-turn',index:0,model:'native',effort:'high'})
      await faultNativeStream(broken,{turnId:'injected-turn',index:1,model:'native'},true)
    } else {
      const event=name==='session.end'?{sessionId:sessionA,reason:'prompt_input_exit'}:{turnId:'injected-turn'}
      const result=name==='session.append'?{uuid:rowID,message:{type:'assistant',content:[]}}:{native:name}
      await faultNativeHook(broken,name,event,result)
    }
  }
}
// Throw after partially constructing an intentional settings rewrite: the native
// request must remain the exact original object, never a half-applied override.
const rewrite=fixture({response:(action,body,response)=>action==='pull'?{...response,commands:[{
  id:'z'.repeat(43),operation:'settings',settings:{model:'phone-model',effort:'low'}
}]}:response})
await rewrite.call('session.start');await settle()
await rewrite.call('turn.start',{turnId:'rewrite-turn'});await settle()
const originalIncludes=Array.prototype.includes
Array.prototype.includes=function(value){if(value==='high')throw new Error('observation failure');return originalIncludes.call(this,value)}
try {await faultNativeStream(rewrite,{turnId:'rewrite-turn',index:0,model:'native',effort:'high',agentId:'child'})}
finally {Array.prototype.includes=originalIncludes}
const request={turnId:'rewrite-turn',index:0,model:'native',effort:'high'}
Object.defineProperty(request,'injected',{enumerable:true,get:fault})
await faultNativeStream(rewrite,request)
// A trace getter can fail after native output; chunks/result still pass unchanged.
const traced=fixture();await traced.call('session.start');await settle()
await traced.call('turn.start',{turnId:'trace-turn'});await settle()
let tracedCalls=0
const tracedResult={native:'trace-result'}
const tracedNext=async function*(input){tracedCalls++;yield input;return tracedResult}
Object.defineProperty(tracedNext,'trace',{get:fault})
const tracedEvent={turnId:'trace-turn',index:0,model:'native'}
const tracedStream=traced.hooks.get('turn.step')(traced.$,tracedEvent,tracedNext)
assert.equal((await boundedNative(tracedStream.next(),'trace chunk')).value,tracedEvent)
assert.equal((await boundedNative(tracedStream.next(),'trace result')).value,tracedResult)
assert.equal(tracedCalls,1)
// Startup may forward the first turn before configuration finishes. Preserve
// only that live event, never a turn completed while private file I/O was pending.
for(const completed of [false,true]) {
  const early=fixture()
  const stat=early.$.fs.stat
  let release
  const pending=new Promise(resolve=>{release=resolve})
  early.$.fs.stat=async path=>{await pending;return stat(path)}
  await early.call('session.start')
  await early.call('turn.start',{turnId:'before-config'})
  if(completed)await early.call('turn.complete',{turnId:'before-config'})
  release();await settle()
  const starts=early.requests.filter(r=>r.action==='push').flatMap(r=>r.body.events).filter(e=>e.kind==='start')
  assert.equal(starts.length,completed?0:1,'early config read lost/resurrected native turn')
}
// An actual elapsed HTTP deadline also releases the pump without waiting on the
// host transport; deterministic timer injection above covers retry accumulation.
const timed=fixture()
let deadlineElapsed
const elapsed=new Promise(resolve=>{deadlineElapsed=resolve})
timed.$.http.fetch=never
timed.$.clock.after=(ms,fn)=>{
  const handle=setTimeout(()=>{fn();deadlineElapsed()},ms)
  return {cancel:()=>clearTimeout(handle)}
}
const beforeTimeout=performance.now()
await timed.call('session.start')
await faultNativeStream(timed,{turnId:'timeout-turn',index:0,model:'native'})
await boundedNative(Promise.resolve('native complete'),'independent native result')
await Promise.race([elapsed,new Promise((resolve,reject)=>{
  const handle=setTimeout(()=>reject(new Error('bridge timeout failed to fire')),1500);handle.unref()
})])
assert(performance.now()-beforeTimeout>=700)
assert(performance.now()-beforeTimeout<1500)
await timed.call('session.end',{sessionId:sessionA,reason:'prompt_input_exit'})
console.log('official Mod fail-open faults passed: native identity, once-only dispatch, <=100ms, bounded HTTP')
`
