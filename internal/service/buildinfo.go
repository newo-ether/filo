package service

import (
	"encoding/hex"
	"runtime/debug"
)

// Release builds inject these when source paths and VCS metadata are trimmed.
// Runtime VCS settings take precedence, including evidence of local changes.
var buildRevision, buildModified string

type buildInfo struct {
	Component string `json:"component"`
	Revision  string `json:"revision,omitempty"`
	Modified  *bool  `json:"modified"`
	Source    string `json:"source"`
}

func currentBuildInfo(component string) buildInfo {
	info, _ := debug.ReadBuildInfo()
	return readBuildInfo(info, buildRevision, buildModified, component)
}

func readBuildInfo(info *debug.BuildInfo, revision, modified, component string) buildInfo {
	result := buildInfo{Component: component, Source: "unknown"}
	vcsModified := false
	if info != nil {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				if validBuildRevision(setting.Value) {
					result.Revision, result.Source = setting.Value, "vcs"
				}
			case "vcs.modified":
				vcsModified = true
				result.Modified = buildModifiedValue(setting.Value)
			}
		}
	}
	if result.Revision == "" && validBuildRevision(revision) {
		result.Revision, result.Source = revision, "injected"
	}
	if !vcsModified && result.Source == "injected" {
		result.Modified = buildModifiedValue(modified)
	}
	return result
}

func validBuildRevision(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func buildModifiedValue(value string) *bool {
	if value != "true" && value != "false" {
		return nil
	}
	modified := value == "true"
	return &modified
}
