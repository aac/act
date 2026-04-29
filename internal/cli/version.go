package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/aac/act/internal/op"
)

// BinaryVersion is the human-readable version of the act binary.
const BinaryVersion = "0.1.0"

// WriterVersion is re-exported from op so the CLI has a single source of truth
// for the writer-version string ops are stamped with.
const WriterVersion = op.WriterVersion

// RunVersion implements `act version`. It is pure with respect to its inputs
// and side-effect-free: it neither writes nor reads anything outside the given
// repoRoot (and only when checkRepo is set).
//
// Returns:
//   - output: a JSON-serializable map with the version payload (success) or
//     an error envelope (skew). Caller renders to stdout/stderr.
//   - exitCode: 0 success, 3 missing .act/, 4 version skew.
func RunVersion(checkRepo bool, repoRoot string) (output any, exitCode int) {
	base := map[string]any{
		"binary_version": BinaryVersion,
		"writer_version": WriterVersion,
		"go_version":     runtime.Version(),
		"platform":       runtime.GOOS + "/" + runtime.GOARCH,
	}
	if !checkRepo {
		return base, 0
	}

	actDir := filepath.Join(repoRoot, ".act")
	if _, err := os.Stat(actDir); os.IsNotExist(err) {
		return map[string]any{
			"error":   "no_repo",
			"message": fmt.Sprintf(".act/ directory not found under %s", repoRoot),
		}, 3
	} else if err != nil {
		return map[string]any{
			"error":   "stat_failed",
			"message": err.Error(),
		}, 3
	}

	opsDir := filepath.Join(actDir, "ops")
	maxVer := ""
	if _, err := os.Stat(opsDir); err == nil {
		walkErr := filepath.WalkDir(opsDir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".json") {
				return nil
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				fmt.Fprintf(os.Stderr, "warning: read %s: %v\n", path, rerr)
				return nil
			}
			var env struct {
				WriterVersion string `json:"writer_version"`
			}
			if jerr := json.Unmarshal(data, &env); jerr != nil {
				fmt.Fprintf(os.Stderr, "warning: parse %s: %v\n", path, jerr)
				return nil
			}
			if env.WriterVersion == "" {
				return nil
			}
			if maxVer == "" || compareSemver(env.WriterVersion, maxVer) > 0 {
				maxVer = env.WriterVersion
			}
			return nil
		})
		if walkErr != nil {
			return map[string]any{
				"error":   "walk_failed",
				"message": walkErr.Error(),
			}, 3
		}
	}

	if maxVer != "" && compareSemver(maxVer, BinaryVersion) > 0 {
		return map[string]any{
			"error":   "version_skew",
			"message": fmt.Sprintf("repo writer_version %s exceeds binary writer_version %s; upgrade act", maxVer, BinaryVersion),
			"details": map[string]any{
				"max_op_version": maxVer,
				"binary_version": BinaryVersion,
			},
		}, 4
	}

	out := make(map[string]any, len(base)+1)
	for k, v := range base {
		out[k] = v
	}
	out["max_op_version"] = maxVer
	return out, 0
}

// compareSemver returns -1, 0, or 1 comparing two semver-style strings by
// splitting on '.', padding with zeros to equal length, and comparing each
// numeric segment. Build/prerelease suffixes are ignored — split by '-' or '+'
// and only the numeric prefix is consumed. Non-numeric segments compare as 0.
func compareSemver(a, b string) int {
	as := splitSemver(a)
	bs := splitSemver(b)
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		var av, bv int
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}

func splitSemver(v string) []int {
	// Strip any prerelease/build metadata: keep prefix before first '-' or '+'.
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			n = 0
		}
		out[i] = n
	}
	return out
}
