package cli

// Doc-vs-implementation sweep test (act-ff5c).
//
// This is the lightest-weight option from the issue: a registry of
// (doc surface, claim pattern, asserting-test name) tuples plus a Go
// test that, on every `go test ./...` invocation, verifies:
//
//   1. The claim pattern is present in the named doc file (drift on
//      the doc side: if someone removes "prefix ok" from a flag-help
//      string, the registry entry is now lying about what the doc says
//      and the test fails — either re-introduce the claim, or delete
//      the registry entry).
//
//   2. A test function with the named `TestDocClaim_*` symbol exists
//      somewhere in the test corpus (drift on the test side: if the
//      asserting test is deleted or renamed without updating the
//      registry, the claim has lost its enforcement and this test
//      surfaces that).
//
// What this catches: the act-6fca and act-ac52 shape — doc claim and
// implementation drift apart with no automated signal. What it does
// NOT catch: a TestDocClaim_X that exists but doesn't actually assert
// anything meaningful. That's a code-review concern; the sweep is
// for orphan-detection.
//
// Scope: ~300 LoC ceiling. The registry is hand-maintained. The
// alternative (static analyzer extracting claims from prose) would
// surface every English sentence in every doc as a candidate; the
// false-positive rate makes it useless.
//
// To add a new tracked claim: append a `docClaim` entry below AND
// write a matching `TestDocClaim_*` test in docclaim_test.go (or
// another *_test.go in either internal/cli/ or cmd/act/).

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// docClaim describes one user-visible behavior claim. The claim is
// "tracked" in the sense that two layers of drift (doc edits, test
// edits) become a build break.
type docClaim struct {
	// name is a short identifier used only in test failure messages.
	name string

	// docFile is the doc surface relative to the repo root.
	docFile string

	// claimPattern is a literal substring that must appear in docFile.
	// We use literal string match (not regex) to keep the registry
	// readable; if a regex is needed, prefer adding a new tuple over
	// generalising this struct.
	claimPattern string

	// testName is the symbol of the asserting test. Must start with
	// "TestDocClaim_" to make the convention searchable; the sweep
	// rejects entries that don't.
	testName string
}

