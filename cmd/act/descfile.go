package main

import (
	"fmt"
	"io"
	"os"

	"github.com/aac/act/internal/op"
)

// loadDescriptionFile reads a description payload from path for
// --description-file. See loadDescriptionPayload for the mechanics; this
// is the flag-named wrapper the replace path uses.
func loadDescriptionFile(path string) (string, int, map[string]any) {
	return loadDescriptionPayload("--description-file", path)
}

// loadDescriptionPayload reads a description payload from path. If path is
// "-", stdin is consumed instead. Returns (contents, exitCode, errorEnv).
// On success exitCode is 0 and errorEnv is nil. On failure exitCode is 2
// (bad flag / oversize / read error) or 3 (file missing) and errorEnv is
// the structured envelope ready to be passed to emitEnvelope.
//
// The cap is op.MaxDescriptionLen, the one description cap, which the op
// payload validators enforce on every write (act-993498). Checking it here
// too lets the reader stop one byte past the limit (via io.LimitReader),
// telling "exactly at the cap" from "over it" without pulling an
// arbitrarily large file into memory, and lets the error name the flag.
//
// flagName names the flag in every error message so --description-file
// and --description-append-file each report themselves rather than one
// impersonating the other (act-0bec25).
func loadDescriptionPayload(flagName, path string) (string, int, map[string]any) {
	var (
		r       io.Reader
		closer  io.Closer
		display = path
	)
	if path == "-" {
		r = os.Stdin
		display = "<stdin>"
	} else {
		f, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				return "", 3, map[string]any{
					"error":   "file_not_found",
					"message": fmt.Sprintf("%s %q: file not found", flagName, path),
				}
			}
			return "", 2, map[string]any{
				"error":   "bad_flag",
				"message": fmt.Sprintf("%s %q: %v", flagName, path, err),
			}
		}
		r = f
		closer = f
	}
	if closer != nil {
		defer func() { _ = closer.Close() }()
	}

	buf, err := io.ReadAll(io.LimitReader(r, op.MaxDescriptionLen+1))
	if err != nil {
		return "", 2, map[string]any{
			"error":   "bad_flag",
			"message": fmt.Sprintf("%s %s: read: %v", flagName, display, err),
		}
	}
	if len(buf) > op.MaxDescriptionLen {
		return "", 2, map[string]any{
			"error": "bad_flag",
			"message": fmt.Sprintf(
				"%s %s: content exceeds %d-byte description cap",
				flagName, display, op.MaxDescriptionLen,
			),
		}
	}
	return string(buf), 0, nil
}
