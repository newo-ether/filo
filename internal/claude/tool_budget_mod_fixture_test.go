package claude

const toolBudgetModFixtureScript = `
for (const surfaces of [['terminal'], ['desktop']]) {
  const large = fixture({surfaces})
  await large.call('session.start'); await settle()
  await large.call('turn.start', {turnId:'budget-turn'}); await settle()
  const args = {file_path:'fixture', old_string:'a'.repeat(128*1024), new_string:'new'}
  const stream = large.hooks.get('turn.step')(large.$, {turnId:'budget-turn',index:0}, async function*() {
    yield {kind:'tool',index:0,id:'budget-use',name:'Edit'}
    yield {kind:'input',index:0,json:JSON.stringify(args)}
  })
  for await (const chunk of stream) {}
  const result = {content:[{type:'text',text:'b'.repeat(128*1024)}]}
  assert.equal(await large.hooks.get('tool.call')(large.$, {tool:'Edit',tool_use_id:'budget-use',...args}, async()=>result), result)
  await large.timers.at(-1).fn()
  const pushed = large.requests.filter(r=>r.action==='push').at(-1)
  const tool = pushed.body.events.find(e=>e.block?.nativeId==='budget-use').block
  assert.deepEqual(JSON.parse(tool.arguments), args)
  assert.deepEqual(JSON.parse(tool.result), result)
  assert.equal(tool.state,'succeeded')
  assert(new TextEncoder().encode(JSON.stringify(pushed.body)).length < 4*1024*1024)
  const overflow = large.hooks.get('turn.step')(large.$,{turnId:'budget-turn',index:1},async function*() {
    yield {kind:'tool',index:0,id:'overflow-use',name:'Write'}
    yield {kind:'input',index:0,json:'x'.repeat(512*1024)}
  })
  for await (const chunk of overflow) {}
  await large.timers.at(-1).fn()
  const event = large.requests.filter(r=>r.action==='push').at(-1).body.events.find(e=>e.block?.nativeId==='overflow-use')
  assert(event)
  assert(!('inputOverflow' in event.block))
  assert(!('arguments' in event.block))
}
for (const input of [
  {output:'x'.repeat(800000),exit_code:7},
  {arguments:JSON.stringify({content:'x'.repeat(800000)}),exit_code:7},
]) {
  const state = {}
  const text = boundedTool(state,input)
  assert(new TextEncoder().encode(text).length <= 384*1024)
  const value = JSON.parse(text)
  assert.equal(value.exit_code,7)
  if(value.arguments) JSON.parse(value.arguments)
  assert(state.truncated)
}
console.log('tool field budgets and large Mod batches passed')
`
