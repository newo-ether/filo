package claude

const desktopRenameFixtureScript = `
function renameFixture(surface='desktop') {
  const f=metadataFixture(surface), calls=[]
  let title='old title'
  f.$.tool.call=async input=>{
    calls.push(input)
    if(input.tool==='mcp__ccd_session_mgmt__get_session') {
      assert.deepEqual(input,{tool:'mcp__ccd_session_mgmt__get_session',session_id:'self'})
      return {text:JSON.stringify({sessionId:desktopHostA,title})}
    }
    assert.deepEqual(input,{tool:'mcp__ccd_session_mgmt__set_session_title',session_id:'self',title:'new title'})
    title=input.title
    return {text:'native acknowledgement'}
  }
  return {...f,nativeCalls:calls}
}
async function issueRename(f,id='n',title='new title',extra={}) {
  f.issue({id:id.repeat(43),operation:'desktop-rename',title,...extra})
  await f.timers.at(-1).fn()
}
const renamed=renameFixture()
await renamed.call('session.start');await settle()
assert.equal(renamed.nativeCalls.length,0,'registration mutated native title')
await issueRename(renamed)
assert.deepEqual(renamed.results(),[{id:'n'.repeat(43),ok:true,rename:{status:'applied',sessionId:sessionA,hostSessionId:desktopHostA,title:'new title'}}])
assert.equal(renamed.nativeCalls.length,3)
await renamed.timers.at(-1).fn();assert.equal(renamed.nativeCalls.length,3,'rename replayed')
assert(!renamed.requests.filter(r=>r.action==='push').flatMap(r=>r.body.events).some(e=>e.kind==='title'),'rename promoted title')
for(const [stage,result,status,count] of [
  [0,{text:JSON.stringify({sessionId:sessionB,title:'old title'})},'invalid',1],
  [0,{deny:'permission refused'},'refused',1],
  [1,{deny:'permission refused'},'refused',2],
  [1,{isError:true,text:'refused'},'refused',2],
  [1,null,'unknown',2],
  [2,{text:JSON.stringify({sessionId:sessionB,title:'new title'})},'unknown',3],
  [2,{text:JSON.stringify({sessionId:desktopHostA,title:'different'})},'unknown',3],
  [2,{text:JSON.stringify({sessionId:desktopHostA,title:'NEW TITLE'})},'unknown',3],
  [2,{text:JSON.stringify({sessionId:desktopHostA})},'unknown',3],
  [2,{isError:true},'unknown',3],
]) {
  const f=renameFixture();await f.call('session.start');await settle()
  const original=f.$.tool.call;let index=0
  f.$.tool.call=input=>index++===stage?(f.nativeCalls.push(input),Promise.resolve(result)):original(input)
  await issueRename(f)
  assert.deepEqual(f.results().at(-1),{id:'n'.repeat(43),ok:false,rename:{status,...(stage===0&&status==='invalid'?{reason:'host-session-mismatch'}:{})}})
  assert.equal(f.nativeCalls.length,count)
}
for(const stage of [0,1,2]) {
  for(const change of ['session','surface','generation']) {
    const f=renameFixture();await f.call('session.start');await settle()
    const original=f.$.tool.call;let index=0,finish
    f.$.tool.call=input=>index++===stage?(f.nativeCalls.push(input),new Promise(resolve=>finish=resolve)):original(input)
    const running=issueRename(f);await settle();assert.equal(typeof finish,'function')
    if(change==='session')f.setID(sessionB)
    if(change==='surface')f.setSurfaces(['desktop','terminal'])
    if(change==='generation')await f.call('session.end',{sessionId:sessionA,reason:'resume'})
    finish(stage===1?{text:'ack'}:{text:JSON.stringify({sessionId:desktopHostA,title:'new title'})})
    await running;await settle()
    assert.equal(f.nativeCalls.length,stage+1,'stale binding continued native rename')
    assert.equal(f.results().length,0,'stale binding reported rename')
  }
}
for(const stage of [0,1,2]) {
  const f=renameFixture();let expire,finish,index=0
  const original=f.$.tool.call,after=f.$.clock.after
  f.$.clock.after=(ms,fn)=>{if(ms===2000)expire=fn;return after(ms,fn)}
  f.$.tool.call=input=>index++===stage?(f.nativeCalls.push(input),new Promise(resolve=>finish=resolve)):original(input)
  await f.call('session.start');await settle()
  const running=issueRename(f);await settle();assert.equal(typeof expire,'function')
  expire();await running
  assert.equal(f.results().at(-1).rename.status,'unknown')
  await issueRename(f,'o');assert.equal(f.results().at(-1).rename.status,'busy')
  f.issue({id:'p'.repeat(43),operation:'desktop-metadata'});await f.timers.at(-1).fn()
  assert.equal(f.results().at(-1).metadata.status,'busy','metadata escaped rename physical slot')
  f.issue(undefined);await f.call('session.end',{sessionId:sessionA,reason:'resume'})
  f.setID(sessionB);await f.timers.at(-1).fn();await settle()
  await issueRename(f,'q');assert.equal(f.results().at(-1).rename.status,'busy','rebind released physical slot')
  const results=f.results().length
  finish(stage===1?{text:'ack'}:{text:JSON.stringify({sessionId:desktopHostA,title:'new title'})})
  await settle()
  assert.equal(f.nativeCalls.length,stage+1,'timed-out call started another native operation')
  assert.equal(f.results().length,results,'late physical result revived rename')
  f.$.tool.call=async()=>({text:JSON.stringify({sessionId:desktopHostB})})
  f.issue({id:'s'.repeat(43),operation:'desktop-metadata',hostSessionId:desktopHostB});await f.timers.at(-1).fn()
  assert.equal(f.results().at(-1).metadata.status,'matched','settlement did not release physical slot')
}
for(const stage of [0,1,2]) {
  const f=renameFixture();await f.call('session.start');await settle()
  const original=f.$.tool.call;let index=0
  f.$.tool.call=input=>{if(index++===stage)throw Error('private native failure');return original(input)}
  await issueRename(f);assert.equal(f.results().at(-1).rename.status,'unknown')
}
for(const title of ['', '  ', ' trim ', 'x'.repeat(201), '\u{1f680}'.repeat(101), 5, null]) {
  const f=renameFixture();await f.call('session.start');await settle()
  await issueRename(f,'v',title)
  assert.equal(f.nativeCalls.length,0,'invalid title reached native tools')
  assert.equal(f.results().at(-1).rename.status,'invalid')
}
for(const extra of [{session_id:sessionB},{tool:'other'},{_consent:'spoofed'},{text:'prompt'}]) {
  const f=renameFixture();await f.call('session.start');await settle()
  await issueRename(f,'e','new title',extra)
  assert.equal(f.nativeCalls.length,0);assert.equal(f.results().length,0)
}
const terminalRename=renameFixture('terminal')
await terminalRename.call('session.start');await settle();await issueRename(terminalRename)
assert.equal(terminalRename.nativeCalls.length,0)
console.log('Desktop fixed rename fixtures passed')
`
