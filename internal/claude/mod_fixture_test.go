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
	script := string(source) + modFixtureScript
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
  let id = sessionA, nonce = 'a'.repeat(43), seq = 0
  const config = JSON.stringify({url:'http://127.0.0.1:7438/claude-mod/v1/', tokenFile:'/fixture/token'})
  const files = new Map([['/fixture/config',config],['/fixture/token','b'.repeat(64)]])
  const $ = {
    env: {get: async name => name === 'FILO_CLAUDE_CONFIG' ? '/fixture/config' : undefined},
    fs: {
      stat: async path => ({kind:'file', size: options.oversize ? 99999 : files.get(path).length,
        isLink: false, mtimeMs:1, realPath:path}),
      read: async path => { reads.push(path); return files.get(path) },
    },
    session: {id:async()=>id, surfaces:async()=>['terminal'], version:async()=>({version:'fixture'}), cwd:async()=>'/fixture'},
    clock: {every:(ms, fn)=>{const timer={fn,cancelled:false,cancel(){this.cancelled=true}};timers.push(timer);return timer}},
    http: {fetch:async (url, request)=>{
      assert.equal(request.headers.Authorization, 'Bearer '+'b'.repeat(64))
      const action = url.split('/').at(-1), body=JSON.parse(request.body)
      requests.push({action,body})
      let response = {readOnly:true,ttlMillis:15000,historyBoundary:'observed-since-registration'}
      if(action==='register') {seq=0;response.nonce=nonce}
      if(action==='push') {assert.equal(body.nonce,nonce);assert.equal(body.sequence,seq+1);seq++;response.sequence=seq}
      return {ok:true,text:JSON.stringify(response)}
    }},
  }
  register((name, fn)=>{assert(!hooks.has(name));hooks.set(name,fn)})
  const call = async (name,e={}) => hooks.get(name)($,e,async value=>value)
  return {$,hooks,reads,requests,timers,call,setID:value=>id=value}
}
const oversized=fixture({oversize:true})
await oversized.call('session.start')
for (let spin=0;spin<40;spin++) await Promise.resolve()
assert.equal(oversized.reads.length,0,'oversize file read before stat bound')
assert.equal(oversized.requests.length,0)
const f=fixture()
await f.call('session.start')
for (let spin=0;spin<40;spin++) await Promise.resolve()
assert.deepEqual(f.reads,['/fixture/config','/fixture/token'])
assert.equal(f.requests[0].action,'register')
assert(!f.hooks.has('prompt.submit'),'observer must not intercept or submit user input')
const append=f.hooks.get('session.append')
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
assert.deepEqual(mainBatch,[{kind:'start',turnId:'native-turn'}], 'subagent event contaminated main runtime')
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
console.log('official Mod fixtures passed')
`