// docClaimRegistry is the source of truth for tracked claims. New
// claims go here in the same commit that adds the doc edit and the
// asserting test.
//
// Order is alphabetical by `name` for readability; the sweep does not
// depend on it.
var docClaimRegistry = []docClaim{
	// act-675b59: fifth stale pass over docs/spec.md. Nine per-command
	// "Exit codes" lines promised exit 4 on version skew; only
	// `act version --check-repo` reports version_skew. Each line now lists
	// what its command returns, asserted against the real binary in
	// cmd/act/spec_pass5_docclaim_test.go. Three rows of the `act help
	// errors` table described codes by something other than what emits
	// them. Drift shape: a line regains "4 on skew", or loses a code the
	// command returns, and the test named here goes red.
	{
		name:         "spec-exit-codes-create-hook-refusal",
		docFile:      "docs/spec.md",
		claimPattern: "1 if the `create` hook refuses the create (`hook_failed`)",
		testName:     "TestDocClaim_ExitCodes_Create",
	},
	{
		name:         "spec-exit-codes-create-never-4",
		docFile:      "docs/spec.md",
		claimPattern: "`act create` never exits 4: an op from a newer writer already in the tracker does not stop the write",
		testName:     "TestDocClaim_ExitCodes_Create",
	},
	{
		name:         "spec-exit-codes-list-no-state-exits-0",
		docFile:      "docs/spec.md",
		claimPattern: "`act list` prints the no-state line and exits 0",
		testName:     "TestDocClaim_ExitCodes_List",
	},
	{
		name:         "spec-exit-codes-list-never-4",
		docFile:      "docs/spec.md",
		claimPattern: "`act list` never exits 4: an op with a newer `writer_version` is applied like any other",
		testName:     "TestDocClaim_ExitCodes_List",
	},
	{
		name:         "spec-exit-codes-show-fold-failed-is-3",
		docFile:      "docs/spec.md",
		claimPattern: "(`fold_failed` — an op with an unknown `op_version` lands here)",
		testName:     "TestDocClaim_ExitCodes_Show",
	},
	{
		name:         "spec-exit-codes-show-never-4",
		docFile:      "docs/spec.md",
		claimPattern: "`act show` never exits 4: an op with a newer `writer_version` is applied like any other",
		testName:     "TestDocClaim_ExitCodes_Show",
	},
	{
		name:         "spec-exit-codes-close-already-closed-is-0",
		docFile:      "docs/spec.md",
		claimPattern: "0, including a close of an issue that is already closed (no op is written; `--json` answers `already_closed: true`)",
		testName:     "TestDocClaim_ExitCodes_Close",
	},
	{
		name:         "spec-exit-codes-close-never-4",
		docFile:      "docs/spec.md",
		claimPattern: "`act close` never exits 4: an op from a newer writer already in the tracker does not stop the close",
		testName:     "TestDocClaim_ExitCodes_Close",
	},
	{
		name:         "spec-exit-codes-ready-mine-config-unreadable",
		docFile:      "docs/spec.md",
		claimPattern: "when `--mine` cannot read `.act/config.json`",
		testName:     "TestDocClaim_ExitCodes_Ready",
	},
	{
		name:         "spec-exit-codes-ready-never-4",
		docFile:      "docs/spec.md",
		claimPattern: "`act ready` never exits 4: an op with a newer `writer_version` is applied like any other",
		testName:     "TestDocClaim_ExitCodes_Ready",
	},
	{
		name:         "spec-exit-codes-search-query-is-literal",
		docFile:      "docs/spec.md",
		claimPattern: "FTS5 operators and unbalanced quotes are searched for literally, not rejected as syntax",
		testName:     "TestDocClaim_ExitCodes_Search",
	},
	{
		name:         "spec-exit-codes-search-index-failure-is-3",
		docFile:      "docs/spec.md",
		claimPattern: "the failures `act list` reports as exit 1 are exit 3 here",
		testName:     "TestDocClaim_ExitCodes_Search",
	},
	{
		name:         "spec-exit-codes-search-never-4",
		docFile:      "docs/spec.md",
		claimPattern: "`act search` never exits 4: an op with a newer `writer_version` is indexed like any other",
		testName:     "TestDocClaim_ExitCodes_Search",
	},
	{
		name:         "spec-exit-codes-log-unparseable-op-is-3",
		docFile:      "docs/spec.md",
		claimPattern: "(`ops_read_failed` — an op with an unknown `op_version` lands here)",
		testName:     "TestDocClaim_ExitCodes_Log",
	},
	{
		name:         "spec-exit-codes-log-never-4",
		docFile:      "docs/spec.md",
		claimPattern: "`act log` never exits 4: an op with a newer `writer_version` is printed like any other",
		testName:     "TestDocClaim_ExitCodes_Log",
	},
	{
		name:         "spec-exit-codes-mcp-missing-repo-is-a-tool-error",
		docFile:      "docs/spec.md",
		claimPattern: "a tool call that needs tracker state answers with an error result (`no_repo`) while the server keeps running",
		testName:     "TestDocClaim_ExitCodes_MCP",
	},
	{
		name:         "spec-exit-codes-mcp-never-3-or-4",
		docFile:      "docs/spec.md",
		claimPattern: "The `act mcp` process never exits 3 or 4",
		testName:     "TestDocClaim_ExitCodes_MCP",
	},
	{
		name:         "spec-exit-codes-version-check-repo-resolves-repo-root",
		docFile:      "docs/spec.md",
		claimPattern: "the check resolves the repo root from the working directory like the other read commands",
		testName:     "TestDocClaim_ExitCodes_Version",
	},
	{
		name:         "spec-exit-codes-version-only-reporter-of-skew",
		docFile:      "docs/spec.md",
		claimPattern: "`act version --check-repo` is the only command that reports `version_skew`",
		testName:     "TestDocClaim_ExitCodes_Version",
	},
	{
		// The write half of "only command that reports version_skew" was
		// already asserted by the third pass for update and close.
		name:         "spec-exit-codes-version-skew-does-not-refuse-writes",
		docFile:      "docs/spec.md",
		claimPattern: "4 on skew, meaning some op file carries a `writer_version` newer than this binary's (`version_skew`)",
		testName:     "TestDocClaim_NewerWriterOpDoesNotBlockWritesAndNothingSquashes",
	},
	{
		name:         "help-errors-version-skew-row",
		docFile:      "cmd/act/help.go",
		claimPattern: "reported only by 'act version --check-repo' (exit 4)",
		testName:     "TestDocClaim_HelpErrors_VersionSkewHookFailedNoRepoRows",
	},
	{
		name:         "help-errors-hook-failed-row",
		docFile:      "cmd/act/help.go",
		claimPattern: "a .act/hooks/<event> script exited non-zero or timed out",
		testName:     "TestDocClaim_HelpErrors_VersionSkewHookFailedNoRepoRows",
	},
	{
		name:         "help-errors-no-repo-row",
		docFile:      "cmd/act/help.go",
		claimPattern: "no .act/ where the command looked (or, from an MCP",
		testName:     "TestDocClaim_HelpErrors_VersionSkewHookFailedNoRepoRows",
	},
	// act-0be17c: the spec said an unknown op_type hard-fails the fold
	// (E_UNKNOWN_OP); the fold has skipped such ops since act-20e87c. The
	// corrected claim is asserted on the real read path, and the op_type
	// enum block is pinned to op.ValidOpTypes so it cannot drift again.
	{
		name:         "spec-fold-unknown-op-type-skipped",
		docFile:      "docs/spec.md",
		claimPattern: "An op with an unrecognised op_type is skipped by the fold, not a hard failure",
		testName:     "TestDocClaim_Fold_UnknownOpTypeIsSkippedNotFatal",
	},
	{
		name:         "spec-op-type-enum-matches-valid-op-types",
		docFile:      "docs/spec.md",
		claimPattern: "`op_type` enum (all of v1):",
		testName:     "TestDocClaim_Spec_OpTypeEnumMatchesValidOpTypes",
	},
	// act-549a7e: second stale pass over the spec. The create and close
	// Behavior lines said the op file is written and THEN the hook runs,
	// against the Hooks contract and the code; three places named a
	// `dep-add` op (and one a `set-status` op) that no writer emits; the
	// fold pseudocode named two errors the code never had; and the golden
	// paragraph required cases under a directory that does not exist. The
	// close line reuses the act-8ee085 test; the rest are new.
	{
		name:         "spec-create-hook-runs-before-op-write",
		docFile:      "docs/spec.md",
		claimPattern: "The `create` hook runs before the create op file is written",
		testName:     "TestDocClaim_CreateHookRunsBeforeOpFileIsWritten",
	},
	{
		name:         "spec-create-no-commit-runs-no-hook",
		docFile:      "docs/spec.md",
		claimPattern: "Under `--no-commit` the op file is written, no hook runs and nothing is committed",
		testName:     "TestDocClaim_CreateHookRunsBeforeOpFileIsWritten",
	},
	{
		name:         "spec-close-hook-runs-before-op-write",
		docFile:      "docs/spec.md",
		claimPattern: "The `close` hook runs before the close op file is written",
		testName:     "TestDocClaim_HookRunsBeforeOpFileIsWritten",
	},
	{
		name:         "spec-dep-add-writes-add-dep-op",
		docFile:      "docs/spec.md",
		claimPattern: "Writes one `add_dep` op on the child issue",
		testName:     "TestDocClaim_DepAdd_WritesOneAddDepOp",
	},
	{
		name:         "spec-act-block-tool-writes-one-add-dep-op",
		docFile:      "docs/spec.md",
		claimPattern: "write one `add_dep` op with `type=blocks` from `id` to `blocked_by`",
		testName:     "TestDocClaim_MCP_BlockWritesOneAddDepOpInOneCommit",
	},
	{
		name:         "spec-act-block-single-op-single-commit",
		docFile:      "docs/spec.md",
		claimPattern: "`act_block` writes exactly one op — an `add_dep` op with `type=blocks` — and no status op",
		testName:     "TestDocClaim_MCP_BlockWritesOneAddDepOpInOneCommit",
	},
	{
		name:         "spec-fold-schema-version-mismatch-aborts",
		docFile:      "docs/spec.md",
		claimPattern: "An op whose schema_version is not the one this binary writes aborts the fold",
		testName:     "TestDocClaim_Fold_VersionFieldsOnRead",
	},
	{
		name:         "spec-fold-newer-writer-version-not-fatal",
		docFile:      "docs/spec.md",
		claimPattern: "a newer writer_version does not stop the fold",
		testName:     "TestDocClaim_Fold_VersionFieldsOnRead",
	},
	{
		name:         "spec-golden-case-list-matches-fixtures",
		docFile:      "docs/spec.md",
		claimPattern: "Golden cases:",
		testName:     "TestDocClaim_Spec_GoldenCaseListMatchesFixtures",
	},
	// act-9c9a85: third stale pass over the spec. It described a
	// writer_version "reader gate" and a squash-and-push refusal the code
	// never had, a closed_by_tree reverse index nothing computes, a doctor
	// check on writer_version that really tests op_version (and exits 1,
	// not 4), and two property/fuzz tests that were never written. The
	// reader-gate rewording reuses the act-549a7e fold test; the rest are
	// new, in spec_pass3_docclaim_test.go.
	{
		name:         "spec-no-writer-version-reader-gate",
		docFile:      "docs/spec.md",
		claimPattern: "There is no reader gate on `writer_version`.",
		testName:     "TestDocClaim_Fold_VersionFieldsOnRead",
	},
	{
		name:         "spec-version-check-repo-reports-writer-skew",
		docFile:      "docs/spec.md",
		claimPattern: "`act version --check-repo` walks `.act/ops/` and exits 4 with `version_skew`",
		testName:     "TestDocClaim_Fold_VersionFieldsOnRead",
	},
	{
		name:         "spec-no-version-skew-refusal-on-read-or-write",
		docFile:      "docs/spec.md",
		claimPattern: "no read or write command refuses with `version_skew`",
		testName:     "TestDocClaim_NewerWriterOpDoesNotBlockWritesAndNothingSquashes",
	},
	{
		name:         "spec-no-squash-of-act-op-commits",
		docFile:      "docs/spec.md",
		claimPattern: "No command squashes `act-op:` commits",
		testName:     "TestDocClaim_NewerWriterOpDoesNotBlockWritesAndNothingSquashes",
	},
	{
		name:         "spec-newer-op-version-aborts-fold-not-version-skew",
		docFile:      "docs/spec.md",
		claimPattern: "A binary older than the migration does not answer `version_skew`",
		testName:     "TestDocClaim_NewerOpVersionAbortsFoldWithoutVersionSkew",
	},
	{
		name:         "spec-close-commit-subject",
		docFile:      "docs/spec.md",
		claimPattern: "The close commit subject is `act-op: (<short-id>) close`",
		testName:     "TestDocClaim_Close_CommitSubjectAndNoClosedByTree",
	},
	{
		name:         "spec-close-no-closed-by-tree",
		docFile:      "docs/spec.md",
		claimPattern: "the index has no `closed_by_tree` column",
		testName:     "TestDocClaim_Close_CommitSubjectAndNoClosedByTree",
	},
	{
		name:         "spec-doctor-unknown-op-version-tests-op-version",
		docFile:      "docs/spec.md",
		claimPattern: "Any op file whose `op_version` is greater than the one this binary writes (1)",
		testName:     "TestDocClaim_Doctor_UnknownOpVersionChecksOpVersion",
	},
	{
		name:         "spec-doctor-unknown-op-version-exits-1",
		docFile:      "docs/spec.md",
		claimPattern: "an `unknown-op-version` finding exits 1 like any other error finding",
		testName:     "TestDocClaim_Doctor_UnknownOpVersionChecksOpVersion",
	},
	{
		name:         "spec-doctor-unknown-op-version-edge-case-bullet",
		docFile:      "docs/spec.md",
		claimPattern: "it emits one finding per op file with a newer `op_version`, naming the file",
		testName:     "TestDocClaim_Doctor_UnknownOpVersionChecksOpVersion",
	},
	// act-f95d6e: plain `act doctor` used to stop at the fold error and
	// never reach unknown-op-version on exactly the store it explains.
	{
		name:         "spec-doctor-plain-run-reports-unknown-op-version-on-fold-error",
		docFile:      "docs/spec.md",
		claimPattern: "Plain `act doctor` reports it too: such an op aborts the fold",
		testName:     "TestDocClaim_Doctor_PlainRunReportsUnknownOpVersionWhenFoldFails",
	},
	// act-bd1d30: the fold failure has its own check name, the fold-free
	// read-only checks still run after it, and no fix is applied.
	{
		name:         "spec-doctor-fold-error-own-check-and-fold-free-checks-run",
		docFile:      "docs/spec.md",
		claimPattern: "only `stale-git-lock`, `time-travel`, `unknown-op-version` and `gitignore-effective` still run",
		testName:     "TestDocClaim_Doctor_FoldErrorHasOwnCheckAndFoldFreeChecksStillRun",
	},
	{
		name:         "spec-doctor-check-fold-error-selectable",
		docFile:      "docs/spec.md",
		claimPattern: "`--check fold-error` folds and reports only this finding",
		testName:     "TestDocClaim_Doctor_FoldErrorCheckSelection",
	},
	{
		name:         "spec-doctor-fold-error-applies-no-fixes",
		docFile:      "docs/spec.md",
		claimPattern: "With the fold failed, `--fix` and `--fix-index` apply nothing",
		testName:     "TestDocClaim_Doctor_FoldErrorAppliesNoFixesAndHonoursStrict",
	},
	{
		name:         "spec-property-tier-names-real-tests",
		docFile:      "docs/spec.md",
		claimPattern: "- **`TestPropertyLWWPermutationInvariance`**",
		testName:     "TestDocClaim_Spec_PropertyAndFuzzTestsExist",
	},
	{
		name:         "spec-fuzz-tier-names-real-test",
		docFile:      "docs/spec.md",
		claimPattern: "- **`FuzzFoldDeterminism`**",
		testName:     "TestDocClaim_Spec_PropertyAndFuzzTestsExist",
	},
	// act-2c7be3: machine affinity. Eight claims across five surfaces —
	// the exclusion itself, the write path (including the clearing
	// form), the fail-open rule, the always-emitted JSON contract, the
	// next/--peek refusal, and `act machine`. Each names the doc a
	// reader would actually be holding when they rely on it.
	{
		name:         "spec-ready-excludes-other-machines",
		docFile:      "docs/spec.md",
		claimPattern: "AND it is not pinned to a different machine",
		testName:     "TestDocClaim_Machine_ReadyExcludesOtherMachines",
	},
	{
		name:         "readme-machine-pin",
		docFile:      "README.md",
		claimPattern: "work queue, not from the tracker",
		testName:     "TestDocClaim_Machine_ReadyExcludesOtherMachines",
	},
	{
		name:         "skill-machine-pin-not-title",
		docFile:      "skills/act/SKILL.md",
		claimPattern: "pin it, don't put it in the title",
		testName:     "TestDocClaim_Machine_ReadyExcludesOtherMachines",
	},
	{
		name:         "spec-update-machine-unpins",
		docFile:      "docs/spec.md",
		claimPattern: "or un-pins it: `--machine \"\"` returns it to the default",
		testName:     "TestDocClaim_Machine_UpdatePinsAndUnpins",
	},
	{
		// The safety claim. Its test is the one that would go red if
		// anybody "simplified" the resolver into filtering on the
		// hostname it can always produce.
		name:         "spec-ready-fails-open-unlabelled",
		docFile:      "docs/spec.md",
		claimPattern: "The machine filter only runs when this machine has an EXPLICIT label",
		testName:     "TestDocClaim_Machine_FailsOpenWithoutExplicitLabel",
	},
	{
		name:         "readme-machine-fails-open",
		docFile:      "README.md",
		claimPattern: "act will not filter on a guess",
		testName:     "TestDocClaim_Machine_FailsOpenWithoutExplicitLabel",
	},
	{
		// The cross-process contract with quota-floor. Absence of the
		// key is semantically loaded, so the key set is the claim.
		name:         "spec-ready-machine-object-always",
		docFile:      "docs/spec.md",
		claimPattern: "The top-level `machine` object is **always emitted**",
		testName:     "TestDocClaim_Machine_ReadyJSONKeySet",
	},
	{
		name:         "spec-next-all-machines-requires-peek",
		docFile:      "docs/spec.md",
		claimPattern: "`--all-machines` requires `--peek`",
		testName:     "TestDocClaim_Machine_NextAllMachinesRequiresPeek",
	},
	{
		// act-58947a: the flag's own help named a --host flag and a
		// top-level `elsewhere` key, neither of which shipped. The
		// claim tracked here is the JSON path a consumer actually
		// reads, so the sweep goes red if the help drifts back off it.
		name:         "ready-help-names-machine-pinned-elsewhere",
		docFile:      "cmd/act/ready.go",
		claimPattern: "--json reports it as `machine.pinned_elsewhere`",
		testName:     "TestDocClaim_Machine_ReadyHelpNamesShippedContract",
	},
	{
		// act-71708d: an accepted design-synth nit. Naming the source
		// of the label is not naming its consequence, and on a guessed
		// label the consequence is that nothing is excluded at all.
		name:         "machine-unlabelled-says-not-filtering",
		docFile:      "cmd/act/machine.go",
		claimPattern: "not filtering: this label is a guess from the hostname",
		testName:     "TestDocClaim_Machine_UnlabelledMachineSaysItIsNotFiltering",
	},
	{
		name:         "spec-act-machine-resolution-order",
		docFile:      "docs/spec.md",
		claimPattern: "Layers 1 and 2 are **explicit**; only they cause",
		testName:     "TestDocClaim_Machine_SubcommandReportsLabelAndSource",
	},
	{
		// act-43d11f: the read path no longer refolds the whole op log
		// before every answer. The claim that matters is the pair —
		// unchanged tree is served from the index, and any change to the
		// tree forces the refold — so the asserting test tampers with
		// index.db to prove the skip and mutates `.act/ops/` to prove the
		// refold still fires.
		name:         "spec-list-skips-refold-on-unchanged-ops",
		docFile:      "docs/spec.md",
		claimPattern: "an unchanged op tree is answered from the index without a refold",
		testName:     "TestDocClaim_List_SkipsRefoldOnUnchangedOpTree",
	},
	{
		// act-50d2e2: the refold that a change does force is scoped to the
		// issues that changed, so a store under active writes stops paying
		// a whole-log fold on every read. Asserted as behaviour, not as a
		// stopwatch: a row nobody's ops touched keeps a value written
		// straight into index.db across a read taken after another issue's
		// op landed, which only a scoped fold can do.
		name:         "spec-list-refolds-only-changed-issues",
		docFile:      "docs/spec.md",
		claimPattern: "refolds only the issues whose ops subtree changed",
		testName:     "TestDocClaim_List_RefoldsOnlyTheIssueWhoseOpsChanged",
	},
	{
		// act-3e21b8: `act update --title` exists and replaces the title.
		// The listing is the surface that matters — TestDocClaim_Update_
		// TitleSetsTitle asserts through `act list`, not just `act show`.
		name:         "spec-update-title",
		docFile:      "docs/spec.md",
		claimPattern: "`--title T` (string). REPLACES the title",
		testName:     "TestDocClaim_Update_TitleSetsTitle",
	},
	{
		name:         "skill-update-retitle",
		docFile:      "skills/act/SKILL.md",
		claimPattern: "When the scope moves, retitle",
		testName:     "TestDocClaim_Update_TitleSetsTitle",
	},
	{
		// act-3e21b8 criterion 3: MCP parity is normative, not incidental —
		// a fold-merged field with no write path on one surface is the gap
		// this ticket closed. TestDocClaim_MCP_UpdateTitleTypeParent
		// asserts both the advertised schema and the handler.
		name:         "spec-update-mcp-parity",
		docFile:      "docs/spec.md",
		claimPattern: "MUST have a write path on both the CLI and MCP surfaces",
		testName:     "TestDocClaim_MCP_UpdateTitleTypeParent",
	},
	// act-a025ab: a configured tracker remote that exists turns the
	// no-state guard's "this is normal" into tracker_not_checked_out;
	// without one the fresh-clone wording is unchanged.
	{
		name:         "help-tracker-remote-found",
		docFile:      "cmd/act/help.go",
		claimPattern: "tracker_not_checked_out, naming the remote and the recovery command",
		testName:     "TestDocClaim_TrackerRemoteFoundNoActDir",
	},
	{
		name:         "help-tracker-remote-xdg-file",
		docFile:      "cmd/act/help.go",
		claimPattern: "(default ~/.config/act/tracker-remote)",
		testName:     "TestDocClaim_TrackerRemoteFoundNoConfigJSON",
	},
	{
		name:         "help-tracker-remote-absent-unchanged",
		docFile:      "cmd/act/help.go",
		claimPattern: "fresh-clone wording is unchanged",
		testName:     "TestDocClaim_TrackerRemoteAbsentKeepsFreshCloneWording",
	},
	// act-15ca2b: {repo} inside a linked worktree is the main repo's name.
	{
		name:         "help-tracker-remote-worktree-repo",
		docFile:      "cmd/act/help.go",
		claimPattern: "Inside a linked git worktree, {repo} is the main repo's directory name.",
		testName:     "TestDocClaim_TrackerRemoteWorktreeUsesMainRepoName",
	},
	{
		name:         "spec-tracker-remote-worktree-repo",
		docFile:      "docs/spec.md",
		claimPattern: "Inside a linked git worktree `{repo}` is the main repository's directory name",
		testName:     "TestDocClaim_TrackerRemoteWorktreeUsesMainRepoName",
	},
	// act-626391: act mcp starts but its tool calls carry the same envelope.
	{
		name:         "help-tracker-remote-mcp",
		docFile:      "cmd/act/help.go",
		claimPattern: "act mcp still starts; its tool calls return the same error envelope.",
		testName:     "TestDocClaim_TrackerRemoteMCPToolCallsReturnTrackerNotCheckedOut",
	},
	{
		name:         "spec-tracker-remote-mcp",
		docFile:      "docs/spec.md",
		claimPattern: "return an `isError` tool result carrying the same `tracker_not_checked_out` envelope",
		testName:     "TestDocClaim_TrackerRemoteMCPToolCallsReturnTrackerNotCheckedOut",
	},
	// act-ef5a69: act init refuses to start a divergent tracker.
	{
		name:         "help-tracker-remote-init-refuses",
		docFile:      "cmd/act/help.go",
		claimPattern: "divergent tracker; act init --force-new starts a new tracker anyway.",
		testName:     "TestDocClaim_TrackerRemoteInitRefusesDivergentTracker",
	},
	{
		name:         "help-init-force-new-flag",
		docFile:      "cmd/act/main.go",
		claimPattern: "without it init refuses with tracker_not_checked_out and prints the clone recovery",
		testName:     "TestDocClaim_TrackerRemoteInitRefusesDivergentTracker",
	},
	{
		name:         "spec-tracker-remote-init-refuses",
		docFile:      "docs/spec.md",
		claimPattern: "refuses with exit 3 and `tracker_not_checked_out`, printing the clone recovery",
		testName:     "TestDocClaim_TrackerRemoteInitRefusesDivergentTracker",
	},
	{
		name:         "spec-tracker-not-checked-out",
		docFile:      "docs/spec.md",
		claimPattern: "no `.act/config.json`, every guarded command, read or write, exits 3 with `tracker_not_checked_out`",
		testName:     "TestDocClaim_TrackerRemoteFoundNoConfigJSON",
	},
	{
		name:         "help-update-retitle",
		docFile:      "cmd/act/help.go",
		claimPattern: "Retitling an issue whose scope moved",
		testName:     "TestDocClaim_Update_TitleSetsTitle",
	},
	{
		// act-3e21b8: --type takes the same closed enum as create, and
		// anything else is exit 2 rather than an op the type filter misses.
		name:         "spec-update-type",
		docFile:      "docs/spec.md",
		claimPattern: "`--type T` (enum task|bug|epic|chore). Any other value → exit 2",
		testName:     "TestDocClaim_Update_TypeSetsType",
	},
	{
		// act-3e21b8: --parent sets the hierarchy parent, "" detaches, and
		// a cycle is refused at the write path because act doctor's cycle
		// check covers the blocks subgraph only.
		name:         "spec-update-parent",
		docFile:      "docs/spec.md",
		claimPattern: "`--parent \"\"` detaches the issue from its parent",
		testName:     "TestDocClaim_Update_ParentSetsAndClears",
	},
	{
		// act-3e21b8 criterion 4: a retitle leaves commit-marker
		// correlation and doctor orphan-close untouched, because both key
		// on the id and never the title.
		name:         "spec-retitle-commit-correlation",
		docFile:      "docs/spec.md",
		claimPattern: "A retitle does not disturb commit-marker correlation",
		testName:     "TestDocClaim_Update_RetitleKeepsCommitCorrelation",
	},
	{
		// act-4ffd57: `act next --peek` is the read-only survey path —
		// it shows what next WOULD claim and writes nothing. The
		// asserting test re-reads the ticket after the peek and checks
		// BOTH that it is still open and that its assignee is empty;
		// asserting only on the peek's own output would pass against a
		// path that reported no claim while writing one.
		name:         "spec-next-peek-readonly",
		docFile:      "docs/spec.md",
		claimPattern: "left with `status == open` and no assignee",
		testName:     "TestDocClaim_NextPeek_DoesNotClaim",
	},
	{
		name:         "skill-next-peek-readonly",
		docFile:      "skills/act/SKILL.md",
		claimPattern: "leaves the issue open and\nunassigned",
		testName:     "TestDocClaim_NextPeek_DoesNotClaim",
	},
	{
		name:         "help-next-peek-readonly",
		docFile:      "cmd/act/help.go",
		claimPattern: "shows the same\n  issue and claims nothing",
		testName:     "TestDocClaim_NextPeek_DoesNotClaim",
	},
	{
		// act-57e743: the canonical loop's work commit names explicit
		// paths, and both surfaces say why — in a checkout several
		// sessions share, `-a` commits a sibling session's dirty files.
		name:         "help-commit-pathspec-rationale",
		docFile:      "cmd/act/help.go",
		claimPattern: "share one checkout",
		testName:     "TestDocClaim_Docs_CommitPathspecRationale",
	},
	{
		name:         "skill-commit-pathspec-rationale",
		docFile:      "skills/act/SKILL.md",
		claimPattern: "share one checkout",
		testName:     "TestDocClaim_Docs_CommitPathspecRationale",
	},
	{
		// act-66f987: `act init` never commits to the host repo without
		// --commit-host. README states it for adopters; the flag help
		// states it at the CLI. TestDocClaim_Init_NoHostCommitByDefault
		// asserts the host HEAD does not move on a default init.
		name:         "readme-init-no-host-commit",
		docFile:      "README.md",
		claimPattern: "`act init` never commits to the host repo.",
		testName:     "TestDocClaim_Init_NoHostCommitByDefault",
	},
	{
		name:         "init-flag-commit-host",
		docFile:      "cmd/act/main.go",
		claimPattern: "act init never commits to the host repo unasked",
		testName:     "TestDocClaim_Init_NoHostCommitByDefault",
	},
	{
		// act-66f987: the CONTRIBUTING stanza is opt-in. A public-looking
		// remote produces only a printed suggestion.
		name:         "init-flag-contributing-opt-in",
		docFile:      "cmd/act/main.go",
		claimPattern: "act init never edits CONTRIBUTING.md unasked",
		testName:     "TestDocClaim_Init_NoContributingWithoutOptIn",
	},
	{
		name:         "readme-init-contributing-opt-in",
		docFile:      "README.md",
		claimPattern: "never edits `CONTRIBUTING.md`",
		testName:     "TestDocClaim_Init_NoContributingWithoutOptIn",
	},
	{
		// act-66f987: an active .act/hooks/close gate is never rewritten by
		// init, and no sample is dropped beside it — including --force.
		name:         "skill-init-preserves-active-close-hook",
		docFile:      "skills/act/SKILL.md",
		claimPattern: "an existing `.act/hooks/close` is never\nrewritten",
		testName:     "TestDocClaim_Init_PreservesExistingCloseHook",
	},
	{
		// act-3803ac: --no-fetch is a genuinely non-mutating read.
		// TestDocClaim_NoFetch_DoesNotTouchStore asserts the store is
		// unchanged on every axis the refresh path can touch, against a
		// control that demonstrably mutates.
		name:         "readme-no-fetch-does-not-touch-store",
		docFile:      "README.md",
		claimPattern: "read without touching the\nstore at all",
		testName:     "TestDocClaim_NoFetch_DoesNotTouchStore",
	},
	{
		name:         "readme-no-fetch-env-var",
		docFile:      "README.md",
		claimPattern: "`ACT_NO_FETCH=1`",
		testName:     "TestDocClaim_NoFetch_EnvVarEquivalent",
	},
	{
		// act-3803ac: the refresh outcome is reported, not discarded.
		name:         "readme-refresh-reported",
		docFile:      "README.md",
		claimPattern: "reports what\nthe refresh layer did under a `refresh` key",
		testName:     "TestDocClaim_ReadCommands_SurfaceRefreshOutcome",
	},
	{
		name:         "readme-refresh-failure-not-silent",
		docFile:      "README.md",
		claimPattern: "`refresh.error` names the failure",
		testName:     "TestDocClaim_RefreshFailure_IsReportedNotSilent",
	},
	{
		name:         "no-fetch-flag-help",
		docFile:      "cmd/act/main.go",
		claimPattern: "skip the read-path fetch+rebase entirely",
		testName:     "TestDocClaim_NoFetch_CLISurface",
	},
	{
		// act-3803ac: --no-fetch and --fresh are contradictory asks, so
		// `act ready` rejects the pair instead of silently picking.
		name:         "no-fetch-fresh-mutually-exclusive",
		docFile:      "cmd/act/ready.go",
		claimPattern: "--no-fetch and --fresh/--no-cache are mutually exclusive",
		testName:     "TestDocClaim_NoFetch_RejectsFreshCombo",
	},
	{
		name:         "act-help-go-install",
		docFile:      "cmd/act/help.go",
		claimPattern: "go install github.com/aac/act/cmd/act@latest",
		testName:     "TestDocClaim_GoInstallPath",
	},
	{
		// act init drops an inert .act/hooks/close.sample scaffold; the
		// skill's Pre-close gates section documents it by name. Asserted
		// (doc + behavior) by TestDocClaim_InitCloseHookSample. act-119309.
		name:         "skill-init-close-hook-scaffold",
		docFile:      "skills/act/SKILL.md",
		claimPattern: ".act/hooks/close.sample",
		testName:     "TestDocClaim_InitCloseHookSample",
	},
	{
		name:         "act-help-subcommands-listing",
		docFile:      "cmd/act/help.go",
		claimPattern: "init, version, log, list, search, ready, mine, show,",
		testName:     "TestDocClaim_ActHelpListsSubcommands",
	},
	{
		// act update --unclaim releases a claim (act-086781): the help
		// "Releasing a claim" block documents it; TestDocClaim_Unclaim_
		// ReleasesToOpen exercises claim -> unclaim -> re-claim at the
		// subprocess boundary.
		name:         "act-help-unclaim-releases-claim",
		docFile:      "cmd/act/help.go",
		claimPattern: "act update --unclaim <id>",
		testName:     "TestDocClaim_Unclaim_ReleasesToOpen",
	},
	{
		// act blocks / blocked-by read-only block-graph queries (act-2e1070):
		// `act help workflow` documents the bare-id-list affordance; the
		// behavior is asserted at the subprocess boundary by
		// TestDocClaim_BlockGraphQueries (in cmd/act).
		name:         "act-help-block-graph-queries",
		docFile:      "cmd/act/help.go",
		claimPattern: "act blocked-by <id>",
		testName:     "TestDocClaim_BlockGraphQueries",
	},
	// next/finish composed CLI verbs (act-431f99): the CLI gains `act next`
	// (ready+claim+show) and `act finish` (close+push), matching the
	// act_next / act_finish MCP tools so both interfaces offer the
	// documented-recommended composed flow. Help-text claims live in
	// cmd/act/help.go (the subcommand listing + the composed-flow note);
	// README examples carry the same. Drift shape: someone drops a verb
	// from the listing or the composed-flow note, and a cold-start agent
	// reading either surface no longer learns the one-command loop exists.
	{
		name:         "next-finish-help-listed",
		docFile:      "cmd/act/help.go",
		claimPattern: "update, next, finish,",
		testName:     "TestDocClaim_Next_ClaimsAndShowsTopReady",
	},
	{
		name:         "next-help-composed-flow",
		docFile:      "cmd/act/help.go",
		claimPattern: "'act next' bundles steps",
		testName:     "TestDocClaim_Next_ClaimsAndShowsTopReady",
	},
	{
		name:         "finish-help-composed-flow",
		docFile:      "cmd/act/help.go",
		claimPattern: "'act finish' is 'act close'",
		testName:     "TestDocClaim_Finish_ClosesAndReportsClosed",
	},
	// finish-does-not-push-host-commit (act-802ef9): `act finish` is `act
	// close` — it commits the close op and pushes THAT op to the nested
	// `.act/` tracker remote. It does NOT push the host-repo work commit
	// carrying the `Act-Id:` trailer. The help/README/skill all carry the
	// clarification so a reader can't conclude their code was published.
	// Boundary test drives `act help` and asserts the live text. Drift
	// shape: someone re-introduces "close + push" framing that implies the
	// host commit was pushed.
	{
		name:         "finish-help-no-host-push",
		docFile:      "cmd/act/help.go",
		claimPattern: "does NOT push your host work commit",
		testName:     "TestDocClaim_Finish_HelpSaysNoHostPush",
	},
	{
		name:         "finish-readme-no-host-push",
		docFile:      "README.md",
		claimPattern: "does NOT push your host",
		testName:     "TestDocClaim_Finish_HelpSaysNoHostPush",
	},
	{
		name:         "next-verb-readme-example",
		docFile:      "README.md",
		claimPattern: "$ act next",
		testName:     "TestDocClaim_Next_ClaimsAndShowsTopReady",
	},
	{
		name:         "finish-verb-readme-example",
		docFile:      "README.md",
		claimPattern: "$ act finish",
		testName:     "TestDocClaim_Finish_ClosesAndReportsClosed",
	},
	{
		// README "How agents use it" prose states the MCP loop tools have
		// matching CLI verbs (act-2645c7), replacing the stale "act_next/
		// act_finish are MCP-only" carve-out. Guarded by the act next
		// behavior test. Drift shape: someone reinstates the carve-out.
		// (act-8a6536 rephrased the same claim when the README was updated
		// for the eight-tool advertised surface; the guarantee is unchanged.)
		name:         "readme-mcp-cli-mirror",
		docFile:      "README.md",
		claimPattern: "each mirroring a CLI verb of the same name",
		testName:     "TestDocClaim_Next_ClaimsAndShowsTopReady",
	},
	// skill-cli-composed-verbs (act-46b704): the shipped act skill +
	// setup.md previously described `act_next`/`act_finish` as MCP-only,
	// with the CLI told to run `act close` then a separate `git push`.
	// Both were stale once commit 739f108 added the real `act next` /
	// `act finish` CLI verbs (cmd/act/next.go, cmd/act/finish.go) that
	// compose the same cli.Run* primitives as the MCP tools. These entries
	// pin the corrected skill/setup claims to the boundary tests that drive
	// the actual CLI verbs. Drift shape: someone reverts the skill to the
	// MCP-only framing while the verbs still exist, re-misleading a CLI
	// agent into a `git push` step that close already does itself.
	//
	// The skillmin -19% reduction (act-c4029d, 2026-07) reworded these
	// three claims without changing what they assert; the substrings below
	// are re-derived from the reduced wording. The composed-verb semantics
	// now live in the MCP-vs-CLI loop table ("same composed pick + claim +
	// show" for next; "`act finish <id>` (same) or `act close`" for finish),
	// and the CLI/MCP-equivalence claim — which the reduction dropped as a
	// redundant restatement in setup.md — is now pinned to its surviving
	// copy in SKILL.md ("they behave identically").
	{
		name:         "skill-cli-next-verb",
		docFile:      "skills/act/SKILL.md",
		claimPattern: "`act next` (same composed pick + claim + show",
		testName:     "TestDocClaim_Next_ClaimsAndShowsTopReady",
	},
	{
		name:         "skill-cli-finish-verb",
		docFile:      "skills/act/SKILL.md",
		claimPattern: "`act finish <id>` (same) or `act close ",
		testName:     "TestDocClaim_Finish_ClosesAndReportsClosed",
	},
	{
		name:         "skill-cli-verbs-equivalent",
		docFile:      "skills/act/SKILL.md",
		claimPattern: "and they behave identically",
		testName:     "TestDocClaim_Next_ClaimsAndShowsTopReady",
	},
	// close-pushes-itself / no-origin-skip (act-46b704, act-e9ce41): close
	// (and `act finish`) performs the publish itself — there is no separate
	// `git push` for the close op — and on a no-origin repo it commits
	// locally and skips the push with no network, still exiting 0. The skill
	// and `act help workflow` both carry these claims; the boundary tests
	// live in internal/cli/close_push_docclaim_test.go (gitops push-counter
	// + close-op publication / status). Drift shape: a refactor that drops
	// the in-close push (re-introducing a manual `git push`) or that errors
	// a no-origin close would break the matching test.
	//
	// The skillmin -19% reduction (act-c4029d, 2026-07) reworded both skill
	// claims without changing what they assert; substrings re-derived from
	// the reduced step-4 wording ("there is no separate `git push` for the
	// close op"; the no-origin branch now reads "it commits locally and
	// skips the push").
	{
		name:         "skill-close-pushes-itself",
		docFile:      "skills/act/SKILL.md",
		claimPattern: "there is no separate `git push` for the close op",
		testName:     "TestDocClaim_Close_PushesOnRemoteNoSeparateGitPush",
	},
	{
		name:         "skill-no-origin-skip-push",
		docFile:      "skills/act/SKILL.md",
		claimPattern: "commits locally and skips the push",
		testName:     "TestDocClaim_Close_NoOriginSkipsPushNoNetwork",
	},
	{
		name:         "help-no-origin-skip-push",
		docFile:      "cmd/act/help.go",
		claimPattern: "skip the push with no network",
		testName:     "TestDocClaim_Close_NoOriginSkipsPushNoNetwork",
	},
	// state-import/export (act-93370d): the worktree-blind state
	// movement verbs. The help text names the subcommands and their
	// directory-scoped <dir> argument shape; the docclaim tests drive
	// `act state import --help` / `act state export --help` and assert
	// the help strings carry no "worker"/"worktree" vocabulary. The
	// deprecated `bootstrap-worker`/`harvest` aliases delegate to these.
	{
		name:         "act-help-state-import-subcommand",
		docFile:      "cmd/act/help.go",
		claimPattern: "act state import <dir>",
		testName:     "TestDocClaim_StateImportExportDirectoryScoped",
	},
	{
		name:         "act-help-state-export-subcommand",
		docFile:      "cmd/act/help.go",
		claimPattern: "act state export <dir>",
		testName:     "TestDocClaim_StateImportExportDirectoryScoped",
	},
	{
		name:         "state-deprecated-aliases-delegate",
		docFile:      "cmd/act/help.go",
		claimPattern: "state import, state export",
		testName:     "TestDocClaim_DeprecatedAliasesDelegate",
	},
	// skill-state-import-seed (act-2c65b5): the skill's Pre-close gates
	// section tells a worker to seed the tracker (and its committed close
	// hook) into a worktree with `act state import <dir>`. The boundary
	// test confirms the binary actually surfaces that verb (in `act help`
	// and as a worktree-blind subcommand). Drift shape: the skill names a
	// seed mechanism the binary no longer exposes.
	{
		name:         "skill-state-import-seed",
		docFile:      "skills/act/SKILL.md",
		claimPattern: "act state import <dir>",
		testName:     "TestDocClaim_StateImportExportDirectoryScoped",
	},
	// mcp-update-priority-range (act-724fba): the act_update tool schema
	// advertises priority as 0-3, matching act_create's 0..3 and the
	// enforced range in internal/op/payloads.go. The same boundary test
	// also pins that act_doctor's schema carries no `compact` property
	// (callDoctor decodes only check/fix; DoctorOptions has no Compact).
	// Drift shape: the schema claims a 0-4 range or re-adds a no-op
	// compact arg an agent would try to pass.
	{
		name:         "mcp-update-priority-range",
		docFile:      "internal/mcp/server.go",
		claimPattern: "New priority (0-3).",
		testName:     "TestDocClaim_MCP_UpdatePriorityRangeAndNoCompact",
	},
	// harvest-json-* (act-c8028f): the harvest help text in cmd/act/help.go
	// names three JSON-envelope fields that the orchestrator (and the act
	// skill's recommended postlude) depend on. If any field name disappears
	// from the doc OR the wire shape stops emitting that key, the matching
	// TestDocClaim_Harvest_JSON*Field test breaks at the user-visible
	// boundary (json.Unmarshal against the envelope), not just in an
	// internal struct assertion.
	{
		name:         "harvest-json-harvested-ops",
		docFile:      "cmd/act/help.go",
		claimPattern: "harvested_ops",
		testName:     "TestDocClaim_Harvest_JSONHarvestedOpsField",
	},
	{
		name:         "harvest-json-skipped-ops",
		docFile:      "cmd/act/help.go",
		claimPattern: "skipped_ops",
		testName:     "TestDocClaim_Harvest_JSONSkippedOpsField",
	},
	{
		name:         "harvest-json-fold-diff-summary",
		docFile:      "cmd/act/help.go",
		claimPattern: "fold_diff_summary",
		testName:     "TestDocClaim_Harvest_JSONFoldDiffSummaryField",
	},
	{
		name:         "canonical-loop-git-push",
		docFile:      "cmd/act/help.go",
		claimPattern: "git push",
		testName:     "TestDocClaim_CanonicalLoop_HelpOverviewIncludesGitPush",
	},
	{
		name:         "commit-marker-trailer-form",
		docFile:      "cmd/act/help.go",
		claimPattern: "Act-Id: act-XXXX",
		testName:     "TestDocClaim_CommitMarker_TrailerFormAndDoctorAttribution",
	},
	{
		name:         "commit-marker-historical-back-compat",
		docFile:      "cmd/act/help.go",
		claimPattern: "back-compat",
		testName:     "TestDocClaim_CommitMarker_HistoricalSubjectFormStillAttributed",
	},
	{
		name:         "error-envelope-id-ambiguous",
		docFile:      "cmd/act/help.go",
		claimPattern: "id_ambiguous",
		testName:     "TestDocClaim_AmbiguousPrefix_ExitsTwoWithIdAmbiguous",
	},
	{
		// act-89a595: the spec's error section now RETIRES
		// `push_exhausted`; the asserting test proves no CLI path
		// emits it and that the deferral replaced it.
		name:         "errors-push-exhausted-retired",
		docFile:      "docs/spec.md",
		claimPattern: "`push_exhausted` (was exit 4) — retired by act-89a595",
		testName:     "TestDocClaim_Errors_PushExhaustedRetired",
	},
	{
		name:         "errors-remote-unreachable",
		docFile:      "docs/spec.md",
		claimPattern: "remote_unreachable",
		testName:     "TestDocClaim_Errors_RemoteUnreachable",
	},
	{
		name:         "prefix-ok-under-flag",
		docFile:      "cmd/act/ready.go",
		claimPattern: "(prefix ok)",
		testName:     "TestDocClaim_PrefixOk_TwoCharUniquePrefixResolves",
	},
	// pushwrite-* (act-65a7d5): Phase 2 ticket 3a wires PushWithRetry into
	// the write-helpers so every successful commit on a remote-configured
	// project pushes synchronously. The spec sentences in docs/spec.md
	// are the load-bearing contract — agents reading the spec cold must
	// learn (a) that auto-publish is on by default when origin is set,
	// (b) that no-origin repos remain local-only, and (c) that a failed
	// publish on a committed op DEFERS (exit 0 + queue + warning)
	// rather than failing the write — act-89a595. Behavioral
	// assertions live in push_integration_test.go; these entries lock the
	// doc claims that the behavior is supposed to match.
	{
		name:         "pushwrite-auto-publish-on-remote",
		docFile:      "docs/spec.md",
		claimPattern: "synchronous `git push`",
		testName:     "TestDocClaim_PushOnWrite_AutoPublishOnRemote",
	},
	{
		name:         "pushwrite-no-origin-local-only",
		docFile:      "docs/spec.md",
		claimPattern: "No-origin repos skip the publish step silently",
		testName:     "TestDocClaim_PushOnWrite_NoOriginIsLocalOnly",
	},
	{
		// act-650378: act resolves untracked-op-file checkout collisions
		// itself instead of emitting git's "move or remove them", which
		// asks a human to delete files inside act's own state dir.
		name:         "sync-resolves-untracked-op-collision",
		docFile:      "docs/spec.md",
		claimPattern: "Untracked op-file collisions are resolved by act, not by the operator (act-650378)",
		testName:     "TestDocClaim_Sync_ResolvesUntrackedOpCollision",
	},
	{
		name:         "pushwrite-publish-failure-defers",
		docFile:      "docs/spec.md",
		claimPattern: "**Publish failure is a deferral, not an error (act-89a595).**",
		testName:     "TestDocClaim_PushOnWrite_PublishFailureDefers",
	},
	{
		name:         "prefix-ok-create-parent",
		docFile:      "cmd/act/create.go",
		claimPattern: "full or unique prefix",
		testName:     "TestDocClaim_PrefixOk_TwoCharUniquePrefixResolves",
	},
	// cache-* (act-20c77e, Phase 2 ticket 5): the read-path TTL cache
	// makes load-bearing claims in docs/spec.md (Read-cache section)
	// and in `act ready --help`. Each entry pins one such claim at the
	// boundary a reader would consult.
	{
		name:         "cache-ttl-five-seconds",
		docFile:      "docs/spec.md",
		claimPattern: "5-second TTL",
		testName:     "TestDocClaim_ReadCache_TTLFiveSeconds",
	},
	{
		// act-00182d: dual-era MCP — 2026-07-28 clients are served
		// statelessly from per-request _meta, with the tools/list cache hint;
		// the test drives the server over stdio without initialize.
		name:         "mcp-stateless-protocol-revision",
		docFile:      "docs/spec.md",
		claimPattern: "answered without initialize",
		testName:     "TestDocClaim_MCP_StatelessRequestsWithoutInitialize",
	},
	{
		// act-ffc00d: the MCP server resolves the host repo per-call from a
		// client-supplied workspace (Codex's proprietary
		// `_meta."x-codex-turn-metadata".workspaces`) rather than its own
		// process cwd — which under the Codex plugin launch model is the
		// plugin install dir, not the user's project. The spec `act mcp`
		// Host-repo resolution paragraph carries the claim;
		// TestDocClaim_MCP_CodexWorkspaceRoutesToClientWorkspace (internal/mcp)
		// drives a tools/call with the _meta hint and asserts the write lands
		// in the named workspace, not the server cwd.
		name:         "mcp-codex-workspace-resolution",
		docFile:      "docs/spec.md",
		claimPattern: "x-codex-turn-metadata",
		testName:     "TestDocClaim_MCP_CodexWorkspaceRoutesToClientWorkspace",
	},
	{
		// act-76cd7a: act.fetchTimeoutSeconds now bounds the `git fetch`
		// steps in gitops.FetchAndRebase (previously inert — defined,
		// defaulted, written by `remote enable`, but never consumed). The
		// spec config-table row carries the live-behavior claim; the
		// asserting test writes the key and confirms a hung fetch is
		// aborted well under the injected sleep, at the FetchAndRebase
		// boundary both the read-cache and push-retry paths share.
		name:         "fetch-timeout-config-bounds-fetch",
		docFile:      "docs/spec.md",
		claimPattern: "a fetch exceeding the budget is aborted",
		testName:     "TestDocClaim_FetchTimeout_ConfigKeyBoundsFetch",
	},
	{
		// act-97415e: the `act.readCacheTTLSeconds` key overrides the
		// default 5s read-cache window per-repo (previously inert — the
		// cache hardcoded the const). The spec Read-cache section carries
		// the claim; TestDocClaim_ReadCache_ConfiguredTTLOverridesDefault
		// exercises both widen and narrow at the MaybeRefresh boundary.
		name:         "cache-ttl-config-override",
		docFile:      "docs/spec.md",
		claimPattern: "overrides the default window per-repo",
		testName:     "TestDocClaim_ReadCache_ConfiguredTTLOverridesDefault",
	},
	{
		name:         "cache-dispatch-mode-bypass",
		docFile:      "docs/spec.md",
		claimPattern: "ACT_DISPATCH_MODE=1",
		testName:     "TestDocClaim_ReadCache_DispatchModeEnvBypass",
	},
	{
		name:         "cache-fresh-no-cache-alias",
		docFile:      "docs/spec.md",
		claimPattern: "`--fresh` / `--no-cache`",
		testName:     "TestDocClaim_ReadCache_FreshNoCacheAlias",
	},
	{
		name:         "cache-fold-invalidation",
		docFile:      "docs/spec.md",
		claimPattern: "fold-checkpoint.json does not survive",
		testName:     "TestDocClaim_ReadCache_FoldCheckpointInvalidation",
	},
	{
		name:         "cache-noop-preserves-checkpoint",
		docFile:      "docs/spec.md",
		claimPattern: "A no-op rebase (HEAD unchanged) leaves both files in place",
		testName:     "TestDocClaim_ReadCache_NoRebaseLeavesCheckpoint",
	},
	{
		name:         "read-cache-fetch-head-source",
		docFile:      "docs/spec.md",
		claimPattern: ".act/.git/FETCH_HEAD",
		testName:     "TestDocClaim_ReadCache_FetchHeadPathLayout",
	},
	{
		name:         "read-cache-fresh-flag-help",
		docFile:      "cmd/act/ready.go",
		claimPattern: "bypass the read-path TTL cache",
		testName:     "TestDocClaim_ReadCache_FreshFlagInReadyHelp",
	},
	// show-full-* (act-3c89): `act show --full` disables the human-mode
	// truncation guard on description and closed_reason. The flag-help
	// string in cmd/act/main.go is the load-bearing claim; the asserting
	// test drives the subprocess `act show <id> --full` and confirms a
	// long description renders verbatim (no "(truncated; see --json"
	// marker).
	{
		name:         "show-full-flag-help",
		docFile:      "cmd/act/main.go",
		claimPattern: "render description and closed_reason without truncation",
		testName:     "TestDocClaim_Show_FullDisablesTruncation",
	},
	// remote-* and config-* (Phase 2 ticket 1a, act-72d20e): the
	// `act remote enable` / `act remote disable` subcommands plus the
	// `act.role` decision that closes v1 OQ #4. Help-text claims live in
	// cmd/act/help.go (helpOverview); spec invariants live in
	// docs/spec.md. The drift shape: someone removes the `remote`
	// listing from helpOverview, or the spec table claim "act.role=
	// orchestrator" no longer matches what enable writes, and a cold-
	// start agent reading either surface gets misled.
	{
		name:         "remote-help-listed",
		docFile:      "cmd/act/help.go",
		claimPattern: "remote",
		testName:     "TestDocClaim_Remote_HelpListsSubcommand",
	},
	{
		name:         "remote-help-enable-receive-policy",
		docFile:      "cmd/act/help.go",
		claimPattern: "receive.denyCurrentBranch=updateInstead",
		testName:     "TestDocClaim_Remote_EnableSetsReceiveDenyCurrentBranch",
	},
	{
		name:         "remote-spec-disable-idempotent",
		docFile:      "docs/spec.md",
		claimPattern: "MUST exit zero both times",
		testName:     "TestDocClaim_Remote_DisableIsIdempotent",
	},
	{
		name:         "remote-spec-disable-removes-hook-file",
		docFile:      "docs/spec.md",
		claimPattern: "MUST remove the file (not merely",
		testName:     "TestDocClaim_Remote_DisableRemovesHookFile",
	},
	{
		name:         "remote-spec-post-receive-skeleton-ticket-6a",
		docFile:      "docs/spec.md",
		claimPattern: "ticket 6a",
		testName:     "TestDocClaim_Remote_PostReceiveSkeletonNamesTicket",
	},
	// remote-enable doctor verification (act-06ef97): spec §"Verification"
	// pins that the post-config doctor pass blocks the role transition
	// ONLY on error-severity findings. Warn-severity findings (typically
	// orphan-close from historical commits not in the current clone) are
	// informational and must not block. The drift shape: a refactor of
	// runRemoteEnable that treats `doctorCode != 0` (or `dr.Count > 0`)
	// as a failure would reintroduce the exit-code/output mismatch that
	// motivated this ticket.
	{
		name:         "remote-spec-enable-only-error-severity-blocks",
		docFile:      "docs/spec.md",
		claimPattern: "any error-severity finding",
		testName:     "TestDocClaim_Remote_EnableOnlyBlocksOnErrorSeverity",
	},
	{
		name:         "config-act-role-orchestrator",
		docFile:      "docs/spec.md",
		claimPattern: "act.role=orchestrator",
		testName:     "TestDocClaim_Config_ActRoleOrchestrator",
	},
	{
		name:         "config-act-role-worker-default",
		docFile:      "docs/spec.md",
		claimPattern: "default is `worker`",
		testName:     "TestDocClaim_Config_ActRoleDefaultsToWorker",
	},
	// remote-sync-* and hook-* (Phase 2 ticket 6a, act-e29159): the
	// `act remote sync` subcommand plus the post-receive hook body
	// that invokes it. Help-text claim lives in cmd/act/help.go; the
	// stderr-literal and sync-log schema claims live in
	// docs/spec.md; the hook-body claim lives in
	// internal/config/remote.go (the constant the install path reads
	// from). The drift shape: someone removes the `remote sync`
	// listing from help, or changes the stderr literal, or changes
	// the hook body to skip `act remote sync`, and a cold-start
	// agent reading either surface gets misled.
	{
		name:         "remote-sync-help-listed",
		docFile:      "cmd/act/help.go",
		claimPattern: "remote sync",
		testName:     "TestDocClaim_RemoteSync_HelpListed",
	},
	{
		name:         "remote-sync-no-upstream-stderr",
		docFile:      "docs/spec.md",
		claimPattern: "no origin-upstream configured",
		testName:     "TestDocClaim_RemoteSync_NoUpstreamStderr",
	},
	{
		name:         "hook-post-receive-body",
		docFile:      "internal/config/remote.go",
		claimPattern: "nohup act remote sync",
		testName:     "TestDocClaim_Hook_PostReceiveInvokesSync",
	},
	{
		name:         "remote-sync-log-reason-first-field",
		docFile:      "docs/spec.md",
		claimPattern: "first JSON field on every line is `reason`",
		testName:     "TestDocClaim_RemoteSync_SyncLogReasonFirstField",
	},
	{
		name:         "remote-sync-log-schema-fields",
		docFile:      "docs/spec.md",
		claimPattern: "| `reason` | string |",
		testName:     "TestDocClaim_RemoteSync_SyncLogSchemaFields",
	},
	// remote-add-upstream-* (Phase 2 ticket 1b, act-4f9375): the
	// `act remote add-upstream <url>` verb plus the public-URL refusal
	// path. Help-text claims live in cmd/act/help.go; the
	// stderr-literal refusal message and the `upstream_public` error
	// code claim live in docs/spec.md. Drift shape: someone changes
	// the stderr literal away from the canonical refusal phrasing, or
	// drops the `--force-public` flag from the help listing, and a
	// cold-start agent reading either surface gets misled.
	{
		name:         "remote-add-upstream-help-listed",
		docFile:      "cmd/act/help.go",
		claimPattern: "add-upstream",
		testName:     "TestDocClaim_RemoteAddUpstream_HelpListed",
	},
	{
		name:         "remote-add-upstream-public-refusal-stderr",
		docFile:      "docs/spec.md",
		claimPattern: "refusing public upstream",
		testName:     "TestDocClaim_RemoteAddUpstream_PublicRefusalStderr",
	},
	{
		name:         "remote-add-upstream-force-public-flag",
		docFile:      "cmd/act/help.go",
		claimPattern: "--force-public",
		testName:     "TestDocClaim_RemoteAddUpstream_ForcePublicFlag",
	},
	// bootstrap-from-remote-* (Phase 2 ticket 7, act-0480c9): the
	// `act bootstrap-worker --from-remote` mode and the two new error
	// codes (`bootstrap_timeout`, `target_not_empty`) it introduces.
	// Drift shape: someone changes the --from-remote help line away
	// from the spec wording, or the role-write target moves off
	// .act/.git/config, and a cold-start agent reading either surface
	// gets misled.
	{
		name:         "bootstrap-from-remote-help-listed",
		docFile:      "cmd/act/help.go",
		claimPattern: "--from-remote",
		testName:     "TestDocClaim_BootstrapFromRemote_HelpListsFlag",
	},
	{
		name:         "bootstrap-from-remote-role-worker",
		docFile:      "docs/spec.md",
		claimPattern: "act.role=worker",
		testName:     "TestDocClaim_BootstrapFromRemote_SetsWorkerRole",
	},
	{
		name:         "bootstrap-from-remote-timeout-envelope",
		docFile:      "docs/spec.md",
		claimPattern: "`bootstrap_timeout`",
		testName:     "TestDocClaim_BootstrapFromRemote_TimeoutEnvelope",
	},
	{
		name:         "bootstrap-from-remote-target-not-empty",
		docFile:      "docs/spec.md",
		claimPattern: "`target_not_empty`",
		testName:     "TestDocClaim_BootstrapFromRemote_TargetNotEmptyEnvelope",
	},
	// bootstrap-from-cwd-* (act-40fce0): the worker-cwd bootstrap mode
	// (`act bootstrap-worker --from-cwd <orchestrator-path>`) lets a worker
	// inside a freshly-created worktree bootstrap from the orchestrator
	// WITHOUT a raw `cp -r` of a live index.db — the divergence behind the
	// concurrent-worker silent-data-loss bug. The help-text flag claim
	// lives in cmd/act/help.go; the spec section claim lives in
	// docs/spec.md; the worker-protocol guidance lives in the embedded
	// SKILL.md. Drift shape: someone removes the --from-cwd listing or the
	// spec section and a cold-start worker falls back to `cp -r`.
	{
		name:         "bootstrap-from-cwd-help-listed",
		docFile:      "cmd/act/help.go",
		claimPattern: "--from-cwd",
		testName:     "TestDocClaim_BootstrapFromCWD_HelpListsFlag",
	},
	{
		name:         "bootstrap-from-cwd-spec-section",
		docFile:      "docs/spec.md",
		claimPattern: "worker-cwd mode, act-40fce0",
		testName:     "TestDocClaim_BootstrapFromCWD_HelpListsFlag",
	},
	{
		name:         "bootstrap-from-cwd-spec-persistence-guarantee",
		docFile:      "docs/spec.md",
		claimPattern: "Op-write persistence guarantee",
		testName:     "TestDocClaim_BootstrapFromCWD_PersistenceGuarantee",
	},
	// harvest-narrow-* (Phase 2 ticket 8, act-e31aa1): when the worker's
	// .act/.git has act.role=worker AND its remote.origin.url matches the
	// orchestrator's canonical .act/.git path, harvest short-circuits with
	// a stable stderr message and an empty envelope. The skip-message
	// literal is the load-bearing claim in cmd/act/help.go; if the help
	// text drifts away from the constant in internal/cli/harvest.go (or
	// vice versa), agents reading either surface get misled about the
	// observable signal.
	{
		name:         "harvest-narrow-skip-message",
		docFile:      "cmd/act/help.go",
		claimPattern: "harvest skipped, worker was push-attached",
		testName:     "TestDocClaim_HarvestNarrow_SkipMessage",
	},
	// phase2-* (Phase 2 ticket 10, act-95bc5c): the in-repo skill +
	// phase2-design surfaces that describe act's Phase 2 push-attached
	// coordination-plane feature. (A former entry asserted on another
	// repo's docs across repos; that inverted dependency was removed —
	// act doesn't test other repos' docs. See act-e7ca40.)
	{
		name:         "phase2-design-cutover-section",
		docFile:      "docs/coordination-plane-design.md",
		claimPattern: "Phase 1.5 → Phase 2 cutover",
		testName:     "TestDocClaim_Phase2Design_Cutover",
	},
	// slow-write-*, pending-push-*, offline-* (Phase 2 ticket 3b,
	// act-4a604d): the `--offline` flag plus slow-write measurement
	// and pending-push deferred-publish queue. The four entries below
	// pin the user-visible surfaces a cold-start agent would consult:
	//   - slow-write-warning-text: literal stderr prefix for a slow
	//     write, asserted by exec'ing a fault-injected commit.
	//   - slow-write-log-schema: the `duration_ms` field name in the
	//     pinned schema for .act/.slow-writes.
	//   - pending-push-schema: the `sha` field name in the pinned
	//     schema for .act/.pending-pushes.
	//   - offline-flag-help: the --offline help string on `act create`
	//     (the flag is wired on all six write subcommands; the help
	//     string is shared via the consistent "commit locally" prefix).
	{
		name:         "slow-write-warning-text",
		docFile:      "docs/spec.md",
		claimPattern: "act: slow write detected (",
		testName:     "TestDocClaim_SlowWrite_WarningText",
	},
	{
		name:         "slow-write-log-schema",
		docFile:      "docs/spec.md",
		claimPattern: `"duration_ms":`,
		testName:     "TestDocClaim_SlowWrite_LogSchema",
	},
	{
		name:         "pending-push-schema",
		docFile:      "docs/spec.md",
		claimPattern: `"sha":`,
		testName:     "TestDocClaim_PendingPush_Schema",
	},
	{
		name:         "offline-flag-help",
		docFile:      "cmd/act/create.go",
		claimPattern: "commit locally, skip push",
		testName:     "TestDocClaim_Offline_FlagHelp",
	},
	// orchestrator-sync-* (Phase 2 ticket 6b, act-a9a59e): the
	// orchestrator-write upstream-sync trigger in
	// internal/gitops/gitops.go fires `act remote sync` in the
	// background after every successful commit when act.role=
	// orchestrator. Spec claims: the role-key gate is "act.role=
	// orchestrator" (no path heuristic), and the detach mechanism
	// is fork-exec. Drift shape: someone refactors the trigger to
	// use a filesystem-path heuristic or switches to a blocking
	// invocation, and a cold-start agent reading the spec gets
	// misled.
	{
		name:         "orchestrator-sync-role-check",
		docFile:      "docs/spec.md",
		claimPattern: "act.role=orchestrator",
		testName:     "TestDocClaim_OrchestratorSync_RoleCheck",
	},
	{
		name:         "orchestrator-sync-background-detach",
		docFile:      "docs/spec.md",
		claimPattern: "fork-exec",
		testName:     "TestDocClaim_OrchestratorSync_BackgroundDetach",
	},
	// doctor-case-* (Phase 2 ticket 9, act-aa4f19): five new
	// reconciliation cases plus the remote-status JSON block. The five
	// entries below pin the user-visible literals — case (a')'s
	// post-receive hook check on orchestrators, case (c')'s
	// worker-without-origin error, case (f)'s unpushed-commits stderr
	// line, case (g)'s origin-unreachable literal (exit 4), and case
	// (h)'s upstream-drift literal. The literal phrasings live in
	// docs/spec.md "Doctor reconciliation (Phase 2)" so a drift in
	// either the spec or the implementation surfaces here.
	{
		name:         "doctor-case-a-prime",
		docFile:      "docs/spec.md",
		claimPattern: "post-receive hook installed",
		testName:     "TestDocClaim_DoctorCase_APrime_HookCheckOnOrchestrator",
	},
	{
		name:         "doctor-case-c-prime",
		docFile:      "docs/spec.md",
		claimPattern: "`worker-without-origin`",
		testName:     "TestDocClaim_DoctorCase_CPrime_WorkerWithoutOrigin",
	},
	{
		name:         "doctor-case-f",
		docFile:      "docs/spec.md",
		claimPattern: "local: <N> unpushed commits ahead of origin",
		testName:     "TestDocClaim_DoctorCase_F_UnpushedCommitsStderr",
	},
	{
		name:         "doctor-case-g",
		docFile:      "docs/spec.md",
		claimPattern: "remote: origin unreachable; run 'act remote sync' from the orchestrator or check connectivity",
		testName:     "TestDocClaim_DoctorCase_G_OriginUnreachableStderr",
	},
	{
		name:         "doctor-case-h",
		docFile:      "docs/spec.md",
		claimPattern: "upstream: origin-upstream is <N> commits behind origin; run 'act remote sync'",
		testName:     "TestDocClaim_DoctorCase_H_UpstreamDriftStderr",
	},
	// init-gitignore-no-ask (act-d4a2): act init writes only `.act/` to the
	// host `.gitignore` — never `.ask/` or any other non-act path. Sibling
	// tools own their own gitignore footprint. The doc surface is the
	// gitignoreEntry constant's comment in internal/cli/init.go; the
	// asserting test runs RunInit on a fresh repo and verifies `.ask/` is
	// absent from the produced .gitignore. Drift shape: a future refactor
	// adds a second `ensureGitignoreEntry(..., ".ask/")` call and a
	// cold-start reader of init.go's commentary gets misled — or, more
	// likely, the comment gets edited away and the regression test silently
	// becomes the only line of defense.
	{
		name:         "init-gitignore-no-ask",
		docFile:      "internal/cli/init.go",
		claimPattern: "does NOT write `.ask/`",
		testName:     "TestDocClaim_Init_GitignoreNoAskEntry",
	},
	// hook-runs-before-op-write / refused-close-* (act-8ee085): the
	// hooks contract now says the hook runs BEFORE the op file is
	// written, and that a refused op leaves nothing behind. Both are
	// user-visible: a hook author reading the spec is told the op file
	// is not on disk while their hook runs, and the fleet's act-sync
	// sweep depends on it (a sweep that commits an in-flight op file
	// produced a close present in HEAD and absent from the working
	// tree). Drift shape: someone re-orders the write ahead of the hook
	// for convenience and the race silently returns — the spec still
	// reads correctly, and only an assertion on where the file is
	// while the hook runs catches it.
	{
		name:         "hook-runs-before-op-write",
		docFile:      "docs/spec.md",
		claimPattern: "before the op file is written",
		testName:     "TestDocClaim_HookRunsBeforeOpFileIsWritten",
	},
	{
		name:         "refused-op-writes-nothing",
		docFile:      "docs/spec.md",
		claimPattern: "writes no op file at all",
		testName:     "TestDocClaim_RefusedCloseSurvivesConcurrentSweep",
	},
	// withdrawn-op-retracted (act-8ee085 / act-cb55ee): where a rollback
	// is still possible (op written, commit failed), act guarantees the
	// working tree and HEAD do not disagree — if HEAD already tracks the
	// file, act commits the removal itself instead of leaving a dirty
	// deletion for a blind sweep to publish anonymously.
	{
		name:         "withdrawn-op-retracted",
		docFile:      "docs/spec.md",
		claimPattern: "`act` commits the removal itself",
		testName:     "TestDocClaim_WithdrawnOpCommittedBySweepIsRetracted",
	},
	// install-smoke (act-8ee085 follow-through): install.sh verifies the
	// binary it installed by round-tripping a scratch tracker, not just by
	// running `act version`. Drift shape: the smoke call is dropped for
	// speed and the installer silently goes back to proving only that the
	// binary starts.
	{
		name:         "install-runs-roundtrip-smoke",
		docFile:      "install.sh",
		claimPattern: "round-trips a scratch tracker",
		testName:     "TestDocClaim_InstallRunsRoundTripSmoke",
	},
	// hook-timeout-* (act-8ee085 follow-through): the hook limit is a host
	// property, settable with ACT_HOOK_TIMEOUT, and a hook act KILLED is
	// reported as a timeout rather than borrowing the wording of a non-zero
	// exit. Drift shape: the timeout goes back to a constant, or the cause
	// gets dropped again on the way to the error envelope — either way a
	// killed gate starts reading like a failing one, which is how this was
	// found.
	{
		name:         "hook-timeout-configurable",
		docFile:      "docs/spec.md",
		claimPattern: "`ACT_HOOK_TIMEOUT` environment variable",
		testName:     "TestDocClaim_HookTimeoutIsConfigurable",
	},
	{
		name:         "hook-timeout-reported-as-timeout",
		docFile:      "docs/spec.md",
		claimPattern: "reported as a TIMEOUT, not as a non-zero exit",
		testName:     "TestDocClaim_HookTimeoutReportedAsTimeout",
	},
	// The end-to-end half of the same claim: a hook that outlives the limit
	// is killed, and the SAME hook passes when the environment raises it.
	// The unit tests above can both pass while a constant stays wired in
	// somewhere they do not reach; this one runs the real close path.
	{
		name:         "hook-timeout-raised-lets-slow-hook-pass",
		docFile:      "docs/spec.md",
		claimPattern: "an unparseable or non-positive value falls back to the default",
		testName:     "TestDocClaim_SlowHookSurvivesRaisedTimeout",
	},
	// pre-commit-hook-permits-deletions (act-4094c6): the host
	// pre-commit hook installed by `act init` permits staged deletions
	// of `.act/*` paths so a normal `git commit` works for the untrack
	// shape (manual `git rm -r --cached .act/` carries to sibling
	// branches that still track `.act/`). Documented in
	// docs/coordination-plane-design.md (Phase 1 delta item 7)
	// alongside the addition/modification rejection rule. Drift
	// shape: a future hook-rewrite drops `--diff-filter=d` and
	// re-rejects deletions, breaking the untrack-carry workflow
	// without any test signal.
	{
		name:         "pre-commit-hook-permits-deletions",
		docFile:      "docs/coordination-plane-design.md",
		claimPattern: "Staged deletions of `.act/*` are permitted",
		testName:     "TestDocClaim_PreCommitHook_PermitsStagedDeletions",
	},
	// doctor-fix-index-* (act-f2f93a): the `--fix-index` flag rebuilds a
	// malformed `.act/index.db` from `.act/ops/`. The user-visible literal
	// claim is the remediation hint that surfaces when doctor sees the
	// corruption without `--fix-index`. The spec section ALSO documents
	// the flag and the recovery semantics — both surfaces have to keep
	// naming the literal or a cold-start agent reading either gets misled
	// about how to recover.
	{
		name:         "doctor-fix-index-remediation-hint",
		docFile:      "docs/spec.md",
		claimPattern: "rebuild with 'act doctor --fix-index'",
		testName:     "TestDocClaim_DoctorFixIndex_StderrRemediationHint",
	},
	// dep-direction-* (act-982a): the display strings for blocks-type
	// deps now read in the actual semantic direction. The four entries
	// below pin (a) the dep add success-line phrasing "is blocked by"
	// in internal/cli/depadd.go's FormatDepAddHuman, (b) the show
	// rendering "blocked-by" in internal/cli/show.go's depShowLabel,
	// (c) the --type flag-help primer in cmd/act/depadd.go, and (d)
	// the same primer surfaced via `act help workflow` in
	// cmd/act/help.go. Drift shape: a refactor reverts to the
	// "A blocks B" reading and a cold-start agent files deps backwards
	// (the original bug).
	{
		name:         "dep-direction-add-blocked-by",
		docFile:      "internal/cli/depadd.go",
		claimPattern: "is blocked by",
		testName:     "TestDocClaim_DepDirection_AddBlocksReadsAsBlockedBy",
	},
	{
		name:         "dep-direction-show-blocked-by-label",
		docFile:      "internal/cli/show.go",
		claimPattern: "blocked-by",
		testName:     "TestDocClaim_DepDirection_ShowRendersBlockedBy",
	},
	{
		name:         "dep-direction-flag-help-primer",
		docFile:      "cmd/act/depadd.go",
		claimPattern: "A is blocked by B; A is hidden from ready until B closes",
		testName:     "TestDocClaim_DepDirection_FlagHelpPrimer",
	},
	{
		name:         "dep-direction-workflow-primer",
		docFile:      "cmd/act/help.go",
		claimPattern: "A is blocked by B; A is hidden from ready until B closes",
		testName:     "TestDocClaim_DepDirection_HelpPrimerInWorkflow",
	},
	// dep-direction-mcp-* (act-59ab1a, act-03cd05, act-e0a672): the MCP
	// surfaces carry the same direction discipline as the CLI strings
	// above, since an inverted reading of child/parent is exactly what
	// misled a cold-start agent. These pin: the dep_add `summary` field
	// in the spec JSON output; the natural-direction dep_rm not-found
	// message (no `--[blocks]-->` arrow); the act_create blocked_by/blocks
	// schema params; the act_dep_add worked example; and the act_show
	// directional labels for blocked_by/blocks.
	{
		name:         "dep-add-summary-spec",
		docFile:      "docs/spec.md",
		claimPattern: `"summary":"act-a1b2`,
		testName:     "TestDocClaim_DepAdd_SummaryDirection",
	},
	{
		name:         "dep-rm-natural-direction",
		docFile:      "internal/cli/update.go",
		claimPattern: "no %s-edge to remove",
		testName:     "TestDocClaim_DepRm_NaturalDirectionNoArrow",
	},
	{
		name:         "mcp-create-blocked-by-param",
		docFile:      "internal/mcp/server.go",
		claimPattern: "Ids the NEW issue is blocked by",
		testName:     "TestDocClaim_MCP_CreateBlockedByAndBlocks",
	},
	{
		name:         "mcp-create-blocks-param",
		docFile:      "internal/mcp/server.go",
		claimPattern: "Existing ids the NEW issue blocks",
		testName:     "TestDocClaim_MCP_CreateBlockedByAndBlocks",
	},
	{
		// act-8a6536: short_id was duplicating id on every row of every
		// listing (ids are generated at the prefix floor, so the two are
		// byte-identical unless an id was extended). The spec now states
		// the emit-only-when-different rule AND the consumer-side reading
		// of an absent key; the test pins both directions at the JSON
		// boundary. Drift shape: the omit rule swallows the informative
		// case too, leaving an extended id with no short handle anywhere.
		name:         "shortid-omitted-when-same-as-id",
		docFile:      "docs/spec.md",
		claimPattern: "emits `short_id` **only when it differs from `id`**",
		testName:     "TestDocClaim_ShortID_OmittedWhenSameAsID",
	},
	{
		// act-8a6536, skill side: the loop-facing statement of the same
		// rule, which is where an agent reads it. Its asserting test is the
		// end-to-end one — an extended id still renders its prefix through
		// `act show`, so "absent means identical" never costs a caller the
		// handle it needs.
		name:         "skill-shortid-only-when-different",
		docFile:      "skills/act/SKILL.md",
		claimPattern: "`short_id` is emitted only when it differs from `id`",
		testName:     "TestDocClaim_ShortID_ShowEmitsPrefixForExtendedID",
	},
	{
		// act-8a6536: the MCP surface advertises eight tools; the rest stay
		// reachable as identically-named CLI verbs and still dispatch over
		// MCP for clients holding a cached list. Drift shape: a tool is
		// dropped from exposedTools AND from invoke(), which would break a
		// live session mid-flight rather than just slimming what it reads.
		name:         "mcp-unadvertised-tools-still-reachable",
		docFile:      "docs/spec.md",
		claimPattern: "remain dispatchable over MCP for clients holding a cached tool list",
		testName:     "TestDocClaim_MCP_UnadvertisedToolsStillDispatch",
	},
	{
		// act-ca659d: read_only left every tool schema (2,048 B of the
		// advertised surface) because nothing enforced it per-call. The spec
		// now says so; the test pins both halves — schemas carry no
		// read_only/no_commit/isolated property, and --read-only still
		// refuses writes. Drift shape: someone re-adds the advisory param,
		// or the trim quietly takes the enforcement with it.
		name:         "mcp-read-only-is-server-level",
		docFile:      "docs/spec.md",
		claimPattern: "no tool schema advertises a `read_only` parameter",
		testName:     "TestDocClaim_MCP_ReadOnlyIsServerLevelNotPerCallParam",
	},
	{
		// act-ca659d moved the dep-add direction worked example OUT of the
		// MCP schema description (re-read every turn) and INTO the skill
		// (read once). The behavior claim is unchanged and the same test
		// still asserts it at the tool boundary; only the doc surface moved.
		name:         "mcp-dep-add-direction-example",
		docFile:      "skills/act/SKILL.md",
		claimPattern: "to make act-A block act-B (B must wait on A), call child=act-B, parent=act-A",
		testName:     "TestDocClaim_MCP_DepAddDirectionExample",
	},
	{
		// act-ce1427: external blockers are added via `act dep add --external`
		// (unified with internal --blocked-by), removed via `act update
		// --ext-rm`. The EXTERNAL DEPS help section documents the add form;
		// TestDocClaim_BlockedByExtDep_ClaimBlocked exercises it at the
		// subprocess boundary (it attaches a ref via `dep add --external`,
		// then asserts the claim gate fires on the blocking ref).
		name:         "act-help-dep-add-external",
		docFile:      "cmd/act/help.go",
		claimPattern: "act dep add <id> --external",
		testName:     "TestDocClaim_BlockedByExtDep_ClaimBlocked",
	},
	{
		name:         "mcp-show-direction-labels",
		docFile:      "internal/mcp/server.go",
		claimPattern: "lists the issues that block THIS issue",
		testName:     "TestDocClaim_Show_BlocksAndBlockedByJSON",
	},
	// op-filename-* (act-2f3d): op filenames use '-' rather than ':' in
	// the time component so the on-disk tree is NTFS-safe (otherwise
	// `git checkout` on Windows hosts fails before any Go code runs).
	// The spec's "Op file naming" section is the load-bearing surface
	// for the format claim; the test asserts the form is documented and
	// that the writer produces no-colon filenames at the boundary.
	{
		name:         "op-filename-ntfs-safe-format",
		docFile:      "docs/spec.md",
		claimPattern: "`YYYY-MM-DDTHH-MM-SS.sssZ`",
		testName:     "TestDocClaim_OpFilename_NoColon",
	},
	// branch-flag-* (act-5d6a): the --branch <ref> universal write flag
	// pins both the auto-commit and the publish to a named branch in the
	// nested .act/ repo, decoupling them from HEAD / tracking config.
	// Worktree subagents pass --branch <worktree-branch> so concurrent
	// agents don't fan their op commits onto origin/main.
	{
		name:         "branch-flag-help-overview",
		docFile:      "cmd/act/help.go",
		claimPattern: "--branch <ref>",
		testName:     "TestDocClaim_BranchFlag_AutoCommitTargetsNamedBranch",
	},
	{
		name:         "branch-flag-create-help",
		docFile:      "cmd/act/create.go",
		claimPattern: "branch in the nested .act/ repo",
		testName:     "TestDocClaim_BranchFlag_AutoCommitTargetsNamedBranch",
	},
	// ux-polish-* (act-f2c7): bundled UX polish pass — bare-`act` help
	// hint, comma-separated subcommand listing (so `dep add` isn't read
	// as three items), `act init` Next-step hint, and the
	// create/update `--description` consistency note. Each entry pins
	// the literal in the surface a cold-start agent would actually
	// hit; the drift shape is that a future cleanup pass strips one of
	// the literals (e.g. drops "act help" from the bare-act usage
	// block) and we silently regress to the pre-polish UX.
	{
		name:         "ux-bare-act-help-hint",
		docFile:      "cmd/act/main.go",
		claimPattern: "run 'act help' for the full subcommand tutorial",
		testName:     "TestDocClaim_BareAct_ListsSubcommandsAndHelpHint",
	},
	{
		name:         "ux-help-subcommand-list-comma-separated",
		docFile:      "cmd/act/help.go",
		claimPattern: "dep add, doctor, import, mcp,",
		testName:     "TestDocClaim_BareAct_DepAddNotThreeItems",
	},
	{
		name:         "ux-init-next-step-hint",
		docFile:      "cmd/act/main.go",
		claimPattern: "Next: run 'act create",
		testName:     "TestDocClaim_Init_NextStepHint",
	},
	{
		name:         "ux-description-consistency-note-create",
		docFile:      "cmd/act/create.go",
		claimPattern: "silently accepted as no-op-equivalent",
		testName:     "TestDocClaim_Description_CreateUpdateConsistencyNote",
	},
	{
		name:         "ux-description-consistency-note-update",
		docFile:      "cmd/act/update.go",
		claimPattern: "empty string explicitly clears the existing description",
		testName:     "TestDocClaim_Description_CreateUpdateConsistencyNote",
	},
	// cwd-robustness (act-0852da): all act commands resolve the host repo root
	// from any directory inside the project tree, including from inside .act/.
	// Before the fix, findRepoRoot stopped at the first .git encountered, which
	// under Phase 1 could be the nested .act/.git — causing "no act state" from
	// inside .act/. The claim lives in cmd/act/help.go's CWD ROBUSTNESS section
	// (helpOpsModel); the test asserts act doctor from cwd=<host>/.act/ returns
	// real output, not the no-state sentinel.
	{
		name:         "cwd-robustness-doctor-from-act-dir",
		docFile:      "cmd/act/help.go",
		claimPattern: "leave cwd inside .act/ after a cd; act skips the",
		testName:     "TestDocClaim_CWDRobustness_DoctorFromInsideActDir",
	},
	// close-no-doctor (act-1849a6): the --no-doctor flag on `act close`
	// skips the post-close single-issue commit-marker correlation check.
	// The flag-help string in cmd/act/close.go is the load-bearing claim;
	// the asserting test drives `act close <id> --no-doctor` as a subprocess
	// and confirms the marker-correlation warning is suppressed — the same
	// class of boundary-vs-internal gap that bit act-6fca / act-ac52.
	{
		name:         "close-no-doctor-flag-help",
		docFile:      "cmd/act/close.go",
		claimPattern: "skip the post-close single-issue commit-marker correlation check",
		testName:     "TestDocClaim_NoDoctorOptOut",
	},
	// claim-lost-last-write-wins (act-2af8c7): the README and `act help`
	// promise "concurrent claimers resolve last-write-wins." Spec §7.4
	// names the test case "concurrent_claim_two_writers" and says "Exactly
	// one exits 0 with {claimed:true}; the other exits 5 with claim_lost."
	// The asserting test drives two sequential `act update --claim`
	// subprocesses with different node_ids against the same issue and
	// verifies exactly one winner (exit 0, claimed:true) and one loser
	// (exit 5, claimed:false, error:claim_lost) at the subprocess boundary
	// (act-a373bb reconciled the loser to exit 5 per spec §7.4).
	{
		name:         "claim-lost-last-write-wins",
		docFile:      "README.md",
		claimPattern: "concurrent claimers resolve last-write-wins",
		testName:     "TestDocClaim_ClaimLost_LastWriteWins",
	},
	// close-reason-cap-* (act-54462e): `act help workflow` documents the
	// 500-byte cap on `--reason`. The prior tests `TestCloseReasonCap*` in
	// cmd/act/close_test.go were not named TestDocClaim_* and had no registry
	// entry, so deleting them would not trip the sweep. Renamed and registered
	// here so the drift guard catches both deletion of the assertion and
	// removal of the cap claim from help.go.
	{
		name:         "close-reason-cap-over-cap-rejected",
		docFile:      "cmd/act/help.go",
		claimPattern: "--reason is capped at 500 bytes",
		testName:     "TestDocClaim_CloseReasonCap_OverCapRejected",
	},
	{
		name:         "close-reason-cap-at-cap-accepted",
		docFile:      "cmd/act/help.go",
		claimPattern: "--reason is capped at 500 bytes",
		testName:     "TestDocClaim_CloseReasonCap_AtCapAccepted",
	},
	// close-reason-cap-multibyte / length-caps-* (act-1d044c): the reason
	// and accept caps are one shared constant, and docs/spec.md "Length
	// caps" names every write-time cap and says they count bytes, not
	// characters. The op-layer test pins every cap at its spec number; the
	// cmd/act test pins the byte count at the user-visible flag boundary.
	{
		name:         "close-reason-cap-multibyte-counts-bytes",
		docFile:      "cmd/act/help.go",
		claimPattern: "LENGTHS ARE BYTE-COUNTED",
		testName:     "TestDocClaim_CloseReasonCap_MultiByteCountsBytes",
	},
	{
		name:         "length-caps-spec-bytes-not-characters",
		docFile:      "docs/spec.md",
		claimPattern: "Every length limit on a written field is counted in **bytes**",
		testName:     "TestDocClaim_LengthCaps_ByteBoundary",
	},
	{
		name:         "length-caps-spec-reason",
		docFile:      "docs/spec.md",
		claimPattern: "| `reason` on `close`, `reopen`, `unclaim` (`act close`/`finish`/`reopen --reason`) | 500 bytes | `MaxReasonLen` |",
		testName:     "TestDocClaim_LengthCaps_ByteBoundary",
	},
	{
		name:         "length-caps-spec-accept-criterion",
		docFile:      "docs/spec.md",
		claimPattern: "| 500 bytes | `MaxAcceptCriterionLen` |",
		testName:     "TestDocClaim_LengthCaps_ByteBoundary",
	},
	{
		name:         "length-caps-spec-external-ref",
		docFile:      "docs/spec.md",
		claimPattern: "| external dep `ref` (`add_external_dep`, `remove_external_dep`) | 256 bytes | `MaxExternalRefLen` |",
		testName:     "TestDocClaim_LengthCaps_ByteBoundary",
	},
	// description-cap-* (act-993498): the description cap is one constant,
	// op.MaxDescriptionLen (1 MiB), enforced by the op payload validators
	// so every write path and `act import` runs through it. The op-layer
	// test pins the byte boundary; the others pin it at each user-visible
	// write boundary the spec row names.
	{
		name:         "description-cap-spec-schema",
		docFile:      "docs/spec.md",
		claimPattern: `"description":  "string, 0..1048576 bytes (1 MiB), default \"\""`,
		testName:     "TestDocClaim_LengthCaps_ByteBoundary",
	},
	{
		name:         "description-cap-spec-row-op",
		docFile:      "docs/spec.md",
		claimPattern: "| 1048576 bytes (1 MiB) | `MaxDescriptionLen` |",
		testName:     "TestDocClaim_LengthCaps_ByteBoundary",
	},
	{
		name:         "description-cap-spec-row-inline",
		docFile:      "docs/spec.md",
		claimPattern: "`act create`/`update --description`",
		testName:     "TestDocClaim_DescriptionCap_Inline",
	},
	{
		name:         "description-cap-spec-row-file-and-append",
		docFile:      "docs/spec.md",
		claimPattern: "`--description-file`, the merged result of `--description-append`/`--description-append-file`",
		testName:     "TestDocClaim_DescriptionCap_FileAndAppendFlags",
	},
	{
		name:         "description-cap-one-exit-code",
		docFile:      "docs/spec.md",
		claimPattern: "An over-cap description exits 2 with error `bad_flag` on every `act create`/`act update` path",
		testName:     "TestDocClaim_DescriptionCap_OneExitCode",
	},
	{
		name:         "description-cap-spec-row-mcp",
		docFile:      "docs/spec.md",
		claimPattern: "MCP `act_create`/`act_update`",
		testName:     "TestDocClaim_MCP_DescriptionCap",
	},
	{
		name:         "description-cap-spec-row-import",
		docFile:      "docs/spec.md",
		claimPattern: "`act import`) | 1048576 bytes (1 MiB)",
		testName:     "TestDocClaim_Import_DescriptionCap",
	},
	// show-include-ops (act-ddd458): the `--include-ops` flag-help string in
	// cmd/act/main.go is a user-visible claim. `TestRunShow_IncludeOpsHumanFormat`
	// covers the internal Go API, but no TestDocClaim_* existed at the subprocess
	// boundary. The asserting test drives `act show --include-ops <id>` and
	// asserts the op stream appears in the output.
	{
		name:         "show-include-ops-flag-help",
		docFile:      "cmd/act/main.go",
		claimPattern: "inline the HLC-sorted op stream alongside the snapshot",
		testName:     "TestDocClaim_IncludeOps_SubprocessShowsOpStream",
	},
	// title-cap-* (act-65b0ec): docs/spec.md states one 256-byte title cap
	// for `act create` and `act update --title`. Op-payload validation (what
	// `act import` runs) once capped at 200, so act's own issues did not
	// round-trip. The tests drive create/update/import through the binary.
	{
		name:         "title-cap-create-over-cap",
		docFile:      "docs/spec.md",
		claimPattern: "title >256 bytes → exit 2",
		testName:     "TestDocClaim_TitleCap_OverCapRejected",
	},
	{
		name:         "title-cap-create-max-round-trips-import",
		docFile:      "docs/spec.md",
		claimPattern: "title >256 bytes → exit 2",
		testName:     "TestDocClaim_TitleCap_MaxLengthRoundTripsThroughImport",
	},
	{
		name:         "title-cap-update-matches-create",
		docFile:      "docs/spec.md",
		claimPattern: "≤256 bytes, matching `act create`",
		testName:     "TestDocClaim_TitleCap_OverCapRejected",
	},
	// help-errors-exit-{3,4,5} (act-387e01, act-a373bb): `act help errors`
	// EXIT CODES block previously listed only exits 1 and 2. Exit 3
	// (issue_not_found) and exit 4 were added by act-387e01; exit 4's
	// label became bootstrap_timeout when act-89a595 retired the
	// push_exhausted envelope (a committed op no longer fails on push);
	// exit 5 (claim_lost) by act-a373bb when the lost-claim code was
	// reconciled to the spec. remote_unreachable is NOT listed under exit 4
	// (act-6d9546): it is a bootstrap-worker exit-3 outcome, not a close-path
	// one. The asserting test drives `act help errors` and asserts the
	// exit-3/4/5 labels appear in the output.
	{
		name:         "help-errors-exit-3-issue-not-found",
		docFile:      "cmd/act/help.go",
		claimPattern: "exit 3   issue_not_found",
		testName:     "TestDocClaim_HelpErrors_ExitCodesListsThreeFourFive",
	},
	{
		name:         "help-errors-exit-4-bootstrap-timeout",
		docFile:      "cmd/act/help.go",
		claimPattern: "exit 4   bootstrap_timeout",
		testName:     "TestDocClaim_HelpErrors_ExitCodesListsThreeFourFive",
	},
	{
		name:         "help-errors-exit-5-claim-lost",
		docFile:      "cmd/act/help.go",
		claimPattern: "exit 5   claim_lost",
		testName:     "TestDocClaim_HelpErrors_ExitCodesListsThreeFourFive",
	},
	// show-blocked-by-blocks (act-00e5cc): `act show --json <id>` includes
	// `blocked_by` and `blocks` arrays. The spec claim lives in
	// docs/spec.md (the schema section); the asserting test drives the
	// binary with a two-issue graph and checks both arrays in both
	// directions.
	{
		name:         "show-blocked-by-array",
		docFile:      "docs/spec.md",
		claimPattern: `"blocked_by": ["act-...", "..."]`,
		testName:     "TestDocClaim_Show_BlocksAndBlockedByJSON",
	},
	{
		name:         "show-blocks-array",
		docFile:      "docs/spec.md",
		claimPattern: `"blocks":     ["act-...", "..."]`,
		testName:     "TestDocClaim_Show_BlocksAndBlockedByJSON",
	},
	// dep-shape-spec (act-5918c7): the spec's dep-edge JSON shape must match
	// the emitted {parent, edge_type} form. The claimPattern pins the
	// canonical spec example; the asserting test drives the binary and
	// asserts the wire shape directly.
	{
		name:         "dep-shape-spec-parent-key",
		docFile:      "docs/spec.md",
		claimPattern: `{ "parent": "act-...", "edge_type": "blocks | relates | supersedes" }`,
		testName:     "TestDocClaim_Show_DepShapeMatchesSpec",
	},
	// dep-add-inspection-hint (act-00e5cc): `act dep add --help` must surface
	// the inspection hint pointing agents to `act show <id> --json` for the
	// `blocked_by / blocks arrays`.
	{
		name:         "dep-add-inspection-hint",
		docFile:      "cmd/act/depadd.go",
		claimPattern: "inspect with act show <id>",
		testName:     "TestDocClaim_DepAdd_InspectionHint",
	},
	// extdep-gate-* (act-5e36): external deps must gate `act update --claim`
	// and `act close`. The three entries below pin:
	//   - The spec table claim for `blocked_by_external_dep` (error code +
	//     exit 2 + details.external_deps shape).
	//   - The help-errors EXIT CODES block claim that names the code under
	//     exit 2.
	//   - The --force override behavior described in the --force flag-help
	//     string on `act close`.
	{
		name:         "extdep-gate-spec-code",
		docFile:      "docs/spec.md",
		claimPattern: "`blocked_by_external_dep`",
		testName:     "TestDocClaim_BlockedByExtDep_ClaimBlocked",
	},
	{
		name:         "extdep-gate-help-errors-exit-2",
		docFile:      "cmd/act/help.go",
		claimPattern: "blocked_by_external_dep",
		testName:     "TestDocClaim_BlockedByExtDep_CloseBlocked",
	},
	{
		name:         "extdep-gate-force-flag",
		docFile:      "cmd/act/close.go",
		claimPattern: "override open external dep gate",
		testName:     "TestDocClaim_BlockedByExtDep_ForceOverrides",
	},
	// accept-replace-* (act-844f97): `act update --accept` was documented as
	// "Replace acceptance criteria" (MCP) but implemented as append (one
	// add_accept op per criterion), so edits unioned. The fix emits a single
	// set_accept op that REPLACES the list, adds --accept-add for the additive
	// flow, and surfaces the pre-existing remove_accept op as --accept-rm so an
	// individual criterion can be removed/replaced. Five user-visible surfaces
	// pin the new semantics: the CLI --accept / --accept-add / --accept-rm
	// flag-help strings (cmd/act/update.go), the spec §"act update" flag block,
	// and the spec op-fold CRDT note. Each asserting test drives the binary as
	// a subprocess and reads the materialized list via `act show --json` — the
	// user-visible boundary, not the fold helper.
	{
		name:         "accept-replace-cli-flag-help",
		docFile:      "cmd/act/update.go",
		claimPattern: "the set REPLACES any prior criteria, it does not append",
		testName:     "TestDocClaim_AcceptReplace_SetReplacesNotUnion",
	},
	{
		name:         "accept-replace-spec-flag",
		docFile:      "docs/spec.md",
		claimPattern: "REPLACES the acceptance list with exactly the supplied criteria",
		testName:     "TestDocClaim_AcceptReplace_SetReplacesNotUnion",
	},
	{
		name:         "accept-replace-spec-set-accept-crdt",
		docFile:      "docs/spec.md",
		claimPattern: "`set_accept` REPLACES the whole list",
		testName:     "TestDocClaim_AcceptReplace_SetReplacesNotUnion",
	},
	{
		name:         "accept-rm-cli-flag-help",
		docFile:      "cmd/act/update.go",
		claimPattern: "remove an acceptance criterion by zero-based index",
		testName:     "TestDocClaim_AcceptRm_RemovesIndividualCriterion",
	},
	{
		name:         "accept-rm-spec-flag",
		docFile:      "docs/spec.md",
		claimPattern: "Replace an individual criterion with `--accept-rm N --accept-add",
		testName:     "TestDocClaim_AcceptRm_RemovesIndividualCriterion",
	},
	{
		name:         "accept-add-cli-flag-help",
		docFile:      "cmd/act/update.go",
		claimPattern: "append an acceptance criterion to the existing list",
		testName:     "TestDocClaim_AcceptAdd_AppendsToList",
	},
	{
		name:         "accept-replace-mcp-schema",
		docFile:      "internal/mcp/server.go",
		claimPattern: "the set REPLACES any prior criteria, it does not append",
		testName:     "TestDocClaim_MCP_AcceptReplacesNotUnion",
	},
	{
		name:         "accept-rm-mcp-schema",
		docFile:      "internal/mcp/server.go",
		claimPattern: "Remove acceptance criteria by zero-based index",
		testName:     "TestDocClaim_MCP_AcceptRmAndAdd",
	},
	{
		name:         "accept-add-mcp-schema",
		docFile:      "internal/mcp/server.go",
		claimPattern: "Append these criteria to the existing acceptance list",
		testName:     "TestDocClaim_MCP_AcceptRmAndAdd",
	},
	// blocked-requires-dep-* (act-36b6d3): `act update --status blocked <id>`
	// with no backing blocked-by dep edge exits 2 with blocked_requires_dep
	// instead of silently reporting false success. The two entries pin:
	//   - The spec claim in docs/spec.md (the blocked_requires_dep code name).
	//   - The flag-help claim in cmd/act/update.go (that 'blocked' is derived
	//     from blocked-by dep edges and agents should use --blocked-by instead).
	{
		name:         "blocked-requires-dep-spec-code",
		docFile:      "docs/spec.md",
		claimPattern: "blocked_requires_dep",
		testName:     "TestDocClaim_BlockedStatus_RequiresDepEdge",
	},
	{
		name:         "blocked-status-writes-no-op",
		docFile:      "docs/spec.md",
		claimPattern: "already blocked by its dep edge (no ops written)",
		testName:     "TestDocClaim_BlockedStatus_WritesNoOp",
	},
	{
		name:         "blocked-requires-dep-flag-help",
		docFile:      "cmd/act/update.go",
		claimPattern: "derived from blocked-by dep edges",
		testName:     "TestDocClaim_BlockedStatus_FlagHelpMentionsDerived",
	},
	// blocked-spec-derived-* (act-15d5e9): spec.md was corrected to state
	// that "blocked" is a DERIVED display state, not a stored/settable status.
	// The data-model status enum comment and the CRDT LWW table were both
	// updated; this entry pins the corrected wording so a future edit that
	// reintroduces "blocked" as a stored status is caught by go test.
	{
		name:         "blocked-spec-derived-display-state",
		docFile:      "docs/spec.md",
		claimPattern: `"blocked" is a DERIVED display state`,
		testName:     "TestDocClaim_BlockedStatus_SpecSaysDerived",
	},
	{
		name:         "blocked-spec-stored-values-enum",
		docFile:      "docs/spec.md",
		claimPattern: "Stored values are `open`, `in_progress`, and `closed` only",
		testName:     "TestDocClaim_BlockedStatus_SpecSaysDerived",
	},
	// state-export-json-* (act-d0a0a9): the `act state export --json` envelope
	// is the primary contract for the worktree-driver consumer. The
	// stateExportHelpText constant in cmd/act/state.go documents both output
	// branches in the --help text; the asserting tests drive the binary as a
	// subprocess and verify the envelope at the user-visible boundary.
	//
	// Drift shape: someone renames harvested_ops in HarvestResult or changes
	// the error code for the no-.act/ path, and a cold-start worktree-driver consumer
	// reading the --help text sees a contract that no longer matches.
	{
		name:         "state-export-json-success-harvested-ops",
		docFile:      "cmd/act/state.go",
		claimPattern: "JSON array of op-path strings",
		testName:     "TestDocClaim_StateExport_JSONSuccessEnvelope",
	},
	{
		name:         "state-export-json-error-worker-state-not-found",
		docFile:      "cmd/act/state.go",
		claimPattern: "worker_state_not_found",
		testName:     "TestDocClaim_StateExport_JSONNoActError",
	},
	// stale-lock-* (act-aef518; incident + defect in act-8fe6eb): an
	// interrupted auto-commit leaves a stale index.lock/HEAD.lock in
	// .act/.git/ and every act write then fails until the lock is removed —
	// with no doctor detection. The README "If a write is interrupted"
	// section documents the failure mode, the no-data-lost guarantee (the
	// op file lands before the commit), and the recovery sequence. The
	// asserting tests plant each lock and drive the binary as a subprocess,
	// then execute the documented recovery verbatim. Drift shape: a write-
	// path refactor stops failing loudly on a stale lock (or starts losing
	// the op file), and the README's recovery advice silently rots.
	{
		name:         "stale-lock-wedges-writes",
		docFile:      "README.md",
		claimPattern: "every act write fails until the lock is removed",
		testName:     "TestDocClaim_StaleLock_WedgesWrites",
	},
	// act-a3160b: a stage failure now withdraws its op like a commit
	// failure does. README says the issue is absent from show/list, the op
	// is preserved under .act/.failed-ops/<timestamp>/ops/, and the runbook
	// copies it back from there. The asserting test runs the README block
	// literally. Drift shape: the stage path goes back to leaving the op in
	// ops/ (exit and `act list` disagree again), or the runbook's cp step
	// stops matching where the op actually lands.
	{
		name:         "stale-lock-failed-write-absent",
		docFile:      "README.md",
		claimPattern: "issue does not appear in `act show` or `act list`",
		testName:     "TestDocClaim_StaleLock_OpSurvivesAndRecovers",
	},
	{
		name:         "stale-lock-op-preserved-in-failed-ops",
		docFile:      "README.md",
		claimPattern: "failed write's op file is moved aside to `.act/.failed-ops/<timestamp>/ops/`",
		testName:     "TestDocClaim_StaleLock_OpSurvivesAndRecovers",
	},
	{
		name:         "stale-lock-recovery-copies-back",
		docFile:      "README.md",
		claimPattern: "$ cp -R .act/.failed-ops/<timestamp>/ops/. .act/ops/",
		testName:     "TestDocClaim_StaleLock_OpSurvivesAndRecovers",
	},
	{
		name:         "help-errors-stage-failure-same-rule",
		docFile:      "cmd/act/help.go",
		claimPattern: "A STAGE failure follows the same rule.",
		testName:     "TestDocClaim_StaleLock_CloseStageFailureStaysOpen",
	},
	// act-b45379: the claim path (internal/claim, outside the shared write
	// helper) gets the same withdrawal, and close's commit step classifies
	// a stale HEAD.lock. Drift shape: claim goes back to leaving its op in
	// ops/, so `act update --claim` exits 1 while `act show` says
	// in_progress; or close's HEAD.lock falls back to commit_failed.
	{
		name:         "stale-lock-failed-claim-unclaimed",
		docFile:      "README.md",
		claimPattern: "a failed `act update --claim` leaves the issue unclaimed",
		testName:     "TestDocClaim_StaleLock_ClaimStageFailureStaysUnclaimed",
	},
	{
		name:         "help-errors-claim-commit-failure-unclaimed",
		docFile:      "cmd/act/help.go",
		claimPattern: "failed leaves the issue unclaimed.",
		testName:     "TestDocClaim_FailedCommitClaimStaysUnclaimed",
	},
	{
		name:         "help-errors-close-head-lock-stale-git-lock",
		docFile:      "cmd/act/help.go",
		claimPattern: "commit step rather than the stage, also reports stale_git_lock.",
		testName:     "TestDocClaim_StaleLock_CloseHeadLockCommitFailure",
	},
	{
		name:         "stale-lock-recovery-doctor-fix",
		docFile:      "README.md",
		claimPattern: "$ act doctor --fix",
		testName:     "TestDocClaim_StaleLock_OpSurvivesAndRecovers",
	},
	{
		// stale-lock structured write error (act-8fe6eb): a write that fails
		// on a stale git lock reports a first-class `stale_git_lock` error
		// naming the lock file and the recovery sequence, not git's stderr
		// buried in write_failed. README documents it; the subprocess test
		// plants each lock and asserts the JSON envelope's error code +
		// remedy. Drift shape: a write-path refactor drops the classification
		// and the failure falls back to the opaque write_failed message.
		name:         "stale-lock-structured-write-error",
		docFile:      "README.md",
		claimPattern: "structured `stale_git_lock` error",
		testName:     "TestDocClaim_StaleLock_StructuredWriteError",
	},
	{
		// stale-lock confirm-before-remove (act-94bbea): the recovery
		// confirms nothing still owns the lock (act serializes its own
		// writers on .act/.write.lock; check for a live git process)
		// BEFORE the rm, on the README runbook, `act help errors`, the
		// envelope remedy and doctor's finding. Drift shape: a remedy
		// rewrite drops or reorders the check and an agent deletes a
		// lock a live git process holds.
		name:         "stale-lock-confirm-before-remove-readme",
		docFile:      "README.md",
		claimPattern: "Before you remove anything, confirm nothing still owns the lock.",
		testName:     "TestDocClaim_StaleLock_RemedyConfirmsBeforeRemove",
	},
	{
		name:         "stale-lock-confirm-before-remove-help-errors",
		docFile:      "cmd/act/help.go",
		claimPattern: "Confirm the lock is really stale before removing it.",
		testName:     "TestDocClaim_StaleLock_RemedyConfirmsBeforeRemove",
	},
	{
		// stale-lock doctor detection (act-8fe6eb): `act doctor` detects a
		// lingering index.lock/HEAD.lock as an error finding, closing the
		// asymmetry where it caught the index divergence but not the lock
		// that caused it. README documents it; the subprocess test plants
		// each lock and asserts the finding. Drift shape: the check is
		// dropped from allChecks and a wedged tracker goes undiagnosed.
		name:         "stale-lock-doctor-detects",
		docFile:      "README.md",
		claimPattern: "detects the lingering lock",
		testName:     "TestDocClaim_StaleLock_DoctorDetects",
	},
	{
		// act update --status open releases a claim (act-bf9e9d): the flag
		// help documents that --status open returns an in_progress issue to
		// open and clears the assignee (routing to the unclaim op so the
		// claim high-water mark is cleared and the issue re-enters `act
		// ready`). Previously it wrote a no-op update_field{status:open} op
		// that reported success while the projection stayed in_progress.
		// TestDocClaim_StatusOpen_ReleasesClaim exercises claim -> --status
		// open -> re-claim at the subprocess boundary. Drift shape: someone
		// reverts --status open to the silent-no-op update_field path.
		name:         "update-status-open-releases-claim",
		docFile:      "cmd/act/update.go",
		claimPattern: "`--status open` releases a claim",
		testName:     "TestDocClaim_StatusOpen_ReleasesClaim",
	},
	// list-truncation-* (act-b50d81): `act list` capped at 200 rows and said
	// nothing, so a caller piping it into a filter got a confident
	// under-count of open work — it closed a p1 ticket on a wrong number the
	// night it was found. Three surfaces now carry the contract: the --limit
	// flag help (both the escape hatch and the warning), and the `act help
	// workflow` COUNTING FROM A LISTING block that tells an agent not to
	// count from a piped default listing at all.
	//
	// Drift shape: someone "tidies up" by moving the notice from stderr into
	// the row stream (breaking every pipe consumer's line count), or
	// re-introduces the `--limit 0 is not allowed` rejection and removes the
	// only way to ask for a complete listing.
	{
		name:         "list-limit-zero-means-no-limit",
		docFile:      "cmd/act/main.go",
		claimPattern: "--limit 0 means no limit",
		testName:     "TestDocClaim_List_LimitZeroReturnsEverything",
	},
	{
		name:         "list-capped-warns-on-stderr",
		docFile:      "cmd/act/main.go",
		claimPattern: "prints a WARNING to stderr naming how many issues were hidden",
		testName:     "TestDocClaim_List_CappedListingWarnsOnStderr",
	},
	{
		name:         "list-counting-guidance-in-help",
		docFile:      "cmd/act/help.go",
		claimPattern: "Never derive a count by piping a default 'act list' into a",
		testName:     "TestDocClaim_List_CappedListingWarnsOnStderr",
	},
	// list-working-set-* (act-9dfdc1): `act list` listed every status,
	// closed included, so the default listing of a mature tracker was
	// almost entirely finished work — and the 200-row cap then spent its
	// budget on closed rows while open work fell off the end. The default
	// is now the working set. Three surfaces carry it: the --status and
	// --all flag help, the `act help workflow` block an agent reads before
	// counting anything, and the act_list MCP schema.
	//
	// Drift shape: someone "restores" the old everything-by-default in the
	// name of compatibility, or keeps the default but lets closed rows
	// float back above open work under --all (the priority-asc sort does
	// exactly that on its own, which is why the grouping is not a sort
	// key).
	{
		name:         "list-default-excludes-closed",
		docFile:      "cmd/act/main.go",
		claimPattern: "default lists the working set and excludes closed issues",
		testName:     "TestDocClaim_List_DefaultExcludesClosed",
	},
	{
		name:         "list-all-flag-includes-closed-last",
		docFile:      "cmd/act/main.go",
		claimPattern: "include closed issues too; closed rows sort after everything still open",
		testName:     "TestDocClaim_List_ClosedSortAfterOpenWork",
	},
	{
		name:         "list-working-set-in-help",
		docFile:      "cmd/act/help.go",
		claimPattern: "'act list' lists the WORKING SET",
		testName:     "TestDocClaim_List_DefaultExcludesClosed",
	},
	{
		name:         "list-closed-reachable-in-help",
		docFile:      "cmd/act/help.go",
		claimPattern: "act list --status closed",
		testName:     "TestDocClaim_List_ClosedReachableViaStatusAndAll",
	},
	{
		// MCP parity: act_list is the surface most agents actually call,
		// so the default and its escape hatch must hold there too.
		name:         "mcp-list-default-excludes-closed",
		docFile:      "internal/mcp/server.go",
		claimPattern: "closed issues are excluded",
		testName:     "TestDocClaim_MCP_ListDefaultExcludesClosed",
	},
	// readonly-stray-args + description-append (act-a79d66): `act log <id>
	// "message"` silently swallowed the message — two agents lost four
	// annotations across three trackers that way. The rejection is half the
	// fix; the other half is that act now HAS a note-append path, so the
	// rejection can name a real command instead of being a dead end.
	//
	// Drift shape: a refactor drops the arity check and the silent swallow
	// returns, or --description-append is removed and the error message
	// starts pointing at a flag that doesn't exist.
	{
		name:         "readonly-verbs-reject-stray-args",
		docFile:      "cmd/act/help.go",
		claimPattern: "is rejected (exit 2) rather than",
		testName:     "TestDocClaim_ReadOnlyVerbs_RejectStrayPositionals",
	},
	{
		name:         "update-description-append-flag-help",
		docFile:      "cmd/act/update.go",
		claimPattern: "append this text to the existing description instead of replacing it",
		testName:     "TestDocClaim_Update_DescriptionAppendAppends",
	},
	{
		name:         "help-workflow-description-append",
		docFile:      "cmd/act/help.go",
		claimPattern: "act update <id> --description-append",
		testName:     "TestDocClaim_Update_DescriptionAppendAppends",
	},
	{
		// The skill is the surface an agent has loaded when it reaches for
		// "how do I leave a note on this ticket" — the question that
		// produced `act log <id> "message"` in the first place.
		name:         "skill-description-append-note",
		docFile:      "skills/act/SKILL.md",
		claimPattern: "act update <id> --description-append",
		testName:     "TestDocClaim_Update_DescriptionAppendAppends",
	},
	{
		// The file twin of --description-append (act-0bec25). A session
		// record is a paragraph with quotes and newlines in it — the
		// payload a single argv token handles worst — so without this
		// flag the caller falls back to the read-modify-write the append
		// path exists to remove.
		//
		// Drift shape: someone folds the two flags into one and drops the
		// file form, or lets it be paired with a replace flag so the
		// ticket body is clobbered by the "append" that was asked for.
		name:         "update-description-append-file-flag-help",
		docFile:      "cmd/act/update.go",
		claimPattern: "append the contents of this file to the existing description",
		testName:     "TestDocClaim_Update_DescriptionAppendFile",
	},
	{
		name:         "skill-description-append-file",
		docFile:      "skills/act/SKILL.md",
		claimPattern: "act update <id> --description-append-file <path>",
		testName:     "TestDocClaim_Update_DescriptionAppendFile",
	},
	// ready-truncation-* (act-1b816e): `act ready` capped at 50 rows,
	// emitted no truncation signal at all, and ignored `--limit 0` — so on
	// the same store `--limit 0` returned 50 while `--limit 500` returned
	// 99, and `act list` had honoured `--limit 0` since act-b50d81. Any
	// aggregator counting ready work across stores silently under-reported
	// with exit 0.
	//
	// Drift shape: the 50-row default migrates back INTO RunReady (which
	// is what made `--limit 0` mean 50), or the notice moves from stderr
	// into the row stream and corrupts every `--json | jq` consumer.
	{
		name:         "ready-limit-zero-means-no-limit",
		docFile:      "cmd/act/ready.go",
		claimPattern: "--limit 0 means no limit (return every ready issue)",
		testName:     "TestDocClaim_Ready_LimitZeroReturnsEverything",
	},
	{
		name:         "ready-capped-warns-on-stderr",
		docFile:      "cmd/act/ready.go",
		claimPattern: "A capped ready set prints a WARNING to stderr naming how many issues were hidden",
		testName:     "TestDocClaim_Ready_CappedSetWarnsOnStderr",
	},
	{
		name:         "ready-counting-guidance-in-help",
		docFile:      "cmd/act/help.go",
		claimPattern: "'act ready' caps at 50 rows",
		testName:     "TestDocClaim_Ready_CappedSetWarnsOnStderr",
	},
	{
		name:         "skill-ready-truncation",
		docFile:      "skills/act/SKILL.md",
		claimPattern: "`act ready` caps at 50 rows",
		testName:     "TestDocClaim_Ready_CappedSetWarnsOnStderr",
	},
	// listings-claim-age (act-d627c8): claim AGE was reachable only via one
	// `act show` subprocess per in-progress id, so a cross-store aggregator
	// paid unbounded fan-out on exactly the projects with the most stale
	// claims. Additive: claimed_at on `act list --json`, created_at on
	// `act ready --json`.
	//
	// Drift shape: a payload-slimming pass drops the field again as
	// "redundant with show".
	{
		name:         "spec-list-carries-claimed-at",
		docFile:      "docs/spec.md",
		claimPattern: "so claim age is readable from a listing without one `act show` per in-progress id",
		testName:     "TestDocClaim_Listings_CarryClaimAge",
	},
	{
		// MCP parity: act_update advertises description_append, and
		// `description` says it REPLACES so an agent can tell the pair
		// apart. The asserting test drives tools/list AND a real
		// tools/call, so a schema that advertises the param while the
		// handler drops it — this ticket's own defect shape — fails.
		name:         "mcp-update-description-append",
		docFile:      "internal/mcp/server.go",
		claimPattern: "Append this text to the existing description",
		testName:     "TestDocClaim_MCP_UpdateDescriptionAppend",
	},
	// act-94272e: a write whose commit failed must not read back as
	// having happened. `act help errors` now states the semantics act
	// chose (invisible until committed) and the recovery affordance
	// (envelope preserved under .act/.failed-ops, path reported). Drift
	// shape: someone restores the old "leave the op file in ops/ so the
	// user can retry" behavior, and the exit code and `act show` go back
	// to disagreeing — silently, because both surfaces still "work".
	{
		name:         "help-errors-op-invisible-until-committed",
		docFile:      "cmd/act/help.go",
		claimPattern: "AN OP WHOSE COMMIT FAILED IS INVISIBLE",
		testName:     "TestDocClaim_FailedCommitStaysUnclosed",
	},
	{
		name:         "help-errors-failed-op-envelope-preserved",
		docFile:      "cmd/act/help.go",
		claimPattern: ".act/.failed-ops/<timestamp>/ops/...",
		testName:     "TestDocClaim_FailedCommitPreservesEnvelope",
	},
	{
		name:         "help-errors-failed-op-details-key",
		docFile:      "cmd/act/help.go",
		claimPattern: "details.quarantined_op",
		testName:     "TestDocClaim_FailedCommitCreateStaysInvisible",
	},
	// act-fec192: `act show` can report a status that only the working
	// tree supports, because the fold reads ops/ and nothing compares
	// ops/ against the nested repo's committed history. Three sessions
	// passed such a close between them and the store later disagreed
	// with all three. `act help` now documents the two shapes doctor's
	// status-vs-oplog check reports. Drift shape: the check is narrowed
	// back to "index vs ops on disk" (which index-divergence already
	// does, and which was silent through the whole incident), and the
	// help text keeps promising a comparison nothing performs.
	{
		name:         "help-doctor-status-vs-oplog-retracted",
		docFile:      "cmd/act/help.go",
		claimPattern: "A STATUS THE OP LOG DOES NOT SUPPORT",
		testName:     "TestDocClaim_StatusVsOplogReportsRetractedOp",
	},
	{
		name:         "help-doctor-status-vs-oplog-not-durable",
		docFile:      "cmd/act/help.go",
		claimPattern: "this status is not durable yet",
		testName:     "TestDocClaim_StatusVsOplogReportsUncommittedOp",
	},
	// act-fec192, the other half: doctor is a diagnostic someone has to
	// think to run, and the readers who got this wrong were running
	// `act show`. Show now warns on stderr, in both modes, when the
	// status it is about to report is not the one the committed op log
	// supports. Drift shape: the warning is made --json-only or
	// human-only, or quietly dropped as noise, and the surface that
	// misled three sessions goes back to saying nothing.
	{
		name:         "help-show-durability-warning",
		docFile:      "cmd/act/help.go",
		claimPattern: "'act show' CARRIES THE SAME WARNING",
		testName:     "TestDocClaim_ShowWarnsStatusNotDurable",
	},
	{
		name:         "help-show-durability-behind",
		docFile:      "cmd/act/help.go",
		claimPattern: "is BEHIND the committed op log",
		testName:     "TestDocClaim_ShowWarnsStatusBehindOplog",
	},
	// act-7b9c54: fourth stale pass over docs/spec.md. Six passages
	// described things the code does not do: a version_skew envelope with
	// details keys nothing emits, a per-op_version apply registry the fold
	// never consults, an index schema with the wrong tables and indices, an
	// `act migrate --dry-run` that was never a flag, a compaction pass no
	// command runs, and a doctor exit-code line that stopped at 2. Drift
	// shape for each: the passage is "tidied" back toward the design it
	// was written from and the test named here goes red.
	{
		name:         "spec-version-skew-row-details-keys",
		docFile:      "docs/spec.md",
		claimPattern: "`max_op_version`, `writer_version`, `binary_version`",
		testName:     "TestDocClaim_Spec_VersionSkewRowMatchesCheckRepo",
	},
	{
		name:         "spec-version-skew-row-message",
		docFile:      "docs/spec.md",
		claimPattern: "`repo writer_version <N> exceeds this binary's writer_version <M>; upgrade act`",
		testName:     "TestDocClaim_Spec_VersionSkewRowMatchesCheckRepo",
	},
	{
		name:         "spec-compaction-not-wired-in",
		docFile:      "docs/spec.md",
		claimPattern: "no command, write path or MCP tool calls it, so a running `act` never compacts",
		testName:     "TestDocClaim_Compaction_NotWiredAndEngineCommitSubject",
	},
	{
		name:         "spec-compaction-engine-commit-subject",
		docFile:      "docs/spec.md",
		claimPattern: "with subject `act-compact: <N> issues`",
		testName:     "TestDocClaim_Compaction_NotWiredAndEngineCommitSubject",
	},
	{
		name:         "spec-fold-dispatch-by-op-type-only",
		docFile:      "docs/spec.md",
		claimPattern: "Dispatch is on `op_type` alone",
		testName:     "TestDocClaim_Fold_DispatchIsByOpTypeOnly",
	},
	{
		name:         "spec-fold-no-dispatch-on-op-version",
		docFile:      "docs/spec.md",
		claimPattern: "There is no dispatch on `op_version`",
		testName:     "TestDocClaim_Fold_DispatchIsByOpTypeOnly",
	},
	{
		// Already asserted by the third pass; the reworded dispatch
		// paragraph restates it, so it is tracked here too.
		name:         "spec-fold-unknown-op-version-never-dispatched",
		docFile:      "docs/spec.md",
		claimPattern: "never reaches dispatch at all: it fails the envelope's equality check and the fold aborts",
		testName:     "TestDocClaim_NewerOpVersionAbortsFoldWithoutVersionSkew",
	},
	{
		name:         "spec-index-schema-block",
		docFile:      "docs/spec.md",
		claimPattern: "CREATE VIRTUAL TABLE fts USING fts5(",
		testName:     "TestDocClaim_Spec_IndexSchemaBlockMatchesIndex",
	},
	{
		name:         "spec-index-schema-counts",
		docFile:      "docs/spec.md",
		claimPattern: "That block is the whole schema: seven tables, three indices",
		testName:     "TestDocClaim_Spec_IndexSchemaBlockMatchesIndex",
	},
	{
		name:         "spec-migrate-synopsis",
		docFile:      "docs/spec.md",
		claimPattern: "**Synopsis:** `act migrate --from <N> --to <M>`",
		testName:     "TestDocClaim_Migrate_FlagsAndExitCodes",
	},
	{
		name:         "spec-migrate-no-dry-run",
		docFile:      "docs/spec.md",
		claimPattern: "no `--dry-run` and no `--json`",
		testName:     "TestDocClaim_Migrate_FlagsAndExitCodes",
	},
	{
		name:         "spec-migrate-exits-5-today",
		docFile:      "docs/spec.md",
		claimPattern: "every well-formed `act migrate` call today exits 5 and writes nothing",
		testName:     "TestDocClaim_Migrate_FlagsAndExitCodes",
	},
	{
		name:         "spec-migrate-op-payload",
		docFile:      "docs/spec.md",
		claimPattern: `{"from_version": 1, "to_version": 2, "transform_id": "<migration description, or v1-to-v2>"}`,
		testName:     "TestDocClaim_Migrate_PayloadOutputAndExitCodes",
	},
	{
		name:         "spec-migrate-output-shape",
		docFile:      "docs/spec.md",
		claimPattern: `{"migrated_issues": <count>, "wrote_ops": <count>}`,
		testName:     "TestDocClaim_Migrate_PayloadOutputAndExitCodes",
	},
	{
		name:         "spec-doctor-exit-4-origin-unreachable",
		docFile:      "docs/spec.md",
		claimPattern: "4 when `origin` is configured but unreachable",
		testName:     "TestDocClaim_Doctor_ExitCodes",
	},
	{
		name:         "spec-doctor-exit-3-not-in-git",
		docFile:      "docs/spec.md",
		claimPattern: "3 when the working directory is not inside a git working tree (`not_in_git`)",
		testName:     "TestDocClaim_Doctor_ExitCodes",
	},
	{
		name:         "spec-doctor-no-state-exits-0",
		docFile:      "docs/spec.md",
		claimPattern: "it prints the no-state line and exits 0",
		testName:     "TestDocClaim_Doctor_ExitCodes",
	},
	{
		name:         "spec-claim-commit-subject-short-id",
		docFile:      "docs/spec.md",
		claimPattern: `git commit --no-verify -m "act-op: (<short-id>) claim"`,
		testName:     "TestDocClaim_Spec_ClaimCommitSubjectUsesSixHexShortID",
	},
	// act-77b411: `act version --check-repo` used the working directory as
	// the repo root and reported no_repo from a subdirectory.
	{
		name:         "spec-exit-codes-version-check-repo-same-from-any-subdirectory",
		docFile:      "docs/spec.md",
		claimPattern: "so it gives the same answer from any subdirectory of the repo",
		testName:     "TestDocClaim_VersionCheckRepo_ResolvesRepoRootFromSubdirectory",
	},
	{
		name:         "spec-exit-codes-version-check-repo-not-in-git",
		docFile:      "docs/spec.md",
		claimPattern: "3 outside a git working tree (`not_in_git`), when the repo has no `.act/` (`no_repo`), or when `.act/` cannot be read",
		testName:     "TestDocClaim_VersionCheckRepo_ResolvesRepoRootFromSubdirectory",
	},
	// act-377d38: the skill teaches capturing a new ticket's id from
	// `act create --json` rather than from the sentence `act create`
	// prints. Asserted against the real binary, and by running the skill's
	// own snippet, in cmd/act/skillcap_docclaim_test.go. Drift shape: the
	// snippet loses its exit-status check or its `jq -e`, so a failed
	// create flows into the next command with an empty or `null` id.
	{
		name:         "skill-create-id-capture-from-json",
		docFile:      "skills/act/SKILL.md",
		claimPattern: "read it from `act create --json`",
		testName:     "TestDocClaim_Skill_CreateJSONCarriesIDFailedCreateDoesNot",
	},
	{
		name:         "skill-create-failed-exits-nonzero-json-without-id",
		docFile:      "skills/act/SKILL.md",
		claimPattern: "create that fails exits non-zero and still writes JSON to stdout: an error envelope with",
		testName:     "TestDocClaim_Skill_CreateJSONCarriesIDFailedCreateDoesNot",
	},
	{
		name:         "skill-create-id-capture-plain-jq-prints-null",
		docFile:      "skills/act/SKILL.md",
		claimPattern: "a plain `jq -r .id` prints the word `null`",
		testName:     "TestDocClaim_Skill_CreateIDCaptureSnippetStopsOnFailedCreate",
	},
	{
		name:         "skill-create-id-capture-snippet-exit-status",
		docFile:      "skills/act/SKILL.md",
		claimPattern: `out=$(act create --json "<title>" --type task) || { echo "$out" >&2; exit 1; }`,
		testName:     "TestDocClaim_Skill_CreateIDCaptureSnippetStopsOnFailedCreate",
	},
	{
		name:         "skill-create-id-capture-snippet-jq-e",
		docFile:      "skills/act/SKILL.md",
		claimPattern: `id=$(jq -er .id <<<"$out") || exit 1`,
		testName:     "TestDocClaim_Skill_CreateIDCaptureSnippetStopsOnFailedCreate",
	},
	// act-e5d6b4: doctor polish after fold-error. remote_reachable was
	// false on every run that never reached phase2-reconciliation; the
	// fold-error finding was not echoed to stderr; `version --check-repo`
	// skipped the tracker-remote guard.
	{
		name:         "spec-doctor-remote-reachable-false-only-when-probe-failed",
		docFile:      "docs/spec.md",
		claimPattern: "only when the probe ran and failed.",
		testName:     "TestDocClaim_Doctor_RemoteReachableFalseOnlyWhenProbeFailed",
	},
	{
		name:         "spec-doctor-fold-error-echoed-bare-to-stderr",
		docFile:      "docs/spec.md",
		claimPattern: "In human mode the finding's message is also written bare to stderr.",
		testName:     "TestDocClaim_Doctor_FoldErrorEchoedBareToStderr",
	},
	{
		name:         "spec-version-check-repo-tracker-not-checked-out",
		docFile:      "docs/spec.md",
		claimPattern: "it exits 3 with `tracker_not_checked_out` rather than `no_repo`",
		testName:     "TestDocClaim_VersionCheckRepo_UnclonedTrackerSaysNotCheckedOut",
	},
	// act-1d3110: the nested-exclude doctor check. act's working files
	// showed as untracked in the nested .act repo; init now lists them in
	// .act/.git/info/exclude and doctor --fix repairs an older store.
	// Drift shape: the entry list in the spec row and the one in
	// internal/cli/nested_exclude.go part ways, or --fix stops driving
	// the check, and the test goes red.
	{
		name:         "spec-doctor-nested-exclude-entries",
		docFile:      "docs/spec.md",
		claimPattern: "`/.write.lock`, `/.slow-writes`, `/.pending-pushes`, `/.sync-log`, `/.compact.lock`, `/index.db`, `/index.db-wal`, `/index.db-shm`, `/index.db-journal`, `/index.db.malformed-*`, `/.doctor-rebuild.db*`, `/fold-checkpoint.json`, `/fold-checkpoint.json.tmp`, `/.bootstrap-meta.json`, `/.sync-log.tmp`, `/.tmp-*`, `/config-*.json.tmp`",
		testName:     "TestDocClaim_Doctor_NestedExclude",
	},
	{
		name:         "spec-doctor-nested-exclude-finding",
		docFile:      "docs/spec.md",
		claimPattern: "Any missing lines are reported as one warn-severity finding that names them. `act init` writes these lines",
		testName:     "TestDocClaim_Doctor_NestedExclude",
	},
	{
		name:         "spec-doctor-nested-exclude-fix",
		docFile:      "docs/spec.md",
		claimPattern: "Appends the missing lines; existing content is kept and a second `--fix` changes nothing.",
		testName:     "TestDocClaim_Doctor_NestedExclude",
	},
	{
		name:         "spec-doctor-fix-flag-drives-nested-exclude",
		docFile:      "docs/spec.md",
		claimPattern: "Drives `index-divergence`, `index-schema` and `nested-exclude` only.",
		testName:     "TestDocClaim_Doctor_NestedExclude",
	},
	{
		name:         "doctor-fix-flag-help-names-nested-exclude",
		docFile:      "cmd/act/doctor.go",
		claimPattern: "auto-remediate index-divergence/index-schema/nested-exclude findings",
		testName:     "TestDocClaim_Doctor_NestedExclude",
	},
	// act-f7a994: sixth stale pass over docs/spec.md. The universal exit
	// list gave 4 one meaning; `--check` was called repeatable; four doctor
	// checks had no table row and the test plan named eight of fifteen; the
	// show/log synopses left out most flags; `act help errors` listed three
	// codes nothing emits; the `--check-repo` bullet did not say where it
	// looks; the remote_status intro said every field defaults to zero.
	// Drift shape: a check or a flag is added without its doc line, or a
	// dropped code gains an emitter, and the test named here goes red.
	{
		name:         "spec-universal-exit-4-three-uses",
		docFile:      "docs/spec.md",
		claimPattern: "Three commands exit 4, each for its own condition",
		testName:     "TestDocClaim_Spec_ExitCodeFourHasThreeUses",
	},
	{
		name:         "spec-universal-exit-4-no-other-command",
		docFile:      "docs/spec.md",
		claimPattern: "No other command exits 4",
		testName:     "TestDocClaim_Spec_ExitCodeFourHasThreeUses",
	},
	{
		name:         "spec-universal-exit-4-bootstrap-timeout",
		docFile:      "docs/spec.md",
		claimPattern: "`act state import --from-remote` when the clone runs past its time budget (`bootstrap_timeout`)",
		testName:     "TestDocClaim_BootstrapFromRemote_TimeoutEnvelope",
	},
	{
		name:         "spec-doctor-check-table-lists-all-checks",
		docFile:      "docs/spec.md",
		claimPattern: "| `phase2-reconciliation` |",
		testName:     "TestDocClaim_Spec_DoctorCheckTableListsExactlyAllChecks",
	},
	{
		name:         "spec-test-plan-doctor-check-list",
		docFile:      "docs/spec.md",
		claimPattern: "and `fold-error`, which has no pass of its own):",
		testName:     "TestDocClaim_Spec_TestPlanDoctorCheckListMatchesAllChecks",
	},
	{
		name:         "spec-doctor-check-flag-single-value",
		docFile:      "docs/spec.md",
		claimPattern: "The flag is not repeatable: passed twice, the last value is the one that runs.",
		testName:     "TestDocClaim_Doctor_CheckFlagTakesOneName",
	},
	{
		name:         "spec-doctor-check-flag-names-are-table-rows",
		docFile:      "docs/spec.md",
		claimPattern: "The names accepted are the rows of the table below; any other name exits 2.",
		testName:     "TestDocClaim_Doctor_CheckFlagTakesOneName",
	},
	{
		name:         "spec-doctor-row-stale-git-lock",
		docFile:      "docs/spec.md",
		claimPattern: "doctor never removes the lock, because it cannot tell a stale lock from one a running git process holds",
		testName:     "TestDocClaim_Spec_DoctorRowsGitignorePhase2StatusLock",
	},
	{
		name:         "spec-doctor-row-status-vs-oplog",
		docFile:      "docs/spec.md",
		claimPattern: "Error findings exit 1; the warn finding exits 0 unless `--strict`.",
		testName:     "TestDocClaim_Spec_DoctorRowsGitignorePhase2StatusLock",
	},
	{
		name:         "spec-doctor-row-gitignore-effective",
		docFile:      "docs/spec.md",
		claimPattern: "Read-only: `--fix` does not edit `.gitignore`. A finding exits 1.",
		testName:     "TestDocClaim_Spec_DoctorRowsGitignorePhase2StatusLock",
	},
	{
		name:         "spec-doctor-row-phase2-umbrella",
		docFile:      "docs/spec.md",
		claimPattern: "Findings carry those five names, never `phase2-reconciliation`, and `--check` takes only the umbrella name",
		testName:     "TestDocClaim_Spec_DoctorRowsGitignorePhase2StatusLock",
	},
	{
		name:         "spec-show-synopsis-names-every-flag",
		docFile:      "docs/spec.md",
		claimPattern: "**Synopsis:** `act show <id> [--json] [--include-ops] [--commit-marker] [--full] [--no-fetch]`",
		testName:     "TestDocClaim_Spec_ShowLogSynopsesNameEveryFlag",
	},
	{
		name:         "spec-log-synopsis-names-every-flag",
		docFile:      "docs/spec.md",
		claimPattern: "**Synopsis:** `act log [<id>] [--since D] [--by-issue ID] [--type T[,T...]] [--summary] [--no-fetch] [--json]`",
		testName:     "TestDocClaim_Spec_ShowLogSynopsesNameEveryFlag",
	},
	{
		name:         "spec-show-commit-marker-flag",
		docFile:      "docs/spec.md",
		claimPattern: "Print only the commit-message trailer for the issue",
		testName:     "TestDocClaim_Show_CommitMarkerFullAndNoFetchFlags",
	},
	{
		name:         "spec-show-full-flag",
		docFile:      "docs/spec.md",
		claimPattern: "Without it each is cut at 5 lines or 400 characters, whichever comes first",
		testName:     "TestDocClaim_Show_CommitMarkerFullAndNoFetchFlags",
	},
	{
		name:         "spec-log-id-optional-bare-log-exits-2",
		docFile:      "docs/spec.md",
		claimPattern: "a bare `act log` exits 2",
		testName:     "TestDocClaim_Log_ScopeAndFilterFlags",
	},
	{
		name:         "spec-log-summary-flag",
		docFile:      "docs/spec.md",
		claimPattern: "`--summary` adds a short note of what the op did",
		testName:     "TestDocClaim_Log_ScopeAndFilterFlags",
	},
	{
		name:         "log-summary-flag-help",
		docFile:      "cmd/act/main.go",
		claimPattern: "human format only: add a short note of what each op did",
		testName:     "TestDocClaim_Log_ScopeAndFilterFlags",
	},
	{
		name:         "help-errors-not-in-git-row-mcp-reads",
		docFile:      "cmd/act/help.go",
		claimPattern: "act_log and act_search MCP tools answer when the",
		testName:     "TestDocClaim_HelpErrors_NotInGitAlsoFromMCPReadsWithNoActDir",
	},
	{
		name:         "help-errors-no-row-for-codes-nothing-emits",
		docFile:      "cmd/act/help.go",
		claimPattern: "    hook_failed             a .act/hooks/<event> script exited non-zero or timed out\n    import_invalid_jsonl    'act import' could not parse a line",
		testName:     "TestDocClaim_HelpErrors_NoRowForCodesNothingEmits",
	},
	{
		name:         "spec-version-check-repo-bullet-where-it-looks",
		docFile:      "docs/spec.md",
		claimPattern: "the repo root is found from any subdirectory, so the flag need not be run from the root",
		testName:     "TestDocClaim_VersionCheckRepo_ResolvesRepoRootFromSubdirectory",
	},
	{
		name:         "spec-version-check-repo-bullet-exit-3",
		docFile:      "docs/spec.md",
		claimPattern: "Outside a git working tree, or in a repo with no `.act/`, it exits 3 (see the exit codes below).",
		testName:     "TestDocClaim_Spec_VersionCheckRepoBulletSaysWhereItLooks",
	},
	{
		name:         "spec-doctor-remote-status-defaults-when-phase2-did-not-run",
		docFile:      "docs/spec.md",
		claimPattern: "is `true` and the other four fields hold their zero values",
		testName:     "TestDocClaim_Doctor_RemoteStatusWhenPhase2DidNotRun",
	},
	{
		name:         "spec-doctor-remote-status-remote-reachable-default-true",
		docFile:      "docs/spec.md",
		claimPattern: "nothing was probed, so nothing is reported unreachable",
		testName:     "TestDocClaim_Doctor_RemoteReachableFalseOnlyWhenProbeFailed",
	},
	// act-76eaec: `act state import` writes the nested-exclude lines too.
	// A --from-remote clone started from git's stock info/exclude and a
	// copy inherited an older source's file, so the imported store listed
	// .write.lock and index.db as untracked after its first write. Drift
	// shape: an import mode stops calling EnsureNestedExclude, or a new
	// mode is added without it, and the test goes red.
	{
		name:         "spec-doctor-nested-exclude-state-import-writes-lines",
		docFile:      "docs/spec.md",
		claimPattern: "and so does `act state import` in every mode, so only a store made by an older act, or one cloned by hand with `git clone`, lacks them.",
		testName:     "TestDocClaim_StateImport_WritesNestedExclude",
	},
	// --- seventh spec pass (act-8a0322, act-870488): one contiguous block ---
	{
		name:         "spec-layout-tree-tracked-group",
		docFile:      "docs/spec.md",
		claimPattern: "Inside the nested repo, only the paths under \"tracked\" are committed.",
		testName:     "TestDocClaim_Spec_LayoutTreeMatchesTopLevelPaths",
	},
	{
		name:         "spec-layout-tree-excluded-by-info-exclude",
		docFile:      "docs/spec.md",
		claimPattern: "are kept out of the nested repo by `.act/.git/info/exclude`, not by a `.gitignore`",
		testName:     "TestDocClaim_Spec_LayoutTreeMatchesTopLevelPaths",
	},
	{
		name:         "spec-layout-tree-no-dot-lock",
		docFile:      "docs/spec.md",
		claimPattern: "Nothing writes a `.act/.lock`.",
		testName:     "TestDocClaim_Spec_LayoutTreeMatchesTopLevelPaths",
	},
	{
		name:         "spec-synopsis-doctor-names-every-flag",
		docFile:      "docs/spec.md",
		claimPattern: "**Synopsis:** `act doctor [--check NAME] [--fix] [--fix-index] [--strict] [--no-fetch] [--json]`",
		testName:     "TestDocClaim_Spec_EverySynopsisNamesEveryFlag",
	},
	{
		name:         "spec-synopsis-list-names-no-fetch",
		docFile:      "docs/spec.md",
		claimPattern: "**Synopsis:** `act list [--status X] [--all] [--assignee Y] [--type T] [--json] [--limit N] [--sort field] [--no-fetch]`",
		testName:     "TestDocClaim_Spec_EverySynopsisNamesEveryFlag",
	},
	{
		name:         "spec-synopsis-ready-names-every-flag",
		docFile:      "docs/spec.md",
		claimPattern: "**Synopsis:** `act ready [--under <id>] [--json] [--limit N] [--all-machines] [--mine] [--as NODE] [--fresh] [--no-cache] [--no-fetch]`",
		testName:     "TestDocClaim_Spec_EverySynopsisNamesEveryFlag",
	},
	{
		name:         "spec-synopsis-search-names-no-fetch",
		docFile:      "docs/spec.md",
		claimPattern: "**Synopsis:** `act search <query> [--in title|desc|all] [--status X] [--limit N] [--no-fetch] [--json]`",
		testName:     "TestDocClaim_Spec_EverySynopsisNamesEveryFlag",
	},
	{
		name:         "spec-synopsis-close-names-every-flag",
		docFile:      "docs/spec.md",
		claimPattern: "**Synopsis:** `act close <id> [--reason TEXT] [--force] [--no-code] [--no-doctor] [--json] [--no-commit] [--push] [--isolated] [--offline] [--branch NAME]`",
		testName:     "TestDocClaim_Spec_EverySynopsisNamesEveryFlag",
	},
	{
		name:         "spec-synopsis-init-names-every-flag",
		docFile:      "docs/spec.md",
		claimPattern: "**Synopsis:** `act init [--force] [--force-new] [--contributing] [--commit-host] [--json]`",
		testName:     "TestDocClaim_Spec_EverySynopsisNamesEveryFlag",
	},
	{
		name:         "spec-write-flags-are-not-universal",
		docFile:      "docs/spec.md",
		claimPattern: "except `--verify`, which only `update`, `reopen` and `delete` take. `act init` takes none of them.",
		testName:     "TestDocClaim_Spec_WriteFlagsAreNotUniversal",
	},
	{
		name:         "spec-write-flag-branch",
		docFile:      "docs/spec.md",
		claimPattern: "The branch is created if it does not exist and is left checked out.",
		testName:     "TestDocClaim_Spec_WriteFlagsAreNotUniversal",
	},
	{
		name:         "spec-update-wait-timeout-is-a-duration",
		docFile:      "docs/spec.md",
		claimPattern: "- `--wait-timeout D` (Go duration such as `90s`, default `30s`). Bound on `--wait`. A bare number such as `60` is not a duration and is exit 2.",
		testName:     "TestDocClaim_Spec_WriteFlagsAreNotUniversal",
	},
	{
		name:         "spec-no-fetch-bullet-on-list-ready-search",
		docFile:      "docs/spec.md",
		claimPattern: "`--json` output then carries `refresh.reason: \"no_fetch\"`. `ACT_NO_FETCH=1` does the same.",
		testName:     "TestDocClaim_Spec_NoFetchOnListReadySearch",
	},
	{
		name:         "spec-doctor-strict-bullet",
		docFile:      "docs/spec.md",
		claimPattern: "- `--strict` (bool). Promote every warn-severity finding to error severity, so a run whose only findings are warnings exits 1 instead of 0.",
		testName:     "TestDocClaim_Spec_DoctorStrictAndNoFetchBullets",
	},
	{
		name:         "spec-doctor-no-fetch-bullet",
		docFile:      "docs/spec.md",
		claimPattern: "Not the read-path flag of the same name: `ACT_NO_FETCH=1` does not set it.",
		testName:     "TestDocClaim_Spec_DoctorStrictAndNoFetchBullets",
	},
	{
		name:         "spec-doctor-json-three-top-level-keys",
		docFile:      "docs/spec.md",
		claimPattern: "The object has exactly three top-level keys: `findings`, `count` and `remote_status`.",
		testName:     "TestDocClaim_Spec_DoctorJSONExampleMatchesBinary",
	},
	{
		name:         "spec-error-table-exit-column",
		docFile:      "docs/spec.md",
		claimPattern: "The `exit` column is the process exit code the binary returns with that error",
		testName:     "TestDocClaim_Spec_ErrorTableExitCodesMatchBinary",
	},
	{
		name:         "spec-error-table-not-in-git-exits-3",
		docFile:      "docs/spec.md",
		claimPattern: "| `not_in_git`               | 3    |",
		testName:     "TestDocClaim_Spec_ErrorTableExitCodesMatchBinary",
	},
	{
		name:         "help-errors-every-row-has-an-emitter",
		docFile:      "cmd/act/help.go",
		claimPattern: "    bad_flag                argparse rejected a flag/value\n    cycle                   legacy alias used by 'dep add'",
		testName:     "TestDocClaim_HelpErrors_EveryRowHasAnEmitter",
	},
	{
		name:         "spec-hooks-contract-hook-failed-exits-1",
		docFile:      "docs/spec.md",
		claimPattern: "No commit is created. Caller exits with code 1.",
		testName:     "TestDocClaim_Spec_ErrorTableExitCodesMatchBinary",
	},
	{
		name:         "spec-ambiguous-prefix-envelope",
		docFile:      "docs/spec.md",
		claimPattern: "Nothing emits the older `ambiguous_id` code.",
		testName:     "TestDocClaim_Spec_AmbiguousPrefixEnvelope",
	},
	{
		name:         "spec-flag-parse-error-json-envelope",
		docFile:      "docs/spec.md",
		claimPattern: "When `--json` (or `-json`) appears anywhere in the arguments, before or after the rejected flag, the envelope goes to stdout and the parser writes nothing to stderr",
		testName:     "TestDocClaim_FlagParseError_JSONEnvelope",
	},
	{
		name:         "spec-flag-parse-error-missing-value-and-wrong-type",
		docFile:      "docs/spec.md",
		claimPattern: "an unknown flag, a flag missing its value, a value of the wrong type for the flag — is `bad_flag` with exit 2",
		testName:     "TestDocClaim_FlagParseError_OtherParseFailures",
	},
	{
		name:         "spec-flag-parse-error-help-is-not-an-error",
		docFile:      "docs/spec.md",
		claimPattern: "`-h` and `--help` are not errors: they print the flag list on stderr with or without `--json`, and no envelope.",
		testName:     "TestDocClaim_FlagParseError_HelpIsNotAnError",
	},
	{
		name:         "spec-flag-parse-error-json-equals-form",
		docFile:      "docs/spec.md",
		claimPattern: "`--json=true` (or any value the flag reads as true, such as `--json=1`) counts as `--json` here; `--json=false` counts as no `--json`, and when both are given the last one wins.",
		testName:     "TestDocClaim_FlagParseError_JSONEqualsForm",
	},
	{
		name:         "spec-flag-parse-error-flag-where-the-verb-belongs",
		docFile:      "docs/spec.md",
		claimPattern: "A flag where the verb of `act state` or `act dep` belongs (`act state --json --nonsense`, `act dep --json`) follows the same rule: `bad_flag` with exit 2, the envelope on stdout under `--json` with the command's verb list as its `message`, the verb list on stderr without it.",
		testName:     "TestDocClaim_FlagParseError_GroupCommands",
	},
	// act-0c1c31: eighth stale pass over docs/spec.md. One contiguous
	// block; the tests are in cmd/act/spec_pass8_docclaim_test.go and run
	// the real binary. Drift shape for each: the quoted sentence stops
	// being what the binary does and the named test goes red.
	{
		name:         "spec-list-sort-keys-prefix-and-suffix",
		docFile:      "docs/spec.md",
		claimPattern: "A bare key sorts ascending; a `-` prefix makes it descending (`-created_at`). The suffix form does the same thing: `created_at:desc` is `-created_at`, and `:asc` (or a `+` prefix) is the bare key.",
		testName:     "TestDocClaim_Spec_ListSortSyntax",
	},
	{
		name:         "spec-list-sort-default-and-id-tiebreak",
		docFile:      "docs/spec.md",
		claimPattern: "Default: `priority,-created_at`. `id` ascending is added as the last key whenever the keys do not name `id`. An unknown field, an unknown direction suffix or an empty key is exit 2.",
		testName:     "TestDocClaim_Spec_ListSortSyntax",
	},
	{
		name:         "spec-init-existing-tracker-exits-1",
		docFile:      "docs/spec.md",
		claimPattern: "1 if `.act/config.json` already exists and `--force` was not given (`act_already_initialized`)",
		testName:     "TestDocClaim_Spec_InitExitCodesAndOutputModes",
	},
	{
		name:         "spec-init-tracker-remote-exits-3-unless-force-new",
		docFile:      "docs/spec.md",
		claimPattern: "or when a configured tracker remote already holds this repo's tracker and `--force-new` was not given (`tracker_not_checked_out`)",
		testName:     "TestDocClaim_Spec_InitExitCodesAndOutputModes",
	},
	{
		name:         "spec-init-json-only-with-flag",
		docFile:      "docs/spec.md",
		claimPattern: "**JSON output** (only with `--json`; without it `act init` prints human-readable lines, the first of them `Initialized .act/ at <path> with node_id <id>`):",
		testName:     "TestDocClaim_Spec_InitExitCodesAndOutputModes",
	},
	{
		name:         "spec-init-json-example-keys",
		docFile:      "docs/spec.md",
		claimPattern: "The first seven keys are always present.",
		testName:     "TestDocClaim_Spec_InitListSearchJSONExamplesMatchBinary",
	},
	{
		name:         "spec-init-json-no-writer-or-schema-version",
		docFile:      "docs/spec.md",
		claimPattern: "There is no `writer_version` or `schema_version` key.",
		testName:     "TestDocClaim_Spec_InitListSearchJSONExamplesMatchBinary",
	},
	{
		name:         "spec-list-json-example-keys",
		docFile:      "docs/spec.md",
		claimPattern: "The object has five top-level keys: `issues`, `count`, `total`, `truncated` and `refresh`. There is no `ok` key.",
		testName:     "TestDocClaim_Spec_InitListSearchJSONExamplesMatchBinary",
	},
	{
		name:         "spec-list-json-row-keys",
		docFile:      "docs/spec.md",
		claimPattern: "Each row always has `id`, `title`, `status`, `priority` and `type`; `assignee`, `created_at` and `claimed_at` are omitted when empty",
		testName:     "TestDocClaim_Spec_InitListSearchJSONExamplesMatchBinary",
	},
	{
		name:         "spec-search-json-example-keys",
		docFile:      "docs/spec.md",
		claimPattern: "The object has three top-level keys: `matches`, `count` and `refresh`. There is no `ok` key and no `results` key. `matches` is an empty array, not `null`, when nothing matched.",
		testName:     "TestDocClaim_Spec_InitListSearchJSONExamplesMatchBinary",
	},
	{
		name:         "spec-error-envelope-three-keys",
		docFile:      "docs/spec.md",
		claimPattern: "`error` is a string code, not an object, and the envelope has those three keys and no others",
		testName:     "TestDocClaim_Spec_ErrorEnvelopeHasNoKindKey",
	},
	{
		name:         "spec-doctor-findings-are-not-an-envelope",
		docFile:      "docs/spec.md",
		claimPattern: "`act doctor` is the exception: a run that exits non-zero because of its findings prints the findings document, not an envelope.",
		testName:     "TestDocClaim_Spec_ErrorEnvelopeHasNoKindKey",
	},
	{
		name:         "spec-dep-add-cycle-envelope",
		docFile:      "docs/spec.md",
		claimPattern: "1 on cycle (`{\"error\":\"cycle\",\"message\":\"act dep add: cycle detected: <a> -> <b> -> <a>\",\"details\":{\"path\":[\"<a>\",\"<b>\",\"<a>\"]}}`)",
		testName:     "TestDocClaim_Spec_ErrorEnvelopeHasNoKindKey",
	},
	{
		name:         "spec-mcp-error-is-iserror-result-with-envelope",
		docFile:      "docs/spec.md",
		claimPattern: "An error surfaces as a tool result with `isError: true` whose text is the error envelope",
		testName:     "TestDocClaim_Spec_ErrorEnvelopeHasNoKindKey",
	},
	{
		name:         "spec-op-payloads-migrate-transform-id",
		docFile:      "docs/spec.md",
		claimPattern: "{ \"from_version\": 1, \"to_version\": 2, \"transform_id\": \"v1-to-v2\" }",
		testName:     "TestDocClaim_Spec_ErrorEnvelopeHasNoKindKey",
	},
	// The 22 flag bullets sp7NoBulletYet used to excuse; one tuple per
	// bullet, keyed on the bullet's opening words.
	{
		name:         "spec-init-flag-force-new",
		docFile:      "docs/spec.md",
		claimPattern: "- `--force-new` (bool, default false). Start a new tracker even though a configured tracker remote",
		testName:     "TestDocClaim_Spec_InitExitCodesAndOutputModes",
	},
	{
		name:         "spec-init-flag-contributing",
		docFile:      "docs/spec.md",
		claimPattern: "- `--contributing` (bool, default false). Append a short section about the `Act-Id:` commit trailer to the host repo's `CONTRIBUTING.md`, creating the file if there is none and keeping what it already holds.",
		testName:     "TestDocClaim_Spec_FlagBulletsInit",
	},
	{
		name:         "spec-init-flag-commit-host",
		docFile:      "docs/spec.md",
		claimPattern: "- `--commit-host` (bool, default false). Commit the host-side files this run wrote",
		testName:     "TestDocClaim_Spec_FlagBulletsInit",
	},
	{
		name:         "spec-init-flag-commit-host-nothing-to-commit",
		docFile:      "docs/spec.md",
		claimPattern: "Nothing is committed when the host repo has no commit yet, or when the run wrote no host file.",
		testName:     "TestDocClaim_Spec_FlagBulletsInit",
	},
	{
		name:         "spec-init-flag-json",
		docFile:      "docs/spec.md",
		claimPattern: "- `--json` (bool, default false). Print the result as one JSON object instead of the human-readable lines.",
		testName:     "TestDocClaim_Spec_InitExitCodesAndOutputModes",
	},
	{
		name:         "spec-create-flag-description",
		docFile:      "docs/spec.md",
		claimPattern: "- `--description \"text\"` (string, default \"\"). The issue's body. An empty string is the same as leaving the flag out",
		testName:     "TestDocClaim_Spec_FlagBulletsCreateDescription",
	},
	{
		name:         "spec-create-flag-description-file",
		docFile:      "docs/spec.md",
		claimPattern: "- `--description-file PATH` (string). Read the body from a UTF-8 file; `-` reads it from stdin. Mutually exclusive with `--description` (the pair is exit 2). A path that does not exist is exit 3 (`file_not_found`).",
		testName:     "TestDocClaim_Spec_FlagBulletsCreateDescription",
	},
	{
		name:         "spec-update-flag-ext-rm",
		docFile:      "docs/spec.md",
		claimPattern: "- `--ext-rm REF` (repeatable). Clears an external dep",
		testName:     "TestDocClaim_Spec_FlagBulletsExternalDeps",
	},
	{
		name:         "spec-update-flag-unclaim",
		docFile:      "docs/spec.md",
		claimPattern: "- `--unclaim` (bool). Releases a claim: an `in_progress` issue returns to `open` and its assignee is cleared.",
		testName:     "TestDocClaim_Spec_FlagBulletsUnclaim",
	},
	{
		name:         "spec-update-flag-force",
		docFile:      "docs/spec.md",
		claimPattern: "- `--force` (bool). With `--claim` only: claim an issue even though it has open external deps.",
		testName:     "TestDocClaim_Spec_FlagBulletsExternalDeps",
	},
	{
		name:         "spec-close-flag-force",
		docFile:      "docs/spec.md",
		claimPattern: "- `--force` (bool). Close an issue even though it has open external deps",
		testName:     "TestDocClaim_Spec_FlagBulletsExternalDeps",
	},
	{
		name:         "spec-close-flag-no-code",
		docFile:      "docs/spec.md",
		claimPattern: "the close op's payload carries `no_code: true` and `act show --json` then reports `closed_no_code: true`. `act doctor`'s `orphan-close` check does not report an issue closed this way.",
		testName:     "TestDocClaim_Spec_FlagBulletsCloseNoCodeNoDoctor",
	},
	{
		name:         "spec-close-flag-no-doctor",
		docFile:      "docs/spec.md",
		claimPattern: "- `--no-doctor` (bool). Skip the check `act close` runs after a committed close.",
		testName:     "TestDocClaim_Spec_FlagBulletsCloseNoCodeNoDoctor",
	},
	{
		name:         "spec-dep-add-flag-blocked-by",
		docFile:      "docs/spec.md",
		claimPattern: "`act dep add <a> --blocked-by <b>` says `<a>` is blocked by `<b>`, the same edge as `act dep add <a> <b>`.",
		testName:     "TestDocClaim_Spec_FlagBulletsDepAddDirectional",
	},
	{
		name:         "spec-dep-add-flag-blocks",
		docFile:      "docs/spec.md",
		claimPattern: "`act dep add <a> --blocks <b>` says `<a>` blocks `<b>`, the same edge as `act dep add <b> <a>`.",
		testName:     "TestDocClaim_Spec_FlagBulletsDepAddDirectional",
	},
	{
		name:         "spec-dep-add-flag-external",
		docFile:      "docs/spec.md",
		claimPattern: "While an issue carries one it is left out of `act ready`, and `act close` and `act update --claim` refuse it with `blocked_by_external_dep` unless given `--force`.",
		testName:     "TestDocClaim_Spec_FlagBulletsExternalDeps",
	},
	{
		name:         "spec-ready-flag-mine",
		docFile:      "docs/spec.md",
		claimPattern: "- `--mine` (bool). Only the ready issues whose assignee is this checkout's `node_id`, read from `.act/config.json`.",
		testName:     "TestDocClaim_Spec_FlagBulletsReadyMineAsFresh",
	},
	{
		name:         "spec-ready-flag-as",
		docFile:      "docs/spec.md",
		claimPattern: "- `--as NODE` (string). With `--mine`: match the assignee `NODE` instead of this checkout's `node_id`. Without `--mine` it is exit 2.",
		testName:     "TestDocClaim_Spec_FlagBulletsReadyMineAsFresh",
	},
	{
		name:         "spec-ready-flag-fresh",
		docFile:      "docs/spec.md",
		claimPattern: "`--json` output then carries `refresh.reason: \"fresh_flag\"`. Mutually exclusive with `--no-fetch` (the pair is exit 2).",
		testName:     "TestDocClaim_Spec_FlagBulletsReadyMineAsFresh",
	},
	{
		name:         "spec-ready-flag-no-cache",
		docFile:      "docs/spec.md",
		claimPattern: "- `--no-cache` (bool). Another spelling of `--fresh`; the two do the same thing.",
		testName:     "TestDocClaim_Spec_FlagBulletsReadyMineAsFresh",
	},
	{
		name:         "spec-next-flag-all-machines",
		docFile:      "docs/spec.md",
		claimPattern: "- `--all-machines` (bool). With `--peek` only: consider issues pinned to another machine too",
		testName:     "TestDocClaim_Spec_FlagBulletsNextAllMachines",
	},
	{
		name:         "spec-migrate-flag-from",
		docFile:      "docs/spec.md",
		claimPattern: "- `--from N` (int, required). The source `op_version`: ops at this version are the ones migrated. Must be greater than 0.",
		testName:     "TestDocClaim_Spec_FlagBulletsMigrateFromTo",
	},
	{
		name:         "spec-migrate-flag-to",
		docFile:      "docs/spec.md",
		claimPattern: "- `--to M` (int, required). The target `op_version`. Must be greater than `--from`.",
		testName:     "TestDocClaim_Spec_FlagBulletsMigrateFromTo",
	},
	// The error table and `act help errors`: every emitted code, no other.
	{
		name:         "spec-error-table-lists-exactly-the-emitted-codes",
		docFile:      "docs/spec.md",
		claimPattern: "The table lists every code the CLI emits — every code that some non-test source line under `cmd/` or `internal/` puts in an envelope's `error` field — and no other.",
		testName:     "TestDocClaim_ErrorCodes_HelpAndSpecListExactlyTheEmittedSet",
	},
	{
		name:         "spec-error-table-mcp-only-codes",
		docFile:      "docs/spec.md",
		claimPattern: "**Codes only the MCP server emits.** A tool call has no exit code; these arrive as the `error` of an `isError` tool result.",
		testName:     "TestDocClaim_ErrorCodes_HelpAndSpecListExactlyTheEmittedSet",
	},
	{
		name:         "help-errors-lists-exactly-the-emitted-codes",
		docFile:      "cmd/act/help.go",
		claimPattern: "These rows are every code the source emits and no other: a constant\n  that nothing emits has no row.",
		testName:     "TestDocClaim_ErrorCodes_HelpAndSpecListExactlyTheEmittedSet",
	},
	{
		name:         "help-errors-mcp-only-block",
		docFile:      "cmd/act/help.go",
		claimPattern: "MCP-only codes (the \"error\" of an isError tool result; never a CLI exit):",
		testName:     "TestDocClaim_ErrorCodes_HelpAndSpecListExactlyTheEmittedSet",
	},
	{
		name:         "spec-error-table-details-always-present-two-exceptions",
		docFile:      "docs/spec.md",
		claimPattern: "Two commands print failures with `error` and `message` alone:",
		testName:     "TestDocClaim_Spec_ErrorTableRowsMatchBinaryAndSource",
	},
	{
		name:         "spec-error-table-two-exit-codes-command-decides",
		docFile:      "docs/spec.md",
		claimPattern: "Where a row lists two exit codes the command decides which: an index that cannot be opened is exit 1 from `act list` and exit 3 from `act search`.",
		testName:     "TestDocClaim_Spec_ErrorTableRowsMatchBinaryAndSource",
	},
	{
		name:         "spec-error-table-state-import-message-prefix",
		docFile:      "docs/spec.md",
		claimPattern: "| `remote_unreachable`       | 3    | `act state import: clone <url>: <reason>`",
		testName:     "TestDocClaim_Spec_ErrorTableRowsMatchBinaryAndSource",
	},
	{
		name:         "spec-error-table-init-existing-tracker-row",
		docFile:      "docs/spec.md",
		claimPattern: "| `act_already_initialized`  | 1    | `act init: <path> already exists; pass --force to reinitialize` | none |",
		testName:     "TestDocClaim_Spec_ErrorTableRowsMatchBinaryAndSource",
	},
	// ---- act-74cf82: spec.md ninth stale pass. ----
	{
		name:         "spec-pass9-error-table-id-ambiguous-row",
		docFile:      "docs/spec.md",
		claimPattern: "| `id_ambiguous`             | 2    | by command, e.g. `act show: prefix \"<prefix>\" matches <N> issues` | `prefix`, `candidates` |",
		testName:     "TestDocClaim_Spec_DepAddAmbiguousPrefixIsExit2",
	},
	{
		name:         "spec-pass9-depadd-comment-ambiguous-is-exit-2",
		docFile:      "internal/cli/depadd.go",
		claimPattern: "an ambiguous prefix is exit 2 with id_ambiguous",
		testName:     "TestDocClaim_Spec_DepAddAmbiguousPrefixIsExit2",
	},
	{
		name:         "spec-pass9-init-creates-these-files",
		docFile:      "docs/spec.md",
		claimPattern: "the placeholder file `ops/.gitkeep`, the inert hook template `hooks/close.sample`, and `config.json`",
		testName:     "TestDocClaim_Spec_InitWritesAndCommits",
	},
	{
		name:         "spec-pass9-init-config-four-keys",
		docFile:      "docs/spec.md",
		claimPattern: "`config.json` holds exactly four keys:",
		testName:     "TestDocClaim_Spec_InitWritesAndCommits",
	},
	{
		name:         "spec-pass9-init-no-index-db",
		docFile:      "docs/spec.md",
		claimPattern: "`act init` does not create `index.db`: the first command that reads the index does",
		testName:     "TestDocClaim_Spec_InitWritesAndCommits",
	},
	{
		name:         "spec-pass9-init-nested-first-commit",
		docFile:      "docs/spec.md",
		claimPattern: "commits what it wrote there as that repo's first commit, with the subject `act init: nested act state bootstrap`",
		testName:     "TestDocClaim_Spec_InitWritesAndCommits",
	},
	{
		name:         "spec-pass9-init-no-host-commit-unless-asked",
		docFile:      "docs/spec.md",
		claimPattern: "It makes no commit in the host repo unless `--commit-host` is given.",
		testName:     "TestDocClaim_Spec_InitWritesAndCommits",
	},
	{
		name:         "spec-pass9-init-side-effects-one-nested-commit",
		docFile:      "docs/spec.md",
		claimPattern: "makes one commit in the nested `.act/` repo — none on a `--force` re-run of a tracker whose nested repo already has a commit",
		testName:     "TestDocClaim_Spec_InitWritesAndCommits",
	},
	{
		name:         "spec-pass9-init-force-always-rewrites-config",
		docFile:      "docs/spec.md",
		claimPattern: "and always rewrites `config.json`: `node_id` is worked out again from the machine id and `user.email` as they are now, and `created_at` becomes the time of the re-run",
		testName:     "TestDocClaim_Spec_InitForceRewritesConfig",
	},
	{
		name:         "spec-pass9-init-force-leaves-config-uncommitted",
		docFile:      "docs/spec.md",
		claimPattern: "so the rewritten `config.json` is left there as an uncommitted change. Op files and snapshots are never changed or removed.",
		testName:     "TestDocClaim_Spec_InitForceRewritesConfig",
	},
	{
		name:         "spec-pass9-init-force-edge-case",
		docFile:      "docs/spec.md",
		claimPattern: "`--force` on a `.act/` with existing ops leaves the op files untouched; it rewrites `config.json` and puts back what is missing.",
		testName:     "TestDocClaim_Spec_InitForceRewritesConfig",
	},
	{
		name:         "spec-pass9-create-json-seven-keys",
		docFile:      "docs/spec.md",
		claimPattern: "Those seven keys are always present. `prefix` is the id cut to `act-` plus six hex characters when the id is longer",
		testName:     "TestDocClaim_Spec_CreateShowUpdateCloseVersionJSONExamplesMatchBinary",
	},
	{
		name:         "spec-pass9-create-json-warnings-key",
		docFile:      "docs/spec.md",
		claimPattern: "An eighth key, `warnings` (an array of strings), appears only when there is a warning: `[\"parent_closed\"]` when `--parent` names a closed issue.",
		testName:     "TestDocClaim_Spec_CreateShowUpdateCloseVersionJSONExamplesMatchBinary",
	},
	{
		name:         "spec-pass9-show-json-flat-eleven-keys",
		docFile:      "docs/spec.md",
		claimPattern: "there is no `ok` key, no `issue` wrapper and no `ops_count`. An issue created with a title and nothing else prints eleven keys:",
		testName:     "TestDocClaim_Spec_CreateShowUpdateCloseVersionJSONExamplesMatchBinary",
	},
	{
		name:         "spec-pass9-show-json-optional-keys",
		docFile:      "docs/spec.md",
		claimPattern: "`machine` appears on a pinned issue, `assignee` and `claimed_at` once the issue is claimed, `closed_at`, `closed_reason` and `closed_by_node` once it is closed, and `ops` (the op envelopes, oldest first) only with `--include-ops`.",
		testName:     "TestDocClaim_Spec_CreateShowUpdateCloseVersionJSONExamplesMatchBinary",
	},
	{
		name:         "spec-pass9-show-json-tombstoned",
		docFile:      "docs/spec.md",
		claimPattern: "A tombstoned issue prints `{\"id\":\"<id>\",\"tombstoned\":true}` and nothing else.",
		testName:     "TestDocClaim_Spec_CreateShowUpdateCloseVersionJSONExamplesMatchBinary",
	},
	{
		name:         "spec-pass9-update-json-plain-three-keys",
		docFile:      "docs/spec.md",
		claimPattern: "**JSON output (an update without `--claim`):**\n```json\n{\"id\":\"act-ecf98c\",\"ops_written\":2,\"committed\":true}\n```\nThose three keys are always present, and there is no `ok` key.",
		testName:     "TestDocClaim_Spec_CreateShowUpdateCloseVersionJSONExamplesMatchBinary",
	},
	{
		name:         "spec-pass9-update-json-claim-writes-no-field-op",
		docFile:      "docs/spec.md",
		claimPattern: "Here `ops_written` is a list of op types, not a count, and it is always `[\"claim\"]`:",
		testName:     "TestDocClaim_Spec_CreateShowUpdateCloseVersionJSONExamplesMatchBinary",
	},
	{
		name:         "spec-pass9-close-json-five-keys",
		docFile:      "docs/spec.md",
		claimPattern: "A close that writes its op prints those five keys.",
		testName:     "TestDocClaim_Spec_CreateShowUpdateCloseVersionJSONExamplesMatchBinary",
	},
	{
		name:         "spec-pass9-close-json-already-closed",
		docFile:      "docs/spec.md",
		claimPattern: "Closing an issue that is already closed prints `{\"id\":\"<id>\",\"already_closed\":true}` and nothing else.",
		testName:     "TestDocClaim_Spec_CreateShowUpdateCloseVersionJSONExamplesMatchBinary",
	},
	{
		name:         "spec-pass9-version-json-four-keys-indented",
		docFile:      "docs/spec.md",
		claimPattern: "`act version --json` prints those four keys, indented over several lines as shown",
		testName:     "TestDocClaim_Spec_CreateShowUpdateCloseVersionJSONExamplesMatchBinary",
	},
	{
		name:         "spec-pass9-version-json-check-repo-fifth-key",
		docFile:      "docs/spec.md",
		claimPattern: "With `--check-repo` a fifth key, `max_op_version`, carries the highest `writer_version` found in the op files — `\"\"` when the tracker has no op yet. There is no `ok`, `schema_version` or `repo_max_writer_version` key.",
		testName:     "TestDocClaim_Spec_CreateShowUpdateCloseVersionJSONExamplesMatchBinary",
	},
	{
		name:         "spec-pass9-envelope-error-and-message-always",
		docFile:      "docs/spec.md",
		claimPattern: "`error` and `message` are present in every envelope. `details` is present in every envelope but the ones named here, and is `{}` when the error carries none.",
		testName:     "TestDocClaim_Spec_EnvelopeKeysAlwaysPresent",
	},
	{
		name:         "spec-pass9-envelope-which-failures-omit-details",
		docFile:      "docs/spec.md",
		claimPattern: "`act migrate` leaves `details` out of every failure it reports (`not_in_git`, `bad_input`, `migration_not_found`, and a read, transform or write failure), and `act version --check-repo` leaves it out of `not_in_git`, `no_repo`, `stat_failed` and `walk_failed`.",
		testName:     "TestDocClaim_Spec_EnvelopeKeysAlwaysPresent",
	},
	{
		name:         "spec-pass9-envelope-version-failures-with-details",
		docFile:      "docs/spec.md",
		claimPattern: "The other two failures of `act version --check-repo`, `version_skew` and `tracker_not_checked_out`, carry `details` like any other envelope.",
		testName:     "TestDocClaim_Spec_EnvelopeKeysAlwaysPresent",
	},
	{
		name:         "spec-pass9-envelope-universal-pointer",
		docFile:      "docs/spec.md",
		claimPattern: "the failures of `act migrate` and `act version --check-repo` that leave `details` out, are listed under \"1. Error handling\".",
		testName:     "TestDocClaim_Spec_EnvelopeKeysAlwaysPresent",
	},
	{
		name:         "spec-pass9-migrate-output-two-keys-and-flag-errors",
		docFile:      "docs/spec.md",
		claimPattern: "those two keys and no `details`. The one case that prints no JSON is a flag error (an unknown flag, or a `--from`/`--to` value that is not an integer): exit 2 with the flag usage text on stderr and nothing on stdout.",
		testName:     "TestDocClaim_Spec_EnvelopeKeysAlwaysPresent",
	},
	// act-fcf9ca: --claim refuses --machine like every other field flag.
	{
		name:         "spec-update-claim-refuses-every-field-flag",
		docFile:      "docs/spec.md",
		claimPattern: "No field flag is let through: the refused call names the flag, claims nothing and changes nothing, so pin an issue with one `act update` and claim it with another.",
		testName:     "TestDocClaim_Spec_UpdateClaimRefusesEveryFieldFlag",
	},
	// act-8c1c05: create's pushed reports the push, not the --push flag.
	{
		name:         "spec-create-json-pushed-reports-the-push",
		docFile:      "docs/spec.md",
		claimPattern: "`pushed` says what happened to the op commit, not which flag was passed: it is true only when this call pushed the commit to `origin`. In a tracker with `origin` that is every create, with or without `--push`, except under `--no-commit` or `--offline` or when the push was deferred; in a tracker with no `origin` remote — the one in the example — it is always false.",
		testName:     "TestDocClaim_Spec_CreatePushedReportsWhatHappened",
	},
	{
		name:         "spec-create-json-push-no-remote-warning",
		docFile:      "docs/spec.md",
		claimPattern: "`\"push_no_remote\"` is the second of three warnings: `--push` was given and the tracker has no `origin` remote, so the issue is created and committed, exit 0, and nothing was pushed. Without `--json` it is a line on stderr.",
		testName:     "TestDocClaim_Spec_CreatePushedReportsWhatHappened",
	},
	// act-4d7596: create has three warnings, and the doctor section says what
	// each command does when the index is already malformed.
	{
		name:         "spec-create-three-warnings",
		docFile:      "docs/spec.md",
		claimPattern: "The third, `\"index_update_failed\"`, is described next.",
		testName:     "TestDocClaim_Spec_CreateHasThreeWarnings",
	},
	{
		name:         "spec-index-malformed-reads-first-commands-write-nothing",
		docFile:      "docs/spec.md",
		claimPattern: "the commands that read the index before they write anything — `act close`, `act dep add`, `act ready`, `act update --claim` — exit 1 with an error whose message carries SQLite's `database disk image is malformed`, and write no op.",
		testName:     "TestDocClaim_Spec_IndexMalformedBeforeTheCommandStarts",
	},
	{
		name:         "spec-index-malformed-create-and-field-update-write-first",
		docFile:      "docs/spec.md",
		claimPattern: "`act create`, and an `act update` that edits a field of an issue named by its full id, do not read the index first: the op is written and committed, and the index failure comes after. `act create` then exits 0 with `\"index_update_failed\"` in `warnings` (see `act create`).",
		testName:     "TestDocClaim_Spec_IndexMalformedCreateAndFieldUpdateWriteFirst",
	},
	// act-e3782c: an index failure after create's commit is a warning.
	{
		name:         "spec-create-index-update-failed-is-a-warning",
		docFile:      "docs/spec.md",
		claimPattern: "`\"index_update_failed\"` in `warnings` means the op was written and committed and `.act/index.db` could not then be updated. The issue exists, the exit code is 0 and `id` is the one to use: do not retry the create.",
		testName:     "TestDocClaim_CreateIndexUnopenable_ReportsTheCreatedIssue",
	},
	{
		name:         "spec-create-index-update-failed-reads-still-fail",
		docFile:      "docs/spec.md",
		claimPattern: "a read that cannot open the index fails in its own name (`index_open_failed`). Without `--json` the warning is a line on stderr, beside the usual `Created` line.",
		testName:     "TestDocClaim_CreateIndexUnopenable_HumanWarnsAndReadsStillFail",
	},
	{
		name:         "spec-create-exit-0-when-index-update-fails",
		docFile:      "docs/spec.md",
		claimPattern: "0, including a create whose push was deferred and a create whose index update failed after the commit;",
		testName:     "TestDocClaim_CreateIndexUnopenable_ReportsTheCreatedIssue",
	},
	{
		name:         "spec-error-table-index-update-failed-never-from-create",
		docFile:      "docs/spec.md",
		claimPattern: "Never from `act create`, which reports it as a warning and exits 0",
		testName:     "TestDocClaim_CreateIndexUnopenable_BlocksEdgeStillLands",
	},
	// act-f4fd34: update --push with no remote lands and warns; isolated
	// writes do not push.
	{
		name:         "spec-update-json-push-no-remote-warning",
		docFile:      "docs/spec.md",
		claimPattern: "A `warnings` key (an array of strings) appears only when there is a warning: `[\"push_no_remote\"]` when `--push` was given and the tracker has no `origin` remote — the update is written and committed, exit 0, and nothing was pushed. Without `--json` it is a line on stderr.",
		testName:     "TestDocClaim_Spec_UpdatePushNoRemoteWarns",
	},
	{
		name:         "spec-update-claim-push-no-remote-warning",
		docFile:      "docs/spec.md",
		claimPattern: "A won claim carries `warnings` on the same terms: `--claim --push` in a tracker with no `origin` remote exits 0 with the issue claimed and `\"warnings\":[\"push_no_remote\"]`, and without `--json` the warning is a line on stderr.",
		testName:     "TestDocClaim_Spec_UpdateClaimPushNoRemoteWarns",
	},
	{
		name:         "spec-isolated-writes-do-not-push",
		docFile:      "docs/spec.md",
		claimPattern: "With `origin` configured, an isolated `create`, `update`, `close`, `dep add`, `reopen` or `delete` commits locally, pushes nothing and queues nothing in `.act/.pending-pushes`; the next write without `--isolated` pushes the held commits along with its own.",
		testName:     "TestDocClaim_Spec_IsolatedWritesDoNotPush",
	},
	// act-7e8600: close, dep add, reopen and delete --push with no remote
	// warn as create and update do; close --isolated does not push.
	{
		name:         "spec-universal-push-no-remote-warning",
		docFile:      "docs/spec.md",
		claimPattern: "In a tracker with no `origin` remote there is nothing to push to: `create`, `update`, `close`, `dep add`, `reopen` and `delete` given `--push` still write and commit the op and exit 0, and the `--json` result carries `\"warnings\":[\"push_no_remote\"]`; without `--json` the warning is a line on stderr.",
		testName:     "TestDocClaim_Spec_PushNoRemoteWarnsOnEveryWrite",
	},
	{
		name:         "spec-isolated-close-does-not-push",
		docFile:      "docs/spec.md",
		claimPattern: "an isolated `create`, `update`, `close`, `dep add`, `reopen` or `delete` commits locally, pushes nothing and queues nothing in `.act/.pending-pushes`",
		testName:     "TestDocClaim_Spec_CloseIsolatedDoesNotPush",
	},
}

