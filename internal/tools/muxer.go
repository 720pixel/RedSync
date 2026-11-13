//go:build !avmux

package tools

// Muxer is the Matroska multiplexer RedSync runs for the final mux and for
// track identification (-J). Public builds always use MKVToolNix's mkvmerge.
const Muxer = MkvMerge

// LocalAvmux is true only in a local build made with -tags avmux (see
// muxer_avmux.go). Release builds never set that tag.
const LocalAvmux = false

func localTools() []string { return nil }
