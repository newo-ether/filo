package claude

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise the native plugin dispatcher and its PreToolUse boundary without a
// session, real MCP server, tool.call substitute, or forged SDK-positive case.
func TestOfficialModNativeGuardHost(t *testing.T) {
	engine := nativeModTestEngine(t)
	plugin := t.TempDir()
	for _, relative := range []string{".claude-plugin/plugin.json", "hooks/hooks.json", "hooks/register.js", "hooks/desktop.mjs"} {
		data, err := os.ReadFile(filepath.Join("mod", filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(plugin, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Read is directed at a disposable synthetic file, never user data. The
	// PreToolUse guard denies unconditionally before any native tool body.
	path := filepath.Join(plugin, "guard-target.txt")
	if err := os.WriteFile(path, []byte("synthetic guard fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixture := strings.ReplaceAll(nativeGuardHostFixture, "__FIXTURE_FILE__", filepath.ToSlash(path))
	if err := os.WriteFile(filepath.Join(plugin, "guard.test.ts"), []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := nativeGuardHostRun(engine, plugin)
	t.Logf("%s", output)
	if err != nil {
		t.Fatalf("native guard fixture failed: %v", err)
	}
	if !strings.Contains(string(output), "5 pass") || !strings.Contains(string(output), "0 fail") {
		t.Fatal("native guard fixture did not execute all five checks")
	}
}

func nativeGuardHostRun(engine, plugin string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, engine, "plugin", "test", plugin)
	command.SysProcAttr = nativeProcessAttributes()
	return command.CombinedOutput()
}

const nativeGuardHostFixture = `
import { expect, test } from 'claude-code/testing'

const caller = {
  name: 'guard-frame-caller',
  register(on) {
    on('tool.call', { tool: ['mcp__guard_fixture__outer', 'mcp__guard_fixture__inner'] }, async ($, e, next) => {
      if (e.tool === 'mcp__guard_fixture__inner')
        return { deny: 'calling frame was incorrectly reentered' }
      if (e.tool !== 'mcp__guard_fixture__outer') return next(e)
      const result = await $.tool.call({ tool: 'mcp__guard_fixture__inner' })
      return { deny: 'outer: ' + result.deny }
    })
    on('tool.call', { tool: 'mcp__guard_fixture__inner' }, async ($, e, next) => {
      const result = await next(e)
      return { deny: 'sibling: ' + result.deny }
    })
  },
}
const leaf = {
  name: 'guard-frame-leaf',
  register(on) {
    on('tool.call', async ($, e, next) => {
      if (e.tool !== 'mcp__guard_fixture__inner') return next(e)
      return { deny: 'leaf sentinel; no core call' }
    })
  },
}

test('native dispatcher skips only the calling frame and retains sibling and other plugin hooks',
  { plugins: [caller, leaf] }, async ($) => {
    const result = await $.tool.call({ tool: 'mcp__guard_fixture__outer' })
    expect(result.deny).toBe('outer: sibling: leaf sentinel; no core call')
  })

const guard = {
  name: 'guard-core-boundary',
  register(on) {
    on('classic.PreToolUse', ($, e) => {
      // Refuse every call. Mod hooks receive the flat tool envelope, so a
      // caller-supplied mcp_server is an argument, not trusted provenance.
      return { deny: 'NATIVE_GUARD_REFUSED ' + JSON.stringify({
        tool: e.tool,
        claimed: e.mcp_server ?? null,
        toolUseId: e.tool_use_id,
      }) }
    })
  },
}

for (const tool of ['Read', 'mcp__ccd_session__get_session']) {
  for (const spoofed of [false, true]) {
    test('native PreToolUse denies ' + tool + ' with ' + (spoofed ? 'spoofed SDK arguments' : 'no provenance claim'),
      { plugins: [guard] }, async ($) => {
        const claimed = { name: 'ccd_session', source: 'sdk' }
        const input = { tool, file_path: '__FIXTURE_FILE__' }
        if (spoofed) Object.assign(input, { source: 'sdk', mcp_server: claimed })
        const result = await $.tool.call(input)
        expect(result.isError).toBe(true)
        expect(result.text.startsWith('NATIVE_GUARD_REFUSED ')).toBe(true)
        const evidence = JSON.parse(result.text.slice('NATIVE_GUARD_REFUSED '.length))
        expect(evidence.tool).toBe(tool)
        expect(evidence.claimed).toEqual(spoofed ? claimed : null)
        expect(typeof evidence.toolUseId).toBe('string')
        expect(evidence.toolUseId.length > 0).toBe(true)
      })
  }
}
`
