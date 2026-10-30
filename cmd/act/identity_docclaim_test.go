package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Doc claims about the claim identity (docs/claim-identity-design.md §2):
// each checkout of a tracker has its own node_id, minted on first use and
// kept in .act/.git/config (act.nodeId); it never travels with a clone, and
// config.json no longer carries it.

var idHex8 = regexp.MustCompile(`^[0-9a-f]{8}$`)

// idNodeIDOf returns the node_id `act doctor --json` reports for the
// checkout at dir — the user-visible place a checkout's identity is read.
func idNodeIDOf(t *testing.T, dir string) string {
	t.Helper()
	out, stderr, code := runActIn(t, dir, "doctor", "--json", "--no-fetch")
	var res struct {
		NodeID *string `json:"node_id"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.NodeID == nil {
		t.Fatalf("act doctor --json in %s: exit %d, no node_id key (%v)\nstdout=%s\nstderr=%s", dir, code, err, out, stderr)
	}
	if !idHex8.MatchString(*res.NodeID) {
		t.Fatalf("act doctor --json in %s: node_id %q is not 8 lowercase hex", dir, *res.NodeID)
	}
	return *res.NodeID
}

// idStoredNodeID reads act.nodeId from the checkout's nested git config.
func idStoredNodeID(t *testing.T, dir string) string {
	t.Helper()
	id, _ := sp7GitOut("config", "-f", filepath.Join(dir, ".act", ".git", "config"), "--get", "act.nodeId")
	return strings.TrimSpace(id)
}

// idConfigNodeID returns the node_id key in .act/config.json, "" if absent.
func idConfigNodeID(t *testing.T, dir string) (string, bool) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, ".act", "config.json"))
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("parse config.json: %v\n%s", err, body)
	}
	v, ok := cfg["node_id"]
	s, _ := v.(string)
	return s, ok
}

// TestDocClaim_Identity_InitDoesNotWriteNodeIDToConfig: after `act init`,
// config.json has no node_id key; the id init reports is the one stored in
// .act/.git/config and the one doctor reports.
func TestDocClaim_Identity_InitDoesNotWriteNodeIDToConfig(t *testing.T) {
	brokencoIsolateEnv(t)
	t.Setenv("ACT_MACHINE", "")
	t.Setenv("ACT_NO_FETCH", "")
	host := sp8HostWithCommit(t)
	out, _ := pass5Run(t, host, 0, "", "init", "--json")

	if v, has := idConfigNodeID(t, host); has {
		t.Errorf("config.json carries node_id %q after act init; it must not", v)
	}
	var res struct {
		NodeID string `json:"node_id"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || !idHex8.MatchString(res.NodeID) {
		t.Fatalf("act init --json node_id %q (%v)\n%s", res.NodeID, err, out)
	}
	if stored := idStoredNodeID(t, host); stored != res.NodeID {
		t.Errorf("act.nodeId in .act/.git/config = %q, want init's node_id %q", stored, res.NodeID)
	}
	if got := idNodeIDOf(t, host); got != res.NodeID {
		t.Errorf("act doctor --json node_id %q, want init's %q", got, res.NodeID)
	}
	// It is not committed: .git/config is outside the nested repo's tree.
	if tree, _ := sp7GitOut("-C", filepath.Join(host, ".act"), "ls-tree", "-r", "--name-only", "HEAD"); strings.Contains(tree, "nodeId") {
		t.Errorf("the nested repo's tree mentions nodeId: %q", tree)
	}
}

// TestDocClaim_Identity_StateImportGetsItsOwnNodeID: `act state import
// --from-remote` makes a second checkout with its own node_id, distinct from
// the first checkout's and from any node_id left in a legacy config.json.
func TestDocClaim_Identity_StateImportGetsItsOwnNodeID(t *testing.T) {
	a, bare, id := clrSiteWithOrigin(t)

	// A tracker made before this change still has a node_id key in its
	// committed config.json; plant one and publish it so the import carries it.
	const legacy = "f188029a"
	cfgPath := filepath.Join(a, ".act", "config.json")
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("parse config.json: %v", err)
	}
	cfg["node_id"] = legacy
	nb, _ := json.Marshal(cfg)
	if err := os.WriteFile(cfgPath, nb, 0o644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
	sp6Git(t, "-C", filepath.Join(a, ".act"), "commit", "-q", "--no-verify", "-m", "legacy node_id", "--", "config.json")
	sp6Git(t, "-C", filepath.Join(a, ".act"), "push", "-q", bare, "HEAD:main")

	b := clrRival(t, bare)
	if v, _ := idConfigNodeID(t, b); v != legacy {
		t.Fatalf("setup: the import did not carry the legacy config.json node_id (got %q)", v)
	}

	idA, idB := idNodeIDOf(t, a), idNodeIDOf(t, b)
	if idA == idB {
		t.Errorf("act doctor --json node_id is %q in both checkouts; a state import must get its own", idA)
	}
	if idA == legacy || idB == legacy {
		t.Errorf("a checkout adopted the config.json node_id %q (A=%q, B=%q)", legacy, idA, idB)
	}

	// The imported checkout claims as itself: winner is its own node_id.
	out, stderr, code := runActIn(t, b, "update", "--claim", "--json", id)
	if code != 0 || !strings.Contains(out, `"winner":"`+idB+`"`) {
		t.Errorf("claim from the imported checkout: exit %d, want 0 with winner %s\nstdout=%s\nstderr=%s", code, idB, out, stderr)
	}
}
