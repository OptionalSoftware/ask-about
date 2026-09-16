// Package askabout is the root of the module. It holds nothing but the files
// compiled into every ask-about binary — the UI, the sample document, the
// personas, the interview prompts — so that any program built on this
// module, not only the one in cmd/ask-about, ships the same ones.
//
// They are embedded here rather than in cmd/ask-about because go:embed
// cannot reach outside its own package directory, and these files live at
// the repository root.
package askabout

import (
	"embed"
	"io/fs"
)

//go:embed all:web
var web embed.FS

// Web is the UI: index.html, the scripts, the stylesheet.
func Web() fs.FS { return mustSub(web, "web") }

// SampleDocument is the fictional document the binary answers from when no
// other is supplied.
//
//go:embed docs/content.md
var SampleDocument string

// The personas, one per kind of subject.
//
//go:embed prompts/person.md
var PersonPersona string

//go:embed prompts/product.md
var ProductPersona string

//go:embed prompts/interview-*.md
var prompts embed.FS

// Prompts is the interview prompts the admin page hands out, one file per
// entry in the server's catalogue.
func Prompts() fs.FS { return mustSub(prompts, "prompts") }

// mustSub roots an embedded filesystem at dir. The directory is a
// compile-time literal, so a failure here is a build mistake, not a runtime
// condition.
func mustSub(f embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return sub
}
