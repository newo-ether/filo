package claude

const liveModFixtureScript = `
const live = fixture()
await live.call('session.start'); await settle()
await live.call('turn.start',{turnId:'live-turn',text:'actual prompt'}); await settle()
const chunks=[{kind:'text',index:0,text:'first '},{kind:'text',index:0,text:'second'},
  {kind:'thinking',index:1,text:'native thought'}, {kind:'tool',index:2,id:'native-use',name:'Read'},
  {kind:'input',index:2,json:'{"file_path":"fixture"}'}]
const result={turnId:'live-turn',index:0,answer:'first second',toolUses:[],stopReason:'tool_use',usage:null}
let nativeCalls=0
const native=async function*(e){nativeCalls++;for(const chunk of chunks)yield chunk;return result}
const stream=live.hooks.get('turn.step')(live.$,{turnId:'live-turn',index:0,model:'native'},native)
for(const chunk of chunks)assert.equal((await stream.next()).value,chunk)
assert.equal((await stream.next()).value,result)
assert.equal(nativeCalls,1)
await live.timers.at(-1).fn()
let events=live.requests.filter(r=>r.action==='push').at(-1).body.events
assert.equal(events.filter(e=>e.kind==='block'&&e.block.type==='text').length,1,'text snapshots did not coalesce')
assert.equal(events.find(e=>e.kind==='block'&&e.block.type==='text').block.text,'first second')
assert.equal(events.find(e=>e.kind==='block'&&e.block.type==='tool').block.nativeId,'native-use')
const toolResult={ref:42,result:{native:true},text:'native output',isReadOnly:true}
const toolInput={tool:'Read',tool_use_id:'native-use',file_path:'fixture'}
let toolCalls=0
assert.equal(await live.hooks.get('tool.call')(live.$,toolInput,async e=>{assert.equal(e,toolInput);toolCalls++;return toolResult}),toolResult)
assert.equal(toolCalls,1)
await live.call('session.append',{uuid:rowID,message:{type:'assistant',content:[{type:'text',text:'first second'}]}})
await live.call('classic.UserPromptSubmit',{session_id:sessionA,session_title:'Native rename',transcript_path:'/fixture/'+sessionA+'.jsonl'})
await settle();await live.timers.at(-1).fn()
events=live.requests.filter(r=>r.action==='push').at(-1).body.events
const completedTool=events.find(e=>e.kind==='block'&&e.block.nativeId==='native-use')
assert.equal(completedTool.block.state,'succeeded');assert.equal(completedTool.block.result,'native output')
assert.deepEqual(JSON.parse(completedTool.block.arguments),{file_path:'fixture'})
assert.equal(events.some(e=>e.kind==='row'),false,'completed UUID duplicate was projected')
assert(events.some(e=>e.kind==='title'&&e.title==='Native rename'))
assert(events.some(e=>e.kind==='metadata'))
// A tool-only step and a later non-streamed turn retain native text fallback.
const toolOnly=live.hooks.get('turn.step')(live.$,{turnId:'live-turn',index:1,model:'native'},
  async function*(){yield {kind:'tool',index:0,id:'next-use',name:'Read'};return result})
await toolOnly.next();await toolOnly.next()
await live.call('session.append',{uuid:rowID,message:{type:'assistant',content:[{type:'text',text:'step fallback'}]}})
await settle();await live.timers.at(-1).fn()
assert(live.requests.filter(r=>r.action==='push').at(-1).body.events.some(e=>e.kind==='row'&&e.text==='step fallback'))
await live.call('turn.complete',{turnId:'live-turn'})
await live.call('turn.start',{turnId:'next-turn',text:'next native prompt'});await settle()
await live.call('session.append',{uuid:rowID,message:{type:'assistant',content:[{type:'text',text:'next turn fallback'}]}})
await settle();await live.timers.at(-1).fn()
assert(live.requests.filter(r=>r.action==='push').at(-1).body.events.some(e=>e.kind==='row'&&e.text==='next turn fallback'))
// Content bounds must not become a lost-lifecycle event during generation.
const longText=live.hooks.get('turn.step')(live.$,{turnId:'next-turn',index:0,model:'native'},
  async function*(){yield {kind:'text',index:0,text:'x'.repeat(25000)};return result})
await longText.next();await longText.next();await live.timers.at(-1).fn()
events=live.requests.filter(r=>r.action==='push').at(-1).body.events
assert(events.some(e=>e.kind==='truncated'))
assert.equal(events.some(e=>e.kind==='gap'),false,'content bound invalidated native turn state')
assert(events.find(e=>e.kind==='block').block.text.length<=24*1024)
for(const name of ['tool.call','classic.SessionStart','classic.UserPromptSubmit']) {
  const broken=fixture();await broken.call('session.start');await settle()
  broken.$.session.id=never
  await faultNativeHook(broken,name,{}, {native:name})
}
console.log('native block/tool/title projections passed')
`
