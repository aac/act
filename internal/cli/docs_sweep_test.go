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
		claimPattern: "init, version, log, list, search, ready, mine, show,",
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
		docFile:      "docs/spec-v2.md",
		claimPattern: "any error-severity finding",
		testName:     "TestDocClaim_Remote_EnableOnlyBlocksOnErrorSeverity",
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
	// remote-add-upstream-* (Phase 2 ticket 1b, act-4f9375): the
	// `act remote add-upstream <url>` verb plus the public-URL refusal
	// path. Help-text claims live in cmd/act/help.go; the
	// stderr-literal refusal message and the `upstream_public` error
	// code claim live in docs/spec-v2.md. Drift shape: someone changes
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
		docFile:      "docs/spec-v2.md",
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
	// orchestrate-phase2-* and migration-phase2-* (Phase 2 ticket 10,
	// act-95bc5c): the cross-repo orchestrate.md update plus the
	// in-repo skill + migration-runbook surfaces that describe the
	// Phase 2 push-attached dispatch flow. The orchestrate.md claim
	// lives in the claude-config repo (commit eae259a on origin/main)
	// and is enforced directly by
	// TestDocClaim_OrchestratePhase2_FromRemoteFlow via os.Readlink —
	// not registered here because this sweep harness can't index files
	// outside the act repo root. The two entries below cover the
	// in-repo doc surfaces.
	{
		name:         "skill-phase2-dispatch-section",
		docFile:      "internal/skill/SKILL.md",
		claimPattern: "Phase 2 dispatch (push-attached)",
		testName:     "TestDocClaim_Skill_Phase2DispatchSection",
	},
	{
		name:         "migration-phase2-cutover-section",
		docFile:      "docs/migration-runbook.md",
		claimPattern: "Phase 1.5 → Phase 2 cutover",
		testName:     "TestDocClaim_MigrationRunbook_Phase2Cutover",
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
		docFile:      "docs/spec-v2.md",
		claimPattern: "act: slow write detected (",
		testName:     "TestDocClaim_SlowWrite_WarningText",
	},
	{
		name:         "slow-write-log-schema",
		docFile:      "docs/spec-v2.md",
		claimPattern: `"duration_ms":`,
		testName:     "TestDocClaim_SlowWrite_LogSchema",
	},
	{
		name:         "pending-push-schema",
		docFile:      "docs/spec-v2.md",
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
		docFile:      "docs/spec-v2.md",
		claimPattern: "act.role=orchestrator",
		testName:     "TestDocClaim_OrchestratorSync_RoleCheck",
	},
	{
		name:         "orchestrator-sync-background-detach",
		docFile:      "docs/spec-v2.md",
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
	// docs/spec-v2.md "Doctor reconciliation (Phase 2)" so a drift in
	// either the spec or the implementation surfaces here.
	{
		name:         "doctor-case-a-prime",
		docFile:      "docs/spec-v2.md",
		claimPattern: "post-receive hook installed",
		testName:     "TestDocClaim_DoctorCase_APrime_HookCheckOnOrchestrator",
	},
	{
		name:         "doctor-case-c-prime",
		docFile:      "docs/spec-v2.md",
		claimPattern: "`worker-without-origin`",
		testName:     "TestDocClaim_DoctorCase_CPrime_WorkerWithoutOrigin",
	},
	{
		name:         "doctor-case-f",
		docFile:      "docs/spec-v2.md",
		claimPattern: "local: <N> unpushed commits ahead of origin",
		testName:     "TestDocClaim_DoctorCase_F_UnpushedCommitsStderr",
	},
	{
		name:         "doctor-case-g",
		docFile:      "docs/spec-v2.md",
		claimPattern: "remote: origin unreachable; run 'act remote sync' from the orchestrator or check connectivity",
		testName:     "TestDocClaim_DoctorCase_G_OriginUnreachableStderr",
	},
	{
		name:         "doctor-case-h",
		docFile:      "docs/spec-v2.md",
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
	// pre-commit-hook-permits-deletions (act-4094c6): the host
	// pre-commit hook installed by `act init` /
	// `act migrate-to-nested` permits staged deletions of `.act/*`
	// paths so a normal `git commit` works for the migrate-to-nested
	// untrack shape (and manual `git rm -r --cached .act/` carries to
	// sibling branches). Documented in docs/migration-runbook.md
	// alongside the addition/modification rejection rule. Drift
	// shape: a future hook-rewrite drops `--diff-filter=d` and
	// re-rejects deletions, breaking the carry-migration workflow
	// without any test signal.
	{
		name:         "pre-commit-hook-permits-deletions",
		docFile:      "docs/migration-runbook.md",
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
		docFile:      "docs/spec-v2.md",
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
	// op-filename-* (act-2f3d): op filenames use '-' rather than ':' in
	// the time component so the on-disk tree is NTFS-safe (otherwise
	// `git checkout` on Windows hosts fails before any Go code runs).
	// The spec's "Op file naming" section is the load-bearing surface
	// for the format claim; the test asserts the form is documented and
	// that the writer produces no-colon filenames at the boundary.
	{
		name:         "op-filename-ntfs-safe-format",
		docFile:      "docs/spec-v2.md",
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
		claimPattern: "dep add, doctor, import, mcp, install-skill,",
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
	// (exit 1, claimed:false) at the subprocess boundary.
	{
		name:         "claim-lost-last-write-wins",
		docFile:      "README.md",
		claimPattern: "concurrent claimers resolve last-write-wins",
		testName:     "TestDocClaim_ClaimLost_LastWriteWins",
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

// crossRepoDocClaimTests names TestDocClaim_* tests that intentionally
// assert claims in doc surfaces OUTSIDE the act repo (e.g. the
// orchestrate command at ~/.claude/commands/orchestrate.md, which is a
// symlink into the claude-config repo). The sweep harness only
// indexes files under the act repo root, so these tests cannot be
// registered in docClaimRegistry. They're listed here to opt them out
// of the orphan check while keeping the TestDocClaim_ naming
// convention intact, so cold-start agents grepping for "doc claim
// tests" still find them.
var crossRepoDocClaimTests = map[string]string{
	// Asserts claude-config orchestrate.md still names
	// `act bootstrap-worker --from-remote` (Phase 2 ticket 10,
	// act-95bc5c). Skips on hosts without the symlink wired in.
	"TestDocClaim_OrchestratePhase2_FromRemoteFlow": "claude-config: commands/orchestrate.md",
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
		if _, ok := crossRepoDocClaimTests[name]; ok {
			continue
		}
		orphans = append(orphans, name)
	}
	if len(orphans) > 0 {
		t.Errorf("orphaned TestDocClaim_* tests (no registry entry references them): %v\n"+
			"  Either add a docClaimRegistry entry pointing at the doc claim, or rename "+
			"the test if it isn't actually asserting a tracked doc claim.\n"+
			"  Tests asserting cross-repo doc surfaces can be opted out via crossRepoDocClaimTests.", orphans)
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
