package op

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aac/act/internal/hlc"
)

// ApplyOpFunc is the per-op-version apply function signature used by the
// version-dispatch registry. State is intentionally typed `any` to keep this
// package free of a dependency on the fold package (which itself depends on
// op). The fold package registers a wrapper at init time that performs the
// concrete type assertion before delegating to fold.ApplyDispatch.
type ApplyOpFunc func(state any, env Envelope, payload []byte) error

// opVersionEntry is the value type of OpVersionRegistry. Future op_versions
// add new entries here; old entries are never removed (per spec §6.4 — old
// op_versions must keep folding identically forever).
type opVersionEntry struct {
	ApplyOp ApplyOpFunc
}

// OpVersionRegistry maps op_version → apply function. For v0.1 only version
// 1 is wired; future versions register additional entries via
// RegisterOpVersion. Reads of this map should go through DispatchByVersion to
// preserve the unknown-version error contract.
//
// The fold package populates the version=1 slot at init time; tests may also
// register entries directly.
var OpVersionRegistry = map[int]opVersionEntry{}

var registryMu sync.RWMutex

// RegisterOpVersion installs apply as the dispatch target for op_version v.
// Re-registering an already-registered version overwrites the previous
// entry; this is intentional so tests can stub the registry, but production
// code should treat each (op_version, apply) pair as register-once.
func RegisterOpVersion(v int, apply ApplyOpFunc) {
	registryMu.Lock()
	defer registryMu.Unlock()
	OpVersionRegistry[v] = opVersionEntry{ApplyOp: apply}
}

// DispatchByVersion returns the apply function for opVersion. It returns an
// error when the version is not registered — the binary cannot fold an op it
// has no apply rule for, which the caller surfaces as version_skew per spec.
func DispatchByVersion(opVersion int) (ApplyOpFunc, error) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	entry, ok := OpVersionRegistry[opVersion]
	if !ok || entry.ApplyOp == nil {
		return nil, fmt.Errorf("op: dispatch: unknown op_version %d", opVersion)
	}
	return entry.ApplyOp, nil
}

// Migration describes how to rewrite an old-version envelope into one or
// more new-version envelopes. A v1→v2 split (e.g. one op carrying two
// fields turning into one op per field) returns multiple envelopes; a
// rename returns a single rewritten envelope.
type Migration struct {
	FromVersion int
	ToVersion   int
	Description string
	Transform   func(env Envelope) ([]Envelope, error)
}

// Migrations is the ordered list of registered migrations. Empty in v0.1;
// future versions append entries here. Lookup is by (from, to) tuple — see
// findMigration.
var Migrations = []Migration{}

// findMigration returns the first Migration matching (from, to), or false.
func findMigration(from, to int) (Migration, bool) {
	for _, m := range Migrations {
		if m.FromVersion == from && m.ToVersion == to {
			return m, true
		}
	}
	return Migration{}, false
}

// MigrateOutput is the JSON-serialisable success payload of RunMigrate.
type MigrateOutput struct {
	MigratedIssues int `json:"migrated_issues"`
	WroteOps       int `json:"wrote_ops"`
}

