package main

import (
	"fmt"
	"io"
	"runtime/debug"
)

var version = "dev"

func printVersion(w io.Writer) {
	info, ok := debug.ReadBuildInfo()
	fmt.Fprintln(w, versionString(version, info, ok))
}

func versionString(injected string, info *debug.BuildInfo, haveInfo bool) string {
	if injected != "dev" {
		return injected
	}
	if !haveInfo || info == nil {
		return "dev"
	}
	revision, dirty := "", false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}
	if revision == "" && !dirty && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	result := "dev"
	if revision != "" {
		if len(revision) > 12 {
			revision = revision[:12]
		}
		result += "+" + revision
	}
	if dirty {
		result += "-dirty"
	}
	return result
}
