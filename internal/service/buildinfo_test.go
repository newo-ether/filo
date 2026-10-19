package service

import (
	"encoding/json"
	"net/http"
	"reflect"
	"runtime/debug"
	"strings"
	"testing"
)

func TestBuildInfoDistinguishesUnknownCleanAndModifiedSources(t *testing.T) {
	revision := strings.Repeat("a", 40)
	injected := strings.Repeat("b", 40)
	clean, dirty := false, true
	for _, fixture := range []struct {
		name     string
		settings []debug.BuildSetting
		revision string
		modified string
		want     buildInfo
	}{
		{name: "unknown", want: buildInfo{Component: "gateway", Source: "unknown"}},
		{name: "invalid injection", revision: "dev/private-path", modified: "false", want: buildInfo{Component: "gateway", Source: "unknown"}},
		{name: "injected release", revision: injected, modified: "false", want: buildInfo{Component: "gateway", Revision: injected, Modified: &clean, Source: "injected"}},
		{name: "injected unspecified state", revision: injected, want: buildInfo{Component: "gateway", Revision: injected, Source: "injected"}},
		{name: "clean vcs", settings: []debug.BuildSetting{{Key: "vcs.revision", Value: revision}, {Key: "vcs.modified", Value: "false"}}, want: buildInfo{Component: "gateway", Revision: revision, Modified: &clean, Source: "vcs"}},
		{name: "dirty vcs overrides release", settings: []debug.BuildSetting{{Key: "vcs.revision", Value: revision}, {Key: "vcs.modified", Value: "true"}}, revision: injected, modified: "false", want: buildInfo{Component: "gateway", Revision: revision, Modified: &dirty, Source: "vcs"}},
		{name: "dirty vcs without revision", settings: []debug.BuildSetting{{Key: "vcs.modified", Value: "true"}}, revision: injected, modified: "false", want: buildInfo{Component: "gateway", Revision: injected, Modified: &dirty, Source: "injected"}},
		{name: "vcs missing state", settings: []debug.BuildSetting{{Key: "vcs.revision", Value: revision}}, revision: injected, modified: "false", want: buildInfo{Component: "gateway", Revision: revision, Source: "vcs"}},
		{name: "invalid vcs state", settings: []debug.BuildSetting{{Key: "vcs.modified", Value: "unknown"}}, revision: injected, modified: "false", want: buildInfo{Component: "gateway", Revision: injected, Source: "injected"}},
		{name: "sha256 revision", revision: strings.Repeat("c", 64), modified: "true", want: buildInfo{Component: "gateway", Revision: strings.Repeat("c", 64), Modified: &dirty, Source: "injected"}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			var info *debug.BuildInfo
			if fixture.settings != nil {
				info = &debug.BuildInfo{Settings: fixture.settings}
			}
			got := readBuildInfo(info, fixture.revision, fixture.modified, "gateway")
			if !reflect.DeepEqual(got, fixture.want) {
				t.Fatalf("build identity = %+v, want %+v", got, fixture.want)
			}
		})
	}
}

func TestBuildInfoDoesNotExposeBuildSettingsOrPaths(t *testing.T) {
	private := "private-token-or-workspace-path"
	info := &debug.BuildInfo{Path: private, Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: private}, {Key: "-ldflags", Value: private},
		{Key: "unrelated", Value: private},
	}}
	wire := string(Marshal(readBuildInfo(info, "", "", "helper")))
	if strings.Contains(wire, private) || wire != `{"component":"helper","modified":null,"source":"unknown"}` {
		t.Fatalf("unknown build leaked data or claimed clean state: %s", wire)
	}
}

func TestInfoReportsBuildComponentWithoutNativeOperations(t *testing.T) {
	for _, component := range []string{"gateway", "helper"} {
		t.Run(component, func(t *testing.T) {
			sessions := newFakeSessions(1)
			options := ServerOptions{Sessions: sessions}
			if component == "helper" {
				options.Identity = &ServiceIdentity{SessionMode: "standalone", Device: "host"}
			}
			server := newTestServer(t, options)
			var info map[string]any
			decode(t, get(t, server.URL+"/v1/info", authHeaders()), &info)
			assertBuildIdentity(t, info["build"], component)
			if info["protocolVersion"] != float64(2) {
				t.Fatalf("protocol version was conflated with build identity: %v", info)
			}
			if reads, attaches, mutations := sessions.counts(); reads != 0 || attaches != 0 || mutations != 0 {
				t.Fatalf("info touched native state: %d/%d/%d", reads, attaches, mutations)
			}
			response := get(t, server.URL+"/v1/info", nil)
			defer response.Body.Close()
			if response.StatusCode != http.StatusUnauthorized {
				t.Fatalf("anonymous info status = %d", response.StatusCode)
			}
		})
	}
}

func TestCurrentBuildInfoUsesLinkerMetadata(t *testing.T) {
	if buildRevision == "" {
		t.Skip("linker metadata is tested by the focused -ldflags invocation")
	}
	info, _ := debug.ReadBuildInfo()
	if info != nil {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				t.Skip("native VCS metadata takes precedence over linker fallback")
			}
		}
	}
	got := currentBuildInfo("gateway")
	if got.Source != "injected" || got.Revision != buildRevision || got.Modified == nil || *got.Modified != (buildModified == "true") {
		t.Fatalf("linker metadata not used: %+v", got)
	}
}

func assertBuildIdentity(t *testing.T, value any, component string) {
	t.Helper()
	wire, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var got buildInfo
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, currentBuildInfo(component)) {
		t.Fatalf("build identity = %s, want component %s and local build identity", wire, component)
	}
	var fields map[string]any
	if json.Unmarshal(wire, &fields) != nil || len(fields) < 3 || len(fields) > 4 {
		t.Fatalf("invalid build record: %s", wire)
	}
	for key := range fields {
		if key != "component" && key != "revision" && key != "modified" && key != "source" {
			t.Fatalf("unexpected build field %q", key)
		}
	}
}
