package claude

const desktopDiscoveryFixtureScript = `
function discoveryEvents(f) {
  return f.requests.filter(r=>r.action==='push').flatMap(r=>r.body.events).filter(e=>e.kind==='discovery')
}
const toolName='mcp__ccd_session_mgmt__get_session'
for (const [label,list,status,names] of [
  ['known',async()=>[{name:toolName,mcp:true,description:'ignored secret'}],'unverified',[toolName]],
  ['spoofed',async()=>[{name:toolName,mcp:true,source:'sdk'}],'unverified',[toolName]],
  ['empty',async()=>[{name:'mcp__other__get_session',mcp:true}],'unverified',[]],
  ['other-session-server',async()=>[{name:'mcp__ccd_session__get_session',mcp:true}],'unverified',[]],
  ['duplicate',async()=>[{name:toolName,mcp:true},{name:toolName,mcp:true}],'invalid',[]],
  ['oversize',async()=>Array(513).fill({name:toolName,mcp:true}),'invalid',[]],
  ['long-name',async()=>[{name:'x'.repeat(257),mcp:true}],'invalid',[]],
  ['throws',()=>{throw new Error('private error must not escape')},'unavailable',[]],
]) {
  const f=fixture({surfaces:['desktop']});let calls=0
  f.$.tool={list:()=>{calls++;return list()},call:()=>assert.fail('tool dispatch from discovery'),check:()=>assert.fail('permission dispatch')}
  f.$.mcp={call:()=>assert.fail('MCP dispatch from discovery')}
  await f.call('session.start');await settle();await f.timers.at(-1).fn();await settle()
  assert.deepEqual(discoveryEvents(f),[{kind:'discovery',discovery:{status,tools:names}}],label)
  await f.timers.at(-1).fn();assert.equal(calls,1,'one list per binding')
  assert.equal(JSON.stringify(discoveryEvents(f)).includes('secret'),false)
}
for (const surface of ['terminal','desktop']) {
  const f=fixture({surfaces:[surface]});let reads=0
  if(surface==='terminal') f.$.tool={list:()=>{reads++;return []}}
  await f.call('session.start');await settle();await f.timers.at(-1).fn()
  assert.equal(reads,0);assert.deepEqual(discoveryEvents(f),[],'missing API/source must preserve existing observer')
}
const hanging=fixture({surfaces:['desktop']});let resolveList,reads=0,timeout,lastDeadline
const after=hanging.$.clock.after
hanging.$.clock.after=(ms,fn)=>{lastDeadline=fn;return after(ms,fn)}
hanging.$.tool={list:()=>{timeout=lastDeadline;reads++;return reads===1 ? new Promise(resolve=>{resolveList=resolve}) : Promise.resolve([{name:toolName,mcp:true}])}}
await hanging.call('session.start');await settle()
assert(hanging.requests.some(r=>r.action==='push'),'hanging list blocked ordinary observation')
timeout();await settle();await hanging.timers.at(-1).fn();await settle()
assert.equal(discoveryEvents(hanging)[0].discovery.status,'timeout')
await hanging.call('session.end',{sessionId:sessionA,reason:'resume'});hanging.setID(sessionB)
await hanging.timers.at(-1).fn();await settle()
assert.equal(reads,1,'timed-out physical read multiplied after registration change')
resolveList([{name:toolName,mcp:true}]);await settle()
assert.equal(discoveryEvents(hanging).length,1,'late result revived old binding')
await hanging.timers.at(-1).fn();await settle();await hanging.timers.at(-1).fn()
assert.equal(reads,2,'new binding may discover once old physical read settles')
assert.equal(discoveryEvents(hanging).at(-1).discovery.status,'unverified')
for (const change of ['session','surface']) {
  const f=fixture({surfaces:['desktop']});let finish
  f.$.tool={list:()=>new Promise(resolve=>{finish=resolve})}
  await f.call('session.start');await settle()
  if(change==='session') f.setID(sessionB);else f.setSurfaces(['desktop','terminal'])
  finish([{name:toolName,mcp:true}]);await settle();await f.timers.at(-1).fn();await settle()
  assert.deepEqual(discoveryEvents(f),[],'stale read crossed '+change+' boundary')
}
console.log('Desktop read-only discovery fixtures passed')
`
