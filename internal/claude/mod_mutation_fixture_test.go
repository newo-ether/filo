package claude

const mutationModFixtureScript = `
for (const surface of ['terminal','desktop']) {
  let command
  const sent=[]
  const mutation=fixture({surfaces:[surface],response:(action,body,response)=>
    action==='pull' && command ? {...response,commands:[command]} : response})
  mutation.$.prompt={submit:async value=>{sent.push(value);return {text:value.text,origin:{kind:'plugin'}}}}
  let appendResult
  mutation.$.session.append=async value=>{
    assert.equal(value.message.isMeta,true,'native 2.1.308 requires an explicit user-note marker')
    sent.push(value)
    return appendResult ?? {uuid:rowID,message:{...value.message,role:'user',isMeta:true}}
  }
  await mutation.call('session.start')
  await settle()
  await mutation.call('turn.start',{turnId:'initial-turn'})
  await mutation.call('turn.complete',{turnId:'initial-turn'})
  command={id:'s'.repeat(43),operation:'send',text:'unique input'}
  await mutation.timers.at(-1).fn()
  assert.deepEqual(sent,[{text:'unique input',asUser:true}])
  assert.equal(mutation.requests.findLast(r=>r.action==='result').body.result.ok,true)
  await mutation.call('turn.start',{turnId:'sent-turn',text:'prefix unique input suffix'})
  await mutation.timers.at(-1).fn()
  assert(!mutation.requests.filter(r=>r.action==='push').flatMap(r=>r.body.events).some(e=>e.observedCommand===command.id),'substring observed an input')
  await mutation.call('session.append',{uuid:rowID,message:{type:'user',content:[{type:'text',text:'unique input'}]}})
  await mutation.timers.at(-1).fn()
  assert(mutation.requests.findLast(r=>r.action==='push').body.events.some(e=>e.uuid===rowID&&e.observedCommand===command.id))
  assert.equal(sent.length,1,'input replayed')
  command={id:'i'.repeat(43),operation:'send',turnId:'sent-turn',text:'running input'}
  await mutation.timers.at(-1).fn()
  assert.deepEqual(sent.at(-1),{message:{type:'user',isMeta:true,content:[{type:'text',text:'running input'}]}})
  assert(mutation.requests.findLast(r=>r.action==='push').body.events.some(e=>e.uuid===rowID&&e.observedCommand===command.id&&e.text==='running input'),'native append result was not observed')
  await mutation.timers.at(-1).fn()
  assert.equal(sent.length,2,'running input replayed')
  for(const [name,result,observed] of [
    ['rewritten',{uuid:rowID,message:{type:'user',isMeta:true,content:[{type:'text',text:'other text'}]}},false],
    ['denied',{deny:'policy refusal'},false],
    ['missing-id',{message:{type:'user',content:[{type:'text',text:'running input'}]}},false],
  ]) {
    appendResult=result
    command={id:name.padEnd(43,'a'),operation:'send',turnId:'sent-turn',text:'running input'}
    await mutation.timers.at(-1).fn()
    const events=mutation.requests.filter(r=>r.action==='push').flatMap(r=>r.body.events)
    assert.equal(events.some(e=>e.observedCommand===command.id),observed,name+' fabricated observation')
    assert.equal(mutation.requests.findLast(r=>r.action==='result').body.result.ok,name==='rewritten',name+' native acceptance')
  }
  const nativeAppend=mutation.$.session.append
  for (const [reason,refusal] of [
    ['refused: main is a conversation no plugin may shape','append_read_only'],
    ['this process keeps no conversation to append to','append_unbound'],
    ['refused: a uuid the engine did not mint','append_identity'],
    ['refused: a user row that is not isMeta','append_shape'],
    ['private arbitrary provider text','native_failure'],
  ]) {
    let calls=0
    mutation.$.session.append=async()=>{calls++;throw new Error('$.session.append: '+reason)}
    command={id:refusal.padEnd(43,'r'),operation:'send',turnId:'sent-turn',text:'unique refused input '+refusal}
    await mutation.timers.at(-1).fn()
    assert.deepEqual(mutation.requests.findLast(r=>r.action==='result').body.result,{id:command.id,ok:false,refusal})
    await mutation.timers.at(-1).fn()
    assert.equal(calls,1,'refused input replayed')
    assert(!JSON.stringify(mutation.requests).includes('private arbitrary provider text'),'raw native error leaked')
  }
  mutation.$.session.append=async()=>({deny:'$.session.append: refused: main is a conversation no plugin may shape'})
  command={id:'p'.repeat(43),operation:'send',turnId:'sent-turn',text:'unique denied note'}
  await mutation.timers.at(-1).fn()
  assert.equal(mutation.requests.findLast(r=>r.action==='result').body.result.refusal,'append_read_only')
  mutation.$.session.append=nativeAppend
  appendResult=undefined
  const originalAppend=mutation.$.session.append
  mutation.$.session.append=async value=>{const result=await originalAppend(value);mutation.setID(sessionB);return result}
  command={id:'b'.repeat(43),operation:'send',turnId:'sent-turn',text:'binding-change input'}
  await mutation.timers.at(-1).fn()
  assert(!mutation.requests.filter(r=>r.action==='push').flatMap(r=>r.body.events).some(e=>e.observedCommand===command.id),'append from changed binding was observed')
  mutation.setID(sessionA)
  mutation.$.session.append=originalAppend
  command={id:'m'.repeat(43),operation:'settings',settings:{model:'sonnet',effort:'low'}}
  await mutation.timers.at(-1).fn()
  async function selectedStep(event, expected, actual=expected, evidence=true) {
    let calls=0
    const next=async function* (input) {
      calls++
      assert.deepEqual(input,expected)
      const result={turnId:event.turnId,index:event.index,answer:'',toolUses:[],stopReason:'end_turn',usage:{model:actual.model}}
      if(evidence) next.trace=[{plugin:'engine',tier:'core',event:'turn.step',outcome:'returned',received:actual,returned:result}]
      return result
    }
    next.trace=[]
    const stream=mutation.hooks.get('turn.step')(mutation.$,event,next)
    await stream.next()
    assert.equal(calls,1)
  }
  const active={turnId:'sent-turn',index:0,model:'original',effort:'high'}
  await selectedStep(active,active)
  await mutation.call('turn.complete',{turnId:'sent-turn'})
  await mutation.call('turn.start',{turnId:'next-settings-turn'})
  const next={turnId:'next-settings-turn',index:0,model:'original',effort:'high'}
  await selectedStep(next,{...next,model:'sonnet',effort:'low'},next,false)
  await mutation.timers.at(-1).fn()
  assert(!mutation.requests.findLast(r=>r.action==='push').body.events.some(e=>e.kind==='request'&&e.observedCommand===command.id),'proposed override confirmed without engine evidence')
  await selectedStep(next,{...next,model:'sonnet',effort:'low'},{...next,model:'downstream-model',effort:'max'})
  await mutation.timers.at(-1).fn()
  assert(mutation.requests.findLast(r=>r.action==='push').body.events.some(e=>e.kind==='request'&&e.model==='downstream-model'&&e.effort==='max'),'downstream rewrite was hidden')
  await selectedStep(next,{...next,model:'sonnet',effort:'low'})
  const child={...next,turnId:'child-turn',agentId:'child'}
  await selectedStep(child,child)
  await mutation.timers.at(-1).fn()
  assert(mutation.requests.findLast(r=>r.action==='push').body.events.some(e=>e.kind==='request'&&e.observedCommand===command.id&&e.model==='sonnet'&&e.effort==='low'))
  command=undefined
  await mutation.call('session.end',{sessionId:sessionA,reason:'clear'})
  mutation.setID(sessionB)
  await mutation.timers.at(-1).fn()
  await mutation.call('turn.start',{turnId:'replacement'})
  const replacement={turnId:'replacement',index:0,model:'original',effort:'high'}
  await selectedStep(replacement,replacement)
}
for (const surface of ['terminal', 'desktop']) {
  let command
  const immediate=fixture({surfaces:[surface],response:(action,body,response)=>
    action==='pull' && command ? {...response,commands:[command]} : response})
  await immediate.call('session.start')
  await settle()
  command={id:'q'.repeat(43),operation:'settings',settings:{effort:'low'}}
  await immediate.timers.at(-1).fn()
  command=undefined
  const nativeID=immediate.$.session.id
  let finishIdentity
  const pendingIdentity=new Promise(resolve=>{finishIdentity=resolve})
  immediate.$.session.id=async()=>{await pendingIdentity;return nativeID()}
  await immediate.call('turn.start',{turnId:'immediate-next-turn'})
  const event={turnId:'immediate-next-turn',index:0,model:'native-model',effort:'high'}
  const stream=immediate.hooks.get('turn.step')(immediate.$,event,async function* (request) {
    assert.equal(request.effort,'low','async start observation lost next-turn effort on '+surface)
    return {turnId:event.turnId,index:0,answer:'',toolUses:[],stopReason:'end_turn',usage:{}}
  })
  try {
    await stream.next()
    immediate.$.session.id=nativeID
    for (const foreign of [{...event,agentId:'child'}, {...event,turnId:'stale-turn'}]) {
      const unchanged=immediate.hooks.get('turn.step')(immediate.$,foreign,async function* (request) {
        assert.equal(request,foreign,'first-turn fix rewrote a child or stale request')
      })
      await unchanged.next()
    }
    command={id:'w'.repeat(43),operation:'settings',settings:{effort:'medium'}}
    await immediate.timers.at(-1).fn()
    command=undefined
    const current=immediate.hooks.get('turn.step')(immediate.$,event,async function* (request) {
      assert.equal(request,event,'settings received during async start changed the current turn')
    })
    await current.next()
  } finally {
    finishIdentity()
    immediate.$.session.id=nativeID
    await settle()
  }
  await immediate.call('turn.complete',{turnId:event.turnId})
  await immediate.call('turn.start',{turnId:'following-turn'})
  const following={...event,turnId:'following-turn'}
  const selected=immediate.hooks.get('turn.step')(immediate.$,following,async function* (request) {
    assert.equal(request.effort,'medium','settings during async start were lost on the following turn')
  })
  await selected.next()
}
console.log('official Mod input and next-request settings fixtures passed')
`
