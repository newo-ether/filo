package claude

const metadataModFixtureScript = `
const metadataEvents = f => f.requests.filter(r=>r.action==='push').flatMap(r=>r.body.events)
const titleEvents = events => events.filter(e=>e.kind==='metadata'||e.kind==='title')
async function reconnectMetadata(f, action='pull') {
  const transport=f.$.http.fetch
  let failed=false
  f.$.http.fetch=async (url,request)=>{
    if(!failed&&url.endsWith('/'+action)){failed=true;throw new Error('fixture bridge restart')}
    return transport(url,request)
  }
  await f.timers.at(-1).fn()
  assert(failed,'fixture did not lose the bridge connection')
  f.$.http.fetch=transport
  const boundary=f.requests.length
  await f.timers.at(-1).fn();await settle();await f.timers.at(-1).fn()
  return f.requests.slice(boundary)
}
for(const surface of ['terminal','desktop']) {
  for(const failure of ['pull','push']) {
    const cached=fixture({surfaces:[surface]})
    await cached.call('session.start');await settle()
    const oldNonce=cached.requests.find(r=>r.action==='push').body.nonce
    const path='/fixture/'+sessionA+'.jsonl'
    await cached.call('classic.SessionStart',{session_id:sessionA,session_title:'Known native title',transcript_path:path})
    await settle();await cached.timers.at(-1).fn()
    // A later title-only observation must keep the previously verified path.
    await cached.call('classic.UserPromptSubmit',{session_id:sessionA,session_title:'Current native title'})
    await cached.call('turn.start',{turnId:'discard-on-disconnect'});await settle()
    await cached.call('session.append',{uuid:rowID,message:{type:'user',content:[{type:'text',text:'old queued row'}]}})
    const reconnected=await reconnectMetadata(cached,failure)
    assert.equal(reconnected.filter(r=>r.action==='register').length,1)
    assert.notEqual(reconnected.find(r=>r.action==='push').body.nonce,oldNonce,'bridge reconnect reused revoked authority')
    const events=reconnected.filter(r=>r.action==='push').flatMap(r=>r.body.events)
    assert.deepEqual(titleEvents(events),[{kind:'metadata',transcriptPath:path},{kind:'title',title:'Current native title'}])
    assert.equal(events.some(e=>e.kind==='row'||e.kind==='start'||e.kind==='complete'||e.kind==='result'),false,'metadata cache restored conversation or control state')
    assert(events.some(e=>e.kind==='busy'),'metadata cache inferred idle')
    assertEnvelopes(cached)
  }
}
const unknownMetadata=fixture()
await unknownMetadata.call('session.start');await settle()
assert.equal(titleEvents((await reconnectMetadata(unknownMetadata)).filter(r=>r.action==='push').flatMap(r=>r.body.events)).length,0,'unknown title was invented')
for(const transition of ['start','end','id','surface','id-away-and-back']) {
  const changed=fixture()
  await changed.call('session.start');await settle()
  await changed.call('classic.UserPromptSubmit',{session_id:sessionA,session_title:'Old session metadata',transcript_path:'/fixture/'+sessionA+'.jsonl'})
  await settle();await changed.timers.at(-1).fn()
  const boundary=changed.requests.length
  if(transition==='start')await changed.call('session.start')
  if(transition==='end')await changed.call('session.end',{sessionId:sessionA,reason:'resume'})
  if(transition==='id'||transition==='end')changed.setID(sessionB)
  if(transition==='surface')changed.setSurfaces(['desktop'])
  if(transition==='id-away-and-back') {
    // A different ID first encountered after transport loss discards the cache.
    const transport=changed.$.http.fetch
    changed.$.http.fetch=async()=>{throw new Error('fixture bridge restart')}
    await changed.timers.at(-1).fn()
    changed.$.http.fetch=transport
    changed.setID(sessionB)
    await changed.timers.at(-1).fn();await settle()
    changed.setID(sessionA)
  }
  await settle();await changed.timers.at(-1).fn();await settle();await changed.timers.at(-1).fn()
  const events=changed.requests.slice(boundary).filter(r=>r.action==='push').flatMap(r=>r.body.events)
  assert.equal(titleEvents(events).length,0,'known metadata crossed native boundary: '+transition)
}
for(const temporarySurfaces of [[],['terminal','desktop'],['desktop']]) {
  const detached=fixture()
  await detached.call('session.start');await settle()
  await detached.call('classic.UserPromptSubmit',{session_id:sessionA,session_title:'Old surface title'})
  await settle();await detached.timers.at(-1).fn()
  const transport=detached.$.http.fetch
  detached.$.http.fetch=async()=>{throw new Error('fixture bridge restart')}
  await detached.timers.at(-1).fn()
  detached.$.http.fetch=transport
  detached.setSurfaces(temporarySurfaces)
  const boundary=detached.requests.length
  await detached.timers.at(-1).fn();await settle()
  detached.setSurfaces(['terminal'])
  await detached.timers.at(-1).fn();await settle();await detached.timers.at(-1).fn()
  assert.equal(titleEvents(detached.requests.slice(boundary).filter(r=>r.action==='push').flatMap(r=>r.body.events)).length,0,'unbound metadata survived a changed or ambiguous surface')
}
for(const transition of ['start','end','id','surface']) {
  const delayed=fixture()
  await delayed.call('session.start');await settle()
  const surfaces=delayed.$.session.surfaces
  let release
  const wait=new Promise(resolve=>{release=resolve})
  delayed.$.session.surfaces=async()=>{await wait;return surfaces()}
  await delayed.call('classic.UserPromptSubmit',{session_id:sessionA,session_title:'Late old title'})
  const boundary=delayed.requests.length
  if(transition==='start')await delayed.call('session.start')
  if(transition==='end'){await delayed.call('session.end',{sessionId:sessionA,reason:'resume'});delayed.setID(sessionB)}
  if(transition==='id')delayed.setID(sessionB)
  if(transition==='surface')delayed.setSurfaces(['desktop'])
  release();await settle()
  delayed.$.session.surfaces=surfaces
  await delayed.timers.at(-1).fn();await settle();await delayed.timers.at(-1).fn()
  assert.equal(titleEvents(delayed.requests.slice(boundary).filter(r=>r.action==='push').flatMap(r=>r.body.events)).length,0,'late metadata crossed native boundary: '+transition)
}
const boundedMetadata=fixture()
await boundedMetadata.call('session.start');await settle()
await boundedMetadata.call('classic.UserPromptSubmit',{session_id:sessionA,session_title:'x'.repeat(4097),transcript_path:'/fixture/'+sessionB+'.jsonl'})
await boundedMetadata.call('classic.UserPromptSubmit',{session_id:sessionB,session_title:'Wrong native session'})
await settle();await boundedMetadata.timers.at(-1).fn()
assert.equal(titleEvents(metadataEvents(boundedMetadata)).length,0)
assert.equal(titleEvents((await reconnectMetadata(boundedMetadata)).filter(r=>r.action==='push').flatMap(r=>r.body.events)).length,0,'invalid metadata entered reconnect cache')
for(const transition of ['id','surface']) {
  for(const failure of ['pull','push']) {
    let settingsCommand
    const rebound=fixture({response:(action,body,response)=>action==='pull'&&settingsCommand?
      {...response,commands:[settingsCommand]}:response})
    await rebound.call('session.start');await settle()
    await rebound.call('turn.start',{turnId:'old-owner-turn'});await settle()
    if(transition==='id')rebound.setID(sessionB)
    else rebound.setSurfaces(['desktop'])
    const transport=rebound.$.http.fetch
    let failed=false,failedNonce
    rebound.$.http.fetch=async(url,request)=>{
      const body=JSON.parse(request.body)
      if(url.endsWith('/'+failure)&&!failed) {
        failed=true;failedNonce=body.nonce
        await rebound.call('turn.start',{turnId:'failed-rebind-turn'});await settle()
        await rebound.call('session.append',{uuid:rowID,message:{type:'user',content:[{type:'text',text:'failed rebind row'}]}})
        throw new Error('fixture transport failed immediately after native rebind')
      }
      return transport(url,request)
    }
    settingsCommand={id:'m'.repeat(43),operation:'settings',settings:{effort:'low'}}
    await rebound.timers.at(-1).fn()
    assert(failed,'fixture did not fail after native rebind')
    const boundary=rebound.requests.length
    settingsCommand=undefined
    rebound.$.http.fetch=transport
    await rebound.timers.at(-1).fn();await settle();await rebound.timers.at(-1).fn()
    const recovered=rebound.requests.slice(boundary)
    assert.equal(recovered.filter(r=>r.action==='register').length,1,'failed same-pump rebind retained its registration: '+transition+'/'+failure)
    assert.notEqual(recovered.find(r=>r.action==='push').body.nonce,failedNonce)
    const events=recovered.filter(r=>r.action==='push').flatMap(r=>r.body.events)
    assert.equal(events.some(e=>e.kind==='row'||e.kind==='start'||e.kind==='complete'),false,'failed rebind retained turn or row state')
    const runtime=lastRuntimeSnapshot(rebound)
    assert.equal(runtime.activeTurnId,undefined,'failed rebind retained active turn')
    await rebound.call('turn.start',{turnId:'fresh-owner-turn'});await settle()
    await nativeStep(rebound,{turnId:'fresh-owner-turn',index:0,model:'native-model',effort:'high'})
    assertEnvelopes(rebound)
  }
}
for(const phase of ['revoke','pull','push']) {
  for(const lifecycle of ['start','end']) {
    const stale=fixture()
    await stale.call('session.start');await settle()
    if(phase==='revoke')stale.setID(sessionB)
    const transport=stale.$.http.fetch
    let observed=false,releaseLifecycle,releaseConfig
    let lifecycleReady
    const lifecycleDone=new Promise(resolve=>{lifecycleReady=resolve})
    const released=new Promise(resolve=>{releaseLifecycle=resolve})
    const configReleased=new Promise(resolve=>{releaseConfig=resolve})
    const originalStat=stale.$.fs.stat
    stale.$.http.fetch=async(url,request)=>{
      if(!observed&&url.endsWith('/'+phase)) {
        observed=true
        if(lifecycle==='start') {
          stale.$.fs.stat=async path=>{lifecycleReady();await configReleased;return originalStat(path)}
          await stale.call('session.start')
        } else {
          await stale.call('session.end',{sessionId:sessionA,reason:'resume'})
          lifecycleReady()
        }
        await released
        if(phase!=='revoke')throw new Error('late old transport rejection')
      }
      return transport(url,request)
    }
    const oldPump=stale.timers.at(-1).fn()
    await lifecycleDone
    const boundary=stale.requests.length
    releaseLifecycle();await oldPump;await settle()
    assert.equal(stale.requests.slice(boundary).filter(r=>r.action==='register').length,0,'stale pump registered after external lifecycle: '+phase+'/'+lifecycle)
    stale.$.http.fetch=transport
    if(lifecycle==='end')stale.setID(sessionB)
    releaseConfig();await settle()
    stale.$.fs.stat=originalStat
    await stale.timers.at(-1).fn();await settle();await stale.timers.at(-1).fn()
    assert.equal(stale.requests.slice(boundary).filter(r=>r.action==='register').length,1,'stale pump damaged lifecycle registration: '+phase+'/'+lifecycle)
  }
}
console.log('bounded exact-session metadata reconnect and lifecycle fences passed')
`