// TestDocSweep_AllClaimsHaveAssertingTests is the meta-test that drives
// the registry. It runs on every `go test ./...`; a fresh agent reading
// the failure message learns both the convention and which entry to
// fix.
func TestDocSweep_AllClaimsHaveAssertingTests(t *testing.T) {
	root := repoRootForDocClaim(t)
	testNames := collectTestNames(t, root)

	for _, c := range docClaimRegistry {
		t.Run(c.name, func(t *testing.T) {
			// 1. Test-name convention: must start with TestDocClaim_.
			if !strings.HasPrefix(c.testName, "TestDocClaim_") {
				t.Fatalf("registry entry %q: testName %q does not start with TestDocClaim_",
					c.name, c.testName)
			}

			// 2. Doc file contains the claim.
			docPath := filepath.Join(root, c.docFile)
			body, err := os.ReadFile(docPath)
			if err != nil {
				t.Fatalf("read %s: %v", c.docFile, err)
			}
			if !strings.Contains(string(body), c.claimPattern) {
				t.Errorf("doc %s no longer contains claim %q\n"+
					"  Either re-introduce the claim or remove the registry entry %q\n"+
					"  (and the corresponding test %s if it has no other purpose).",
					c.docFile, c.claimPattern, c.name, c.testName)
			}

			// 3. Asserting test exists in the corpus.
			if !testNames[c.testName] {
				t.Errorf("no test function named %s found under %s\n"+
					"  The claim %q in %s is not enforced by any TestDocClaim_*.\n"+
					"  Add the test, or remove the registry entry %q if the claim is no longer load-bearing.",
					c.testName, root, c.claimPattern, c.docFile, c.name)
			}
		})
	}
}

