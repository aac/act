// Package redaction removes strings from stored op files by re-issuing them
// (docs/redaction-design.md §3–§5).
//
// A redaction never edits a file in place. For each op file that holds a
// listed string it writes a re-issued op — the same envelope, the string
// replaced by Replacement in the payload's text fields, and one new payload
// key, "redacted": N, the generation — under the file name the new content
// hash gives, then deletes the old file. Among op files that share a slot
// (issue_id, op_type, hlc.wall, hlc.logical, node_id), the highest
// generation is the op (§4), so a checkout holding the old and the new file
// side by side folds exactly like one holding only the new.
//
// The package holds the pure parts of the operation: reading a patterns file
// (LoadPatterns), rewriting one op (Apply, Reissue), planning a pass over a
// state root (NewPlan), carrying it out (Plan.Execute) and the raw
// verification scan (Scan). It has no git and no CLI in it: locking,
// fetching, the record ops, staging, committing and publishing belong to the
// command that drives it.
//
// Nothing this package returns — an error, a residual, a count — contains a
// pattern or the text around a match. Paths appear, because the person has
// to go and look.
//
// Named redaction, not redact: internal/gitops/redact.go already owns that
// word for URL-credential masking.
package redaction
