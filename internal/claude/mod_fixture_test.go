package claude

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// This runs the shipped JS itself against fixture APIs, not a rewritten observer.
// Node is an optional development syntax/fixture runner only; it is not shipped,
// installed, or required by the Mod or the Go service.
func TestOfficialModArtifactFixtures(t *testing.T) {
	runner, err := exec.LookPath("node")
	if err != nil {
		t.Skip("optional installed JS fixture runner is unavailable")
	}
	source, err := os.ReadFile("mod/hooks/register.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(source) + modFixtureScript + runtimeModFixtureScript + controlModFixtureScript + effortModFixtureScript + mutationModFixtureScript + failOpenModFixtureScript + liveModFixtureScript
	command := exec.Command(runner, "--input-type=module")
	command.Stdin = strings.NewReader(script)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("shipped Mod fixture: %v\n%s", err, output)
	}
}

const modFixtureScript = `
import assert from 'node:assert/strict'
const sessionA = '10000000-0000-4000-8000-000000000001'
const sessionB = '10000000-0000-4000-8000-000000000002'
const rowID = '20000000-0000-4000-8000-000000000001'
function fixture(options = {}) {
  const hooks = new Map(), reads = [], requests = [], timers = []
  let id = sessionA, nonce, seq = 0, registrations = 0
  let surfaces = options.surfaces ?? ['terminal'], onRequest
  const config = JSON.stringify({url:'http://127.0.0.1:7438/claude-mod/v1/', tokenFile:'/fixture/token',
    sources:options.sources ?? ['claude-code','claude-desktop']})
  const files = new Map([['/fixture/plugin/filo-config.json',config],['/fixture/token','b'.repeat(64)]])
  const $ = {
    plugin: {root:'/fixture/plugin'},
    fs: {
      stat: async path => ({kind:'file', size: options.oversize ? 99999 : files.get(path).length,
        isLink: false, mtimeMs:1, realPath:path}),
      read: async path => { reads.push(path); return files.get(path) },
    },
    session: {id:async()=>id, surfaces:async()=>surfaces, version:async()=>({version:'fixture'}), cwd:async()=>'/fixture'},
    clock: {
      after:(ms,fn)=>{const handle=setTimeout(fn,ms);handle.unref();return {cancel:()=>clearTimeout(handle)}},
      every:(ms, fn)=>{const timer={fn,cancelled:false,cancel(){this.cancelled=true}};timers.push(timer);return timer},
    },
    http: {fetch:async (url, request)=>{
      assert.equal(request.headers.Authorization, 'Bearer '+'b'.repeat(64))
      const action = url.split('/').at(-1), body=JSON.parse(request.body)
      requests.push({action,body})
      onRequest?.(action,body)
      let response = {readOnly:true,ttlMillis:15000,historyBoundary:'observed-since-registration'}
      if(action==='register') {seq=0;nonce=String(++registrations).padStart(43,'a');response.nonce=nonce}
      if(action==='push') {assert.equal(body.nonce,nonce);assert.equal(body.sequence,seq+1);seq++;response.sequence=seq}
      if (options.response) response = options.response(action, body, response)
      return {ok:true,text:JSON.stringify(response)}
    }},
  }
  const catches = new Map(), matchers = new Map()
  register((name, matcher, fn)=>{
    if (typeof matcher === 'function') {fn=matcher;matcher=undefined}
    assert(!hooks.has(name));hooks.set(name,fn);matchers.set(name,matcher)
    return {catch(handler){catches.set(name,handler)}}
  })
  const call = async (name,e={}) => hooks.get(name)($,e,async value=>value)
  return {$,hooks,catches,matchers,reads,requests,timers,call,setID:value=>id=value,
    setSurfaces:value=>surfaces=value,setOnRequest:fn=>onRequest=fn}
}
async function settle() { for (let spin=0;spin<100;spin++) await Promise.resolve() }
function assertEnvelopes(f) {
  for (const {action,body} of f.requests) {
    if(action==='pull' || action==='revoke') assert.deepEqual(Object.keys(body).sort(),['nonce','sessionId'])
    if(action==='push') assert.deepEqual(Object.keys(body).sort(),['events','nonce','sequence','sessionId'])
  }
}
const oversized=fixture({oversize:true})
await oversized.call('session.start')
await settle()
assert.equal(oversized.reads.length,0,'oversize file read before stat bound')
assert.equal(oversized.requests.length,0)
const f=fixture()
await f.call('session.start')
await settle()
assert.deepEqual(f.reads,['/fixture/plugin/filo-config.json','/fixture/token'])
assert.equal(f.requests[0].action,'register')
assert(!f.hooks.has('prompt.submit'),'observer must not intercept or submit user input')
const append=f.hooks.get('session.append')
assert.deepEqual(f.matchers.get('session.append'),{door:['prompt','response']},'do not subscribe to managed context or internal notices')
assert.equal(typeof f.catches.get('session.append'),'function','observation needs a native pass-through fallback')
for (const called of [false,true]) {
  let storageCalls=0
  const result={uuid:rowID,message:{type:'assistant',content:[]}}
  const next=async()=>{if(!called)storageCalls++;return result}
  next.called=called
  next.error={kind:called?'throw':'re-entry'}
  assert.equal(await f.catches.get('session.append')(f.$,{},next),result)
  assert.equal(storageCalls,called?0:1,'fallback must preserve native next result reuse')
}
const event={uuid:rowID,message:{type:'assistant',content:[{type:'text',text:'same text'}]}}
await append(f.$,event,async e=>({uuid:e.uuid,message:e.message}))
await f.timers.at(-1).fn()
const batch=f.requests.filter(r=>r.action==='push').at(-1).body
const row=batch.events.find(e=>e.kind==='row')
assert.equal(row.uuid,rowID)
assert.equal(row.turnId,undefined,'no turn assignment by event order')
await f.call('turn.start',{turnId:'native-turn'})
await f.call('turn.start',{turnId:'subagent-turn',agentId:'native-subagent'})
await f.call('turn.complete',{turnId:'subagent-turn',agentId:'native-subagent',isAborted:false})
await f.timers.at(-1).fn()
const mainBatch=f.requests.filter(r=>r.action==='push').at(-1).body.events
assert.deepEqual(mainBatch,[{kind:'start',turnId:'native-turn'},{kind:'snapshot',canStop:true,canInput:true,canSettings:true}], 'subagent event contaminated main runtime')
await f.call('turn.complete',{turnId:'native-turn',isAborted:false})
await f.timers.at(-1).fn()
assert(f.requests.filter(r=>r.action==='push').at(-1).body.events.some(e=>e.kind==='complete'))
await f.call('session.end',{sessionId:sessionA,reason:'clear'})
assert.equal(f.requests.at(-1).action,'revoke')
const oldRegisters=f.requests.filter(r=>r.action==='register').length
await f.timers.at(-1).fn()
assert.equal(f.requests.filter(r=>r.action==='register').length,oldRegisters,'ended ID re-registered before transition')
f.setID(sessionB)
await f.timers.at(-1).fn()
assert.equal(f.requests.filter(r=>r.action==='register').at(-1).body.sessionId,sessionB)
await f.call('session.end',{sessionId:sessionB,reason:'prompt_input_exit'})
assert.equal(f.timers.at(-1).cancelled,true)
assert.equal(f.requests.at(-1).action,'revoke')
assert(f.requests.every(r=>['register','pull','push','revoke'].includes(r.action)))
for (const [sources,surfaces,origin] of [
  [['claude-code'],['terminal'],'terminal'],
  [['claude-desktop'],['desktop'],'desktop'],
  [['claude-desktop'],['terminal'],undefined],
  [['claude-code'],['desktop'],undefined],
  [['claude-code','claude-desktop'],['vscode'],undefined],
  [['claude-code','claude-desktop'],['mobile'],undefined],
  [['claude-code','claude-desktop'],['terminal','desktop'],undefined],
  [['claude-code','claude-desktop'],['desktop','terminal'],undefined],
  [['claude-code','claude-desktop'],['terminal','mobile'],undefined],
  [['claude-code','claude-desktop'],['desktop','vscode'],undefined],
  [['claude-code','claude-desktop'],['terminal','terminal'],undefined],
  [['claude-code','claude-desktop'],[],undefined],
  [[],['terminal'],undefined],
  [['claude-code','claude-code'],['terminal'],undefined],
]) {
  const scoped=fixture({sources,surfaces})
  await scoped.call('session.start')
  await settle()
  const registration=scoped.requests.find(r=>r.action==='register')
  assert.equal(registration?.body.origin,origin,'selected-source boundary must match native surface')
  if (!origin) assert.equal(scoped.requests.length,0)
}
assertEnvelopes(f)
for (const sources of [['claude-code'],['claude-code','claude-desktop']]) {
  const changing=fixture({sources})
  await changing.call('session.start')
  await settle()
  const first=changing.requests.find(r=>r.action==='push').body.nonce
  await changing.call('turn.start',{turnId:'must-not-cross-roster'})
  changing.setSurfaces(['terminal','desktop'])
  let boundary=changing.requests.length
  await changing.timers.at(-1).fn()
  assert.deepEqual(changing.requests.slice(boundary).map(r=>r.action),['revoke'])
  assert.equal(changing.requests.at(-1).body.nonce,first)
  changing.setSurfaces(['desktop'])
  boundary=changing.requests.length
  await changing.timers.at(-1).fn()
  if (sources.includes('claude-desktop')) {
    assert.equal(changing.requests[boundary].action,'register')
    assert.equal(changing.requests[boundary].body.origin,'desktop')
    const pushed=changing.requests.at(-1).body
    assert.notEqual(pushed.nonce,first,'surface switch must acquire a fresh nonce')
    assert.deepEqual(pushed.events,[{kind:'busy'},{kind:'snapshot',canStop:true,canInput:true,canSettings:true}],'old queued events crossed surface binding')
  } else {
    assert.equal(changing.requests.length,boundary,'unselected desktop must not register')
  }
  changing.setSurfaces([])
  boundary=changing.requests.length
  await changing.timers.at(-1).fn()
  assert.deepEqual(changing.requests.slice(boundary).map(r=>r.action),sources.includes('claude-desktop') ? ['revoke'] : [])
  changing.setSurfaces(['terminal'])
  boundary=changing.requests.length
  await changing.timers.at(-1).fn()
  assert.equal(changing.requests[boundary].action,'register')
  assert.equal(changing.requests[boundary].body.origin,'terminal')
  assert.notEqual(changing.requests.at(-1).body.nonce,first,'returning surface must not reuse revoked nonce')
  assertEnvelopes(changing)
}
const direct=fixture()
await direct.call('session.start')
await settle()
const directNonce=direct.requests.at(-1).body.nonce
let boundary=direct.requests.length
direct.setSurfaces(['desktop'])
await direct.timers.at(-1).fn()
assert.deepEqual(direct.requests.slice(boundary).map(r=>r.action),['revoke','register','pull','push'])
assert.notEqual(direct.requests.at(-1).body.nonce,directNonce)
assertEnvelopes(direct)
for (const action of ['register','pull']) {
  const racing=fixture()
  if (action==='register') racing.setOnRequest(name=>{if(name===action) racing.setSurfaces(['terminal','mobile'])})
  await racing.call('session.start')
  await settle()
  if (action==='pull') {
    await racing.call('turn.start',{turnId:'do-not-send-after-attach'})
    racing.setOnRequest(name=>{if(name===action) racing.setSurfaces(['terminal','mobile'])})
    boundary=racing.requests.length
    await racing.timers.at(-1).fn()
    assert.deepEqual(racing.requests.slice(boundary).map(r=>r.action),['pull','revoke'])
  } else {
    assert.deepEqual(racing.requests.map(r=>r.action),['register','revoke'])
  }
  racing.setOnRequest(undefined)
  racing.setSurfaces(['terminal'])
  boundary=racing.requests.length
  await racing.timers.at(-1).fn()
  assert.deepEqual(racing.requests.slice(boundary).map(r=>r.action),['register','pull','push'])
  assert.deepEqual(racing.requests.at(-1).body.events,[{kind:'busy'},{kind:'snapshot',canStop:true,canInput:true,canSettings:true}])
  assertEnvelopes(racing)
}
// Two registrations must retain separate bindings, queues and terminal state.
const isolatedTerminal=fixture({sources:['claude-code']})
const isolatedDesktop=fixture({sources:['claude-desktop'],surfaces:['desktop']})
await isolatedTerminal.call('session.start')
await isolatedDesktop.call('session.start')
await settle()
await isolatedTerminal.call('turn.start',{turnId:'terminal-only'})
await isolatedDesktop.timers.at(-1).fn()
assert.equal(isolatedDesktop.requests.filter(r=>r.action==='push').at(-1).body.events.some(e=>e.turnId==='terminal-only'),false)
await isolatedDesktop.call('session.end',{sessionId:sessionA,reason:'prompt_input_exit'})
await isolatedTerminal.timers.at(-1).fn()
assert(isolatedTerminal.requests.filter(r=>r.action==='push').at(-1).body.events.some(e=>e.turnId==='terminal-only'))
assert.equal(isolatedTerminal.timers.at(-1).cancelled,false)
console.log('official Mod fixtures passed')
`