// unregisteredDocClaimOptOut names TestDocClaim_* tests that
// deliberately don't fit the docClaimRegistry model (which requires a
// positive claimPattern that must appear in an in-repo docFile). They
// keep the TestDocClaim_ naming convention — so cold-start agents
// grepping for "doc claim tests" still find them — but opt out of the
// orphan check below.
//
// act's suite must not assert on files outside the act repo: a doc
// claim in another repo (e.g. a command defined in a separate repo)
// is that repo's to test, not act's. The former cross-repo entry here
// was an inverted dependency and was removed (act-e7ca40).
var unregisteredDocClaimOptOut = map[string]string{
	// Absence property: asserts `act help` output contains no bare internal
	// tracker IDs outside example-session blocks. This does not fit the
	// registry's "claimPattern must appear in docFile" model (there is no
	// positive string to pin), so it lives here as an opt-out from the
	// orphan check rather than a registry entry.
	"TestDocClaim_Help_NoBareTrackerIDs": "cmd/act/help.go (absence: no bare act-XXXX outside example blocks)",
	// act-57e743. Absence property: no instructional surface teaches the
	// work commit with a commit-all flag (-a / -am / --all), which in a
	// checkout shared by concurrent sessions commits a sibling session's
	// dirty files. The positive half (the shared-checkout rationale must
	// be present) IS in the registry above.
	"TestDocClaim_Docs_NoCommitAllInCanonicalLoop": "cmd/act/help.go, skills/act/SKILL.md, README.md (absence: no `git commit -a` in the taught loop)",
}

