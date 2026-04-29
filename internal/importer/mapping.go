package importer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LoadAllMappings returns the union of `bootstrap_id -> local_id` entries
// across all `.act/imports/*.json` files, processed in lex order. Bootstrap
// ids are never re-used across imports per spec §3, so a duplicate key is
// itself a corruption signal; in that case the first lex-order entry wins
// (matching the resolution order documented in the spec).
//
// A missing importsDir is reported as an empty map (not an error): a fresh
// repo with no imports has no mappings.
func LoadAllMappings(importsDir string) (map[string]string, error) {
	out := make(map[string]string)
	entries, err := os.ReadDir(importsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return out, nil
		}
		return nil, fmt.Errorf("importer: read imports dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		full := filepath.Join(importsDir, name)
		data, err := os.ReadFile(full)
		if err != nil {
			return nil, fmt.Errorf("importer: read %s: %w", full, err)
		}
		var mf mappingFile
		if err := json.Unmarshal(data, &mf); err != nil {
			// Skip corrupt mapping files rather than failing the lookup.
			continue
		}
		for k, v := range mf.Mapping {
			if _, dup := out[k]; dup {
				// First-wins: lex order = creation order.
				continue
			}
			out[k] = v
		}
	}
	return out, nil
}
