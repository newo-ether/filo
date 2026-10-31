package claude

const desktopMetadataFixtureScript = `
const desktopHostA='local_30000000-0000-4000-8000-000000000003'
const desktopHostB='local_40000000-0000-4000-8000-000000000004'
function metadataFixture(surface='desktop') {
  let command
  const f=fixture({surfaces:[surface],response:(action,body,response)=>
    action==='pull' && command ? {...response,commands:[command]} : response})
  let calls=0
  f.$.tool={list:async()=>[{name:'mcp__ccd_session_mgmt__get_session',mcp:true}],call:async input=>{
    calls++;assert.deepEqual(input,{tool:'mcp__ccd_session_mgmt__get_session',session_id:'self'})
    return {text:JSON.stringify({sessionId:desktopHostA,title:'private title',cwd:'/private'})}
  }}
  f.$.mcp={call:()=>assert.fail('metadata bypassed native tool permissions')}
  return {...f,issue:value=>{command=value?{hostSessionId:desktopHostA,...value}:value},calls:()=>calls,
    results:()=>f.requests.filter(r=>r.action==='result').map(r=>r.body.result)}
}
const meta=metadataFixture()
await meta.call('session.start');await settle();await meta.timers.at(-1).fn()
assert.equal(meta.calls(),0,'registration or discovery invoked metadata')
meta.issue({id:'m'.repeat(43),operation:'desktop-metadata'})
await meta.timers.at(-1).fn()
assert.deepEqual(meta.results(),[{id:'m'.repeat(43),ok:true,metadata:{status:'matched',sessionId:sessionA,hostSessionId:desktopHostA,title:'private title'}}])
assert.equal(JSON.stringify(meta.results()).includes('/private'),false,'diagnostic exposed unrelated metadata')
await meta.timers.at(-1).fn();assert.equal(meta.calls(),1,'metadata command replayed')
assert(!meta.requests.filter(r=>r.action==='push').flatMap(r=>r.body.events).some(e=>e.kind==='title'),'diagnostic promoted title')
for (const [label,result,status,reason] of [
  ['title-type',{text:JSON.stringify({sessionId:desktopHostA,title:123})},'invalid','title-shape'],
  ['title-bytes',{text:JSON.stringify({sessionId:desktopHostA,title:'字'.repeat(1366)})},'invalid','title-shape'],
  ['wrong-session',{text:JSON.stringify({sessionId:desktopHostB})},'invalid','host-session-mismatch'],
  ['engine-domain',{text:JSON.stringify({sessionId:sessionA})},'invalid','host-session-mismatch'],
  ['prefix-arithmetic',{text:JSON.stringify({sessionId:'local_'+sessionA})},'invalid','host-session-mismatch'],
  ['missing-session',{text:'{"title":"private"}'},'invalid','host-session-mismatch'],
  ['array',{text:'[]'},'invalid','metadata-shape'],
  ['not-json',{text:'session '+sessionA},'invalid','text-json'],
  ['nested',{text:JSON.stringify({metadata:{sessionId:desktopHostA}})},'invalid','host-session-mismatch'],
  ['no-text',{content:[{type:'text',text:JSON.stringify({sessionId:desktopHostA})}]},'invalid','text-missing'],
  ['oversize',{text:' '.repeat(65537)},'invalid','text-too-large'],
  ['native-error',{isError:true,text:JSON.stringify({sessionId:desktopHostA})},'refused'],
  ['native-deny',{deny:'private refusal'},'refused'],
  ['null',null,'invalid','result-shape'],
]) {
  const f=metadataFixture();await f.call('session.start');await settle()
  f.$.tool.call=async()=>result
  f.issue({id:'v'.repeat(43),operation:'desktop-metadata'})
  await f.timers.at(-1).fn()
  assert.deepEqual(f.results().at(-1),{id:'v'.repeat(43),ok:false,metadata:{status,...(reason?{reason}:{})}},label)
}
for (const title of [undefined,null,'','字'.repeat(1365)+'a']) {
  const f=metadataFixture();await f.call('session.start');await settle()
  f.$.tool.call=async()=>({text:JSON.stringify({sessionId:desktopHostA,title})})
  f.issue({id:'u'.repeat(43),operation:'desktop-metadata'});await f.timers.at(-1).fn()
  assert.deepEqual(f.results().at(-1).metadata,{status:'matched',sessionId:sessionA,hostSessionId:desktopHostA,...(title==null?{}:{title})})
}
for (const throws of [true,false]) {
  const f=metadataFixture();await f.call('session.start');await settle()
  f.$.tool.call=()=>{if(throws)throw new Error('private failure');return Promise.reject(new Error('private failure'))}
  f.issue({id:'x'.repeat(43),operation:'desktop-metadata'});await f.timers.at(-1).fn()
  assert.equal(f.results().at(-1).metadata.status,'unavailable')
}
for (const extra of [{session_id:sessionB},{tool:'other'},{consent:true},{text:'input'}]) {
  const f=metadataFixture();await f.call('session.start');await settle()
  f.issue({id:'e'.repeat(43),operation:'desktop-metadata',...extra});await f.timers.at(-1).fn()
  assert.equal(f.calls(),0,'query accepted arbitrary arguments');assert.equal(f.results().length,0)
}
const terminalMetadata=metadataFixture('terminal')
await terminalMetadata.call('session.start');await settle()
terminalMetadata.issue({id:'t'.repeat(43),operation:'desktop-metadata'})
await terminalMetadata.timers.at(-1).fn();assert.equal(terminalMetadata.calls(),0)
for (const change of ['session','surface','generation']) {
  const f=metadataFixture();await f.call('session.start');await settle()
  let finish,entered=false
  f.$.tool.call=()=>{entered=true;return new Promise(resolve=>{finish=resolve})}
  f.issue({id:'r'.repeat(43),operation:'desktop-metadata'})
  const running=f.timers.at(-1).fn();await settle();assert(entered)
  if(change==='session')f.setID(sessionB)
  if(change==='surface')f.setSurfaces(['desktop','terminal'])
  if(change==='generation')await f.call('session.end',{sessionId:sessionA,reason:'resume'})
  finish({text:JSON.stringify({sessionId:sessionA})});await running;await settle()
  assert.equal(f.results().length,0,'stale metadata crossed '+change)
}
const blockedMetadata=metadataFixture();let finishMetadata,physicalCalls=0,expireMetadata
const metadataAfter=blockedMetadata.$.clock.after
blockedMetadata.$.clock.after=(ms,fn)=>{if(ms===2000)expireMetadata=fn;return metadataAfter(ms,fn)}
blockedMetadata.$.tool.call=()=>{physicalCalls++;return new Promise(resolve=>{finishMetadata=resolve})}
await blockedMetadata.call('session.start');await settle()
blockedMetadata.issue({id:'h'.repeat(43),operation:'desktop-metadata'})
const hangingMetadata=blockedMetadata.timers.at(-1).fn();await settle()
assert.equal(physicalCalls,1);expireMetadata();await hangingMetadata
assert.equal(blockedMetadata.results().at(-1).metadata.status,'timeout')
const pullsBefore=blockedMetadata.requests.filter(r=>r.action==='pull').length
await blockedMetadata.timers.at(-1).fn()
assert(blockedMetadata.requests.filter(r=>r.action==='pull').length>pullsBefore,'timeout blocked observation')
blockedMetadata.issue(undefined)
await blockedMetadata.call('session.end',{sessionId:sessionA,reason:'resume'});blockedMetadata.setID(sessionB)
await blockedMetadata.timers.at(-1).fn();await settle()
blockedMetadata.issue({id:'i'.repeat(43),operation:'desktop-metadata'})
await blockedMetadata.timers.at(-1).fn()
assert.equal(physicalCalls,1,'timeout or rebind multiplied physical metadata calls')
assert.equal(blockedMetadata.results().at(-1).metadata.status,'busy')
const resultsBefore=blockedMetadata.results().length
finishMetadata({text:JSON.stringify({sessionId:sessionA})});await settle()
assert.equal(blockedMetadata.results().length,resultsBefore,'late result revived timed-out query')
blockedMetadata.$.tool.call=async()=>{physicalCalls++;return {text:JSON.stringify({sessionId:desktopHostB})}}
blockedMetadata.issue({id:'j'.repeat(43),operation:'desktop-metadata',hostSessionId:desktopHostB});await blockedMetadata.timers.at(-1).fn()
assert.equal(physicalCalls,2);assert.equal(blockedMetadata.results().at(-1).metadata.sessionId,sessionB)
console.log('Desktop fixed metadata fixtures passed')
`