// TestDocSweep_NoOrphanedDocClaimTests is the inverse pass: every
// TestDocClaim_* function found under the repo must be referenced by
// some registry entry, OR it must be a doc-claim helper (we allow
// shared assertions referenced from multiple registry entries — the
// PrefixOk_* test covers two registry entries). The cap protects
// against tests that *look like* they assert a doc claim but are
// silently disconnected from any tracked surface.
func TestDocSweep_NoOrphanedDocClaimTests(t *testing.T) {
	root := repoRootForDocClaim(t)
	testNames := collectTestNames(t, root)

	registered := map[string]bool{}
	for _, c := range docClaimRegistry {
		registered[c.testName] = true
	}
	var orphans []string
	for name := range testNames {
		if !strings.HasPrefix(name, "TestDocClaim_") {
			continue
		}
		if registered[name] {
			continue
		}
		if _, ok := unregisteredDocClaimOptOut[name]; ok {
			continue
		}
		orphans = append(orphans, name)
	}
	if len(orphans) > 0 {
		t.Errorf("orphaned TestDocClaim_* tests (no registry entry references them): %v\n"+
			"  Either add a docClaimRegistry entry pointing at the doc claim, or rename "+
			"the test if it isn't actually asserting a tracked doc claim.\n"+
			"  Tests that don't fit the registry model can be opted out via unregisteredDocClaimOptOut.", orphans)
	}
}

// collectTestNames walks the repo and returns the set of `func TestXxx`
// names declared in any *_test.go file under the project root. We
// deliberately don't parse the AST — a regex over file contents is
// sufficient and avoids the go/ast import cost.
//
// Files under hidden dirs (.git, .act, .claude) and bin/ are skipped.
func collectTestNames(t *testing.T, root string) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	funcRE := regexp.MustCompile(`(?m)^func\s+(Test[A-Za-z0-9_]+)\s*\(`)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		base := filepath.Base(path)
		if info.IsDir() {
			if base == ".git" || base == ".act" || base == ".claude" ||
				base == "bin" || base == "node_modules" || base == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(base, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range funcRE.FindAllStringSubmatch(string(body), -1) {
			names[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return names
}
