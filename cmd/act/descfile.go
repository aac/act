package main

import (
	"fmt"
	"io"
	"os"
)

// maxDescriptionBytes mirrors the schema's 16384-char description cap
// (spec.md §Data model: description is "0..16384 chars"). The check
// is byte-based for parity with the other write-side length checks in
// internal/op/payloads.go (title <= 200, accept[i] <= 500), all of
// which use len() rather than a rune count.
const maxDescriptionBytes = 16384

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
// The cap is enforced by reading one byte past the limit (via
// io.LimitReader) so we can distinguish "exactly 16384 bytes" from
// "more than 16384 bytes" without pulling a multi-megabyte file into
// memory.
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

	buf, err := io.ReadAll(io.LimitReader(r, maxDescriptionBytes+1))
	if err != nil {
		return "", 2, map[string]any{
			"error":   "bad_flag",
			"message": fmt.Sprintf("%s %s: read: %v", flagName, display, err),
		}
	}
	if len(buf) > maxDescriptionBytes {
		return "", 2, map[string]any{
			"error": "bad_flag",
			"message": fmt.Sprintf(
				"%s %s: content exceeds %d-char description limit",
				flagName, display, maxDescriptionBytes,
			),
		}
	}
	return string(buf), 0, nil
}