// MigrateError is the JSON-serialisable error envelope for RunMigrate.
type MigrateError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// RunMigrate walks `<repoRoot>/.act/ops/**/*.json`, finds envelopes at
// fromVersion, looks up a Migration in Migrations matching (from, to), and
// applies its Transform to produce one or more new envelopes at toVersion.
// Each new envelope is written via ProbeAndWrite with a freshly-stamped HLC
// from the local clock so it sorts after the originals during fold. One
// migrate-typed envelope per affected issue is also emitted recording the
// {from_version, to_version} transition.
//
// fromVersion must be > 0 and strictly less than toVersion. The output is a
// JSON-serialisable map; exitCode is 0 on success, 2 on bad input, 5 on
// missing migration, 1 on I/O failure.
func RunMigrate(repoRoot string, fromVersion, toVersion int) (output any, exitCode int) {
	if fromVersion <= 0 || toVersion <= 0 {
		return MigrateError{
			Error:   "bad_input",
			Message: fmt.Sprintf("from_version=%d to_version=%d: both must be > 0", fromVersion, toVersion),
		}, 2
	}
	if fromVersion >= toVersion {
		return MigrateError{
			Error:   "bad_input",
			Message: fmt.Sprintf("from_version=%d must be < to_version=%d", fromVersion, toVersion),
		}, 2
	}

	migration, found := findMigration(fromVersion, toVersion)
	if !found {
		return MigrateError{
			Error:   "migration_not_found",
			Message: fmt.Sprintf("no Migration registered for from=%d to=%d", fromVersion, toVersion),
		}, 5
	}

	rootOps := filepath.Join(repoRoot, ".act", "ops")
	if _, err := os.Stat(rootOps); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return MigrateOutput{MigratedIssues: 0, WroteOps: 0}, 0
		}
		return MigrateError{
			Error:   "io_failure",
			Message: fmt.Sprintf("stat %s: %v", rootOps, err),
		}, 1
	}

	// Discover all envelopes at fromVersion, grouped by issue id.
	affectedByIssue := map[string][]Envelope{}
	walkErr := filepath.WalkDir(rootOps, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return fmt.Errorf("read %s: %w", path, rerr)
		}
		// Use a permissive decoder so we can read mixed-version op files
		// without imposing the current envelope's Validate constraints.
		var raw struct {
			OpVersion int `json:"op_version"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		if raw.OpVersion != fromVersion {
			return nil
		}
		// Decode as a full envelope; this requires the envelope to validate
		// per the current binary's rules. Mixed-version repos may need a
		// per-version decoder in the future, but for v0.1 fromVersion=1
		// matches the current envelope shape.
		env, err := Unmarshal(body)
		if err != nil {
			return fmt.Errorf("unmarshal %s: %w", path, err)
		}
		affectedByIssue[env.IssueID] = append(affectedByIssue[env.IssueID], env)
		return nil
	})
	if walkErr != nil {
		return MigrateError{
			Error:   "io_failure",
			Message: walkErr.Error(),
		}, 1
	}

	if len(affectedByIssue) == 0 {
		return MigrateOutput{MigratedIssues: 0, WroteOps: 0}, 0
	}

	// Build a clock for stamping the migrated envelopes. We borrow the
	// node_id from the first affected envelope (any envelope works — they
	// all live in the same repo). The local wall comes from time.Now.
	var nodeID string
	for _, envs := range affectedByIssue {
		if len(envs) > 0 {
			nodeID = envs[0].NodeID
			break
		}
	}
	clock := hlc.NewClock(nodeID, func() int64 { return time.Now().UTC().UnixMilli() })
	// Seed the clock past the latest existing HLC so re-stamps sort strictly
	// after every original op.
	for _, envs := range affectedByIssue {
		for _, e := range envs {
			clock.Receive(e.HLC)
		}
	}

	fsLock := func() (func(), error) { return func() {}, nil }

	wroteOps := 0
	for issueID, envs := range affectedByIssue {
		for _, env := range envs {
			newEnvs, terr := migration.Transform(env)
			if terr != nil {
				return MigrateError{
					Error:   "transform_failed",
					Message: fmt.Sprintf("issue=%s: %v", issueID, terr),
				}, 1
			}
			for _, ne := range newEnvs {
				ne.HLC = clock.Send()
				ne.NodeID = nodeID
				body, err := ne.Marshal()
				if err != nil {
					return MigrateError{
						Error:   "marshal_failed",
						Message: err.Error(),
					}, 1
				}
				if _, _, err := ProbeAndWrite(rootOps, ne, body, fsLock); err != nil {
					return MigrateError{
						Error:   "write_failed",
						Message: err.Error(),
					}, 1
				}
				wroteOps++
			}
		}

		// Emit one migrate-typed op per affected issue recording the transition.
		transformID := migration.Description
		if transformID == "" {
			transformID = fmt.Sprintf("v%d-to-v%d", fromVersion, toVersion)
		}
		mPayload := struct {
			FromVersion int    `json:"from_version"`
			ToVersion   int    `json:"to_version"`
			TransformID string `json:"transform_id"`
		}{
			FromVersion: fromVersion,
			ToVersion:   toVersion,
			TransformID: transformID,
		}
		payloadBytes, err := json.Marshal(mPayload)
		if err != nil {
			return MigrateError{
				Error:   "marshal_failed",
				Message: err.Error(),
			}, 1
		}
		mEnv := Envelope{
			OpVersion:     CurrentOpVersion,
			SchemaVersion: CurrentSchemaVersion,
			WriterVersion: WriterVersion,
			OpType:        "migrate",
			IssueID:       issueID,
			Payload:       payloadBytes,
			HLC:           clock.Send(),
			NodeID:        nodeID,
		}
		body, err := mEnv.Marshal()
		if err != nil {
			return MigrateError{
				Error:   "marshal_failed",
				Message: err.Error(),
			}, 1
		}
		if _, _, err := ProbeAndWrite(rootOps, mEnv, body, fsLock); err != nil {
			return MigrateError{
				Error:   "write_failed",
				Message: err.Error(),
			}, 1
		}
		wroteOps++
	}

	return MigrateOutput{
		MigratedIssues: len(affectedByIssue),
		WroteOps:       wroteOps,
	}, 0
}

// ReadMaxOpVersion walks rootOps and returns the maximum op_version seen
// across all op files. A missing rootOps yields (0, nil). Files that do not
// look like op envelopes are skipped silently — the fold path is the
// authority on validation.
func ReadMaxOpVersion(rootOps string) (int, error) {
	if _, err := os.Stat(rootOps); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("op: ReadMaxOpVersion: stat %s: %w", rootOps, err)
	}
	maxV := 0
	walkErr := filepath.WalkDir(rootOps, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return fmt.Errorf("read %s: %w", path, rerr)
		}
		var raw struct {
			OpVersion int `json:"op_version"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			// Non-JSON or malformed: skip rather than fail.
			return nil
		}
		if raw.OpVersion > maxV {
			maxV = raw.OpVersion
		}
		return nil
	})
	if walkErr != nil {
		return 0, fmt.Errorf("op: ReadMaxOpVersion: walk: %w", walkErr)
	}
	return maxV, nil
}
