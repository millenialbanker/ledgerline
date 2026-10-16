// Package ledgerline embeds the default configuration and the sample trading
// day into the binaries, so the tools work from any folder without needing
// to read the repository from disk.
package ledgerline

import "embed"

// FS holds config/ and testdata/sample/ as shipped with this build.
//
//go:embed config testdata/sample
var FS embed.FS
