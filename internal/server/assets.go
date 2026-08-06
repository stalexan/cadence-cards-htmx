package server

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"

	"cadence-cards/web"
)

// assetVersion fingerprints the embedded static assets.
//
// Embedded files carry a zero mod-time, so net/http emits neither Last-Modified
// nor ETag for them. Without a fingerprint in the URL the browser has no
// validator at all and will serve a stale app.css straight from disk cache for
// the whole Cache-Control lifetime — which, combined with go:embed requiring a
// rebuild per edit, makes CSS changes look like they never landed.
//
// The hash covers file paths as well as contents so a rename alone still moves
// it. fs.WalkDir visits in lexical order, so the result is deterministic.
func assetVersion() (string, error) {
	h := sha256.New()
	err := fs.WalkDir(web.Static, "static", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := fs.ReadFile(web.Static, path)
		if err != nil {
			return err
		}
		h.Write([]byte(path))
		h.Write(b)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:8], nil
}
