//go:build !embedui

package webui

import "io/fs"

// FS is nil without -tags embedui: the API runs alone and the UI is hosted elsewhere
// (Cloudflare Pages, Vercel) or by `npm run dev`.
func FS() fs.FS { return nil }
