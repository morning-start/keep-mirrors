// Package plugins embeds the curated plugin snapshot that ships with the
// binary.
//
// This is a seed, not a runtime source. km reads plugins from
// <appdata>/keep-mirrors/plugins/{official,user}/ at runtime; the copy here is
// unpacked into official/ once, so a fresh install works offline. After that it
// is never consulted again, which is what lets a registry sync or a user edit
// change behaviour without rebuilding the binary.
//
// The .lua files live in this package rather than at the repository root
// because go:embed cannot reach a parent directory, and having exactly one copy
// on disk removes the "edited the source, forgot to sync the copy" failure mode
// entirely.
package plugins

import "embed"

// FS holds every *.lua file in this package.
//
//go:embed *.lua
var FS embed.FS
