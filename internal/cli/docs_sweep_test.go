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
	{
		name:         "act-help-go-install",
		docFile:      "README.md",
		claimPattern: "go install github.com/aac/act/cmd/act@latest",
		testName:     "TestDocClaim_GoInstallPath",
	},
	{
		name:         "act-help-subcommands-listing",
		docFile:      "cmd/act/help.go",
		claimPattern: "init version log list search ready mine show",
		testName:     "TestDocClaim_ActHelpListsSubcommands",
	},
	{
		name:         "act-help-bootstrap-worker-subcommand",
		docFile:      "cmd/act/help.go",
		claimPattern: "bootstrap-worker",
		testName:     "TestDocClaim_BootstrapWorker_HelpListsSubcommand",
	},
	{
		name:         "act-help-harvest-subcommand",
		docFile:      "cmd/act/help.go",
		claimPattern: "act harvest <worker-path>",
		testName:     "TestDocClaim_Harvest_HelpListsSubcommand",
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
		name:         "errors-push-exhausted",
		docFile:      "docs/spec-v2.md",
		claimPattern: "push_exhausted",
		testName:     "TestDocClaim_Errors_PushExhausted",
	},
	{
		name:         "errors-remote-unreachable",
		docFile:      "docs/spec-v2.md",
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
	// project pushes synchronously. The spec sentences in docs/spec-v2.md
	// are the load-bearing contract — agents reading the spec cold must
	// learn (a) that auto-publish is on by default when origin is set,
	// (b) that no-origin repos remain local-only, and (c) that retry
	// exhaustion surfaces envelope push_exhausted exit 4. Behavioral
	// assertions live in push_integration_test.go; these entries lock the
	// doc claims that the behavior is supposed to match.
	{
		name:         "pushwrite-auto-publish-on-remote",
		docFile:      "docs/spec-v2.md",
		claimPattern: "synchronous `git push`",
		testName:     "TestDocClaim_PushOnWrite_AutoPublishOnRemote",
	},
	{
		name:         "pushwrite-no-origin-local-only",
		docFile:      "docs/spec-v2.md",
		claimPattern: "No-origin repos skip the publish step silently",
		testName:     "TestDocClaim_PushOnWrite_NoOriginIsLocalOnly",
	},
	{
		name:         "pushwrite-exhaustion-envelope",
		docFile:      "docs/spec-v2.md",
		claimPattern: "exits 4 with envelope `push_exhausted`",
		testName:     "TestDocClaim_PushOnWrite_ExhaustionSurfaceIsPushExhausted",
	},
	{
		name:         "prefix-ok-create-parent",
		docFile:      "cmd/act/create.go",
		claimPattern: "full or unique prefix",
		testName:     "TestDocClaim_PrefixOk_TwoCharUniquePrefixResolves",
	},
	// cache-* (act-20c77e, Phase 2 ticket 5): the read-path TTL cache
	// makes load-bearing claims in docs/spec-v2.md (Read-cache section)
	// and in `act ready --help`. Each entry pins one such claim at the
	// boundary a reader would consult.
	{
		name:         "cache-ttl-five-seconds",
		docFile:      "docs/spec-v2.md",
		claimPattern: "5-second TTL",
		testName:     "TestDocClaim_ReadCache_TTLFiveSeconds",
	},
	{
		name:         "cache-dispatch-mode-bypass",
		docFile:      "docs/spec-v2.md",
		claimPattern: "ACT_DISPATCH_MODE=1",
		testName:     "TestDocClaim_ReadCache_DispatchModeEnvBypass",
	},
	{
		name:         "cache-fresh-no-cache-alias",
		docFile:      "docs/spec-v2.md",
		claimPattern: "`--fresh` / `--no-cache`",
		testName:     "TestDocClaim_ReadCache_FreshNoCacheAlias",
	},
	{
		name:         "cache-fold-invalidation",
		docFile:      "docs/spec-v2.md",
		claimPattern: "fold-checkpoint.json does not survive",
		testName:     "TestDocClaim_ReadCache_FoldCheckpointInvalidation",
	},
	{
		name:         "cache-noop-preserves-checkpoint",
		docFile:      "docs/spec-v2.md",
		claimPattern: "A no-op rebase (HEAD unchanged) leaves both files in place",
		testName:     "TestDocClaim_ReadCache_NoRebaseLeavesCheckpoint",
	},
	{
		name:         "read-cache-fetch-head-source",
		docFile:      "docs/spec-v2.md",
		claimPattern: ".act/.git/FETCH_HEAD",
		testName:     "TestDocClaim_ReadCache_FetchHeadPathLayout",
	},
	{
		name:         "read-cache-fresh-flag-help",
		docFile:      "cmd/act/ready.go",
		claimPattern: "bypass the read-path TTL cache",
		testName:     "TestDocClaim_ReadCache_FreshFlagInReadyHelp",
	},
	// skill-worker-* (act-9e7078): the worker-protocol section in the
	// embedded SKILL.md tells dispatched sub-agents (a) that the
	// orchestrator pre-seeds .act/ via bootstrap-worker before launch and
	// (b) that the orchestrator harvests ops at teardown — so workers can
	// run the canonical loop locally without mid-flight coordination. If
	// either subcommand reference is dropped from the skill, a cold-start
	// worker reads no doc and might invent its own coordination protocol
	// (push from worktree, mid-flight rsync, etc.) — exactly the kind of
	// drift the sweep catches.
	//
	// The orchestrate command itself (~/.claude/commands/orchestrate.md)
	// ALSO makes load-bearing claims about bootstrap-on-dispatch and
	// harvest-on-teardown, but that file lives OUTSIDE this repo
	// (claude-config). The sweep harness resolves docFile relative to
	// the act repo root only (see repoRootForDocClaim in
	// docclaim_test.go) and has no mechanism to reach into another git
	// repo. Adding outside-repo entries would either silently no-op or
	// blow up filepath.Join. The claude-config repo would need its own
	// equivalent sweep to enforce those claims; cross-repo doc-claim
	// enforcement is out of scope for this registry.
	{
		name:         "skill-worker-bootstrap-ref",
		docFile:      "internal/skill/SKILL.md",
		claimPattern: "bootstrap-worker",
		testName:     "TestDocClaim_Skill_MentionsBootstrapWorker",
	},
	{
		name:         "skill-worker-harvest-ref",
		docFile:      "internal/skill/SKILL.md",
		claimPattern: "harvest",
		testName:     "TestDocClaim_Skill_MentionsHarvest",
	},
	{
		name:         "skill-worker-section",
		docFile:      "internal/skill/SKILL.md",
		claimPattern: "Working in a worktree or sandbox",
		testName:     "TestDocClaim_Skill_WorkerProtocolSection",
	},
	// remote-* and config-* (Phase 2 ticket 1a, act-72d20e): the
	// `act remote enable` / `act remote disable` subcommands plus the
	// `act.role` decision that closes v1 OQ #4. Help-text claims live in
	// cmd/act/help.go (helpOverview); spec invariants live in
	// docs/spec-v2.md. The drift shape: someone removes the `remote`
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
		docFile:      "docs/spec-v2.md",
		claimPattern: "MUST exit zero both times",
		testName:     "TestDocClaim_Remote_DisableIsIdempotent",
	},
	{
		name:         "remote-spec-disable-removes-hook-file",
		docFile:      "docs/spec-v2.md",
		claimPattern: "MUST remove the file (not merely",
		testName:     "TestDocClaim_Remote_DisableRemovesHookFile",
	},
	{
		name:         "remote-spec-post-receive-skeleton-ticket-6a",
		docFile:      "docs/spec-v2.md",
		claimPattern: "ticket 6a",
		testName:     "TestDocClaim_Remote_PostReceiveSkeletonNamesTicket",
	},
	{
		name:         "config-act-role-orchestrator",
		docFile:      "docs/spec-v2.md",
		claimPattern: "act.role=orchestrator",
		testName:     "TestDocClaim_Config_ActRoleOrchestrator",
	},
	{
		name:         "config-act-role-worker-default",
		docFile:      "docs/spec-v2.md",
		claimPattern: "default is `worker`",
		testName:     "TestDocClaim_Config_ActRoleDefaultsToWorker",
	},
	// remote-sync-* and hook-* (Phase 2 ticket 6a, act-e29159): the
	// `act remote sync` subcommand plus the post-receive hook body
	// that invokes it. Help-text claim lives in cmd/act/help.go; the
	// stderr-literal and sync-log schema claims live in
	// docs/spec-v2.md; the hook-body claim lives in
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
		docFile:      "docs/spec-v2.md",
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
		docFile:      "docs/spec-v2.md",
		claimPattern: "first JSON field on every line is `reason`",
		testName:     "TestDocClaim_RemoteSync_SyncLogReasonFirstField",
	},
	{
		name:         "remote-sync-log-schema-fields",
		docFile:      "docs/spec-v2.md",
		claimPattern: "| `reason` | string |",
		testName:     "TestDocClaim_RemoteSync_SyncLogSchemaFields",
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
		docFile:      "docs/spec-v2.md",
		claimPattern: "act.role=worker",
		testName:     "TestDocClaim_BootstrapFromRemote_SetsWorkerRole",
	},
	{
		name:         "bootstrap-from-remote-timeout-envelope",
		docFile:      "docs/spec-v2.md",
		claimPattern: "`bootstrap_timeout`",
		testName:     "TestDocClaim_BootstrapFromRemote_TimeoutEnvelope",
	},
	{
		name:         "bootstrap-from-remote-target-not-empty",
		docFile:      "docs/spec-v2.md",
		claimPattern: "`target_not_empty`",
		testName:     "TestDocClaim_BootstrapFromRemote_TargetNotEmptyEnvelope",
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
		if !registered[name] {
			orphans = append(orphans, name)
		}
	}
	if len(orphans) > 0 {
		t.Errorf("orphaned TestDocClaim_* tests (no registry entry references them): %v\n"+
			"  Either add a docClaimRegistry entry pointing at the doc claim, or rename "+
			"the test if it isn't actually asserting a tracked doc claim.", orphans)
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
