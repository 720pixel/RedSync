//go:build avmux

package tools

// LOCAL ONLY. This file is compiled solely with -tags avmux, which the
// developer's gitignored local.mk adds to `make build`. It swaps mkvmerge for
// avmux on the developer's own machine. Public releases (make linux / windows /
// release, CI) never pass the tag and always mux with MKVToolNix.

// Avmux is a fast, multi-threaded mkvmerge-compatible Matroska multiplexer.
const Avmux = "avmux"

// Muxer is the Matroska multiplexer RedSync runs for the final mux and for
// track identification (-J).
const Muxer = Avmux

// LocalAvmux marks a local avmux build.
const LocalAvmux = true

// avmux can't shift chapters itself, so a local build still needs mkvextract
// and mkvpropedit to move them after the mux.
func localTools() []string { return []string{Avmux} }
