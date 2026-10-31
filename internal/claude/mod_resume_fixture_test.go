package claude

const resumeModFixtureScript = `
for (const surface of ['terminal','desktop']) {
  for (const [reason,source] of [['clear','clear'],['resume','resume'],['resume','fork']]) {
    const resumed=fixture({surfaces:[surface]})
    await resumed.call('session.start');await settle()
    const oldNonce=resumed.requests.find(r=>r.action==='register') && resumed.requests.find(r=>r.action==='push').body.nonce
    await resumed.call('turn.start',{turnId:'old-native-turn'});await settle()
    await resumed.call('session.append',{uuid:rowID,message:{type:'user',content:[{type:'text',text:'old queued row'}]}})
    await resumed.call('session.end',{sessionId:sessionA,reason})
    assert.equal(resumed.requests.at(-1).action,'revoke')
    assert.equal(resumed.timers.at(-1).cancelled,false,'continuing process lost its observer timer')
    let boundary=resumed.requests.length
    await resumed.timers.at(-1).fn()
    assert.equal(resumed.requests.length,boundary,'ending ID re-registered before native transition')
    resumed.setID(sessionB)
    await resumed.call('classic.SessionStart',{session_id:sessionB,source,
      session_title:'Resumed native title',transcript_path:'/fixture/'+sessionB+'.jsonl'})
    await settle();await resumed.timers.at(-1).fn();await settle();await resumed.timers.at(-1).fn()
    let changed=resumed.requests.slice(boundary)
    assert.equal(changed.find(r=>r.action==='register').body.sessionId,sessionB)
    assert.notEqual(changed.find(r=>r.action==='push').body.nonce,oldNonce)
    const events=changed.filter(r=>r.action==='push').flatMap(r=>r.body.events)
    assert(events.some(e=>e.kind==='busy'),'resume invented an idle native state')
    assert(events.some(e=>e.kind==='title'&&e.title==='Resumed native title'))
    assert(events.some(e=>e.kind==='metadata'&&e.transcriptPath==='/fixture/'+sessionB+'.jsonl'))
    assert.equal(events.some(e=>e.kind==='row'||e.turnId==='old-native-turn'),false,'revoked conversation leaked across resume')
    // Return to the original ID without another per-process session.start hook.
    await resumed.call('session.end',{sessionId:sessionB,reason:'resume'})
    resumed.setID(sessionA)
    boundary=resumed.requests.length
    await resumed.call('classic.SessionStart',{session_id:sessionA,source:'resume',session_title:'Returned title'})
    await settle();await resumed.timers.at(-1).fn();await settle();await resumed.timers.at(-1).fn()
    changed=resumed.requests.slice(boundary)
    assert.equal(changed.find(r=>r.action==='register').body.sessionId,sessionA)
    assert.notEqual(changed.find(r=>r.action==='push').body.nonce,oldNonce,'resumed ID reused revoked authority')
    assert(changed.filter(r=>r.action==='push').flatMap(r=>r.body.events).some(e=>e.kind==='title'&&e.title==='Returned title'))
    assertEnvelopes(resumed)
  }
}
console.log('native clear/resume/branch observer bindings passed')
`
