package main

import (
	"log/slog"
	"runtime/debug"
	"strings"
)

// These values are overridden by the build with -ldflags -X. The defaults keep
// locally built binaries and source archives identifiable when no build
// metadata was supplied.
var (
	buildCommit  = "unknown"
	buildVersion = ""
)

type buildIdentity struct {
	commit  string
	version string
}

func currentBuildIdentity() buildIdentity {
	var settings []debug.BuildSetting
	if info, ok := debug.ReadBuildInfo(); ok {
		settings = info.Settings
	}
	return resolveBuildIdentity(buildCommit, buildVersion, settings)
}

func resolveBuildIdentity(linkerCommit, linkerVersion string, settings []debug.BuildSetting) buildIdentity {
	commit := strings.TrimSpace(linkerCommit)
	if commit == "" || commit == "unknown" {
		for _, setting := range settings {
			if setting.Key == "vcs.revision" {
				if revision := strings.TrimSpace(setting.Value); revision != "" {
					commit = revision
					break
				}
			}
		}
	}
	if commit == "" {
		commit = "unknown"
	}

	return buildIdentity{
		commit:  commit,
		version: strings.TrimSpace(linkerVersion),
	}
}

func logBuildInfo(logger *slog.Logger) {
	logBuildIdentity(logger, currentBuildIdentity())
}

func logBuildIdentity(logger *slog.Logger, identity buildIdentity) {
	attrs := []any{"commit", identity.commit}
	if identity.version != "" {
		attrs = append(attrs, "version", identity.version)
	}
	logger.Info("build information", attrs...)
}

type buildView struct {
	Version string
	Commit  string
}

func buildViewFor(identity buildIdentity) buildView {
	version := identity.version
	if version == "" {
		version = "dev"
	}
	return buildView{
		Version: version,
		Commit:  identity.commit,
	}
}
