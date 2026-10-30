package main

import (
	"os"
	"path/filepath"
	"testing"
)

// act-ee05b6: a claim whose rebase onto origin soft-fails on a dirty nested
// tree (ErrPullRebaseSoftFail, act-68f08b) is decided from local ops in
// internal/claim, but the command does not report that as a win on its own:
// the claim's publish (or --push) still has to land on origin. A push that
// lands is a fast-forward, so origin had no op this checkout lacked and the
// win is confirmed; a rival's earlier claim on origin makes the push
// non-fast-forward and the answer is exit 6 claim_unconfirmed. These tests
// pin that end-to-end property.

// psfDirtyNestedTree commits psf-dirty.txt in dir's nested repo, then
// rewrites it unstaged, so a `git rebase` there refuses ("cannot rebase:
// You have unstaged changes").
func psfDirtyNestedTree(t *testing.T, dir string) {
	t.Helper()
	p := filepath.Join(clrActDir(dir), "psf-dirty.txt")
	if err := os.WriteFile(p, []byte("committed\n"), 0o644); err != nil {
		t.Fatalf("write psf-dirty.txt: %v", err)
	}
	sp6Git(t, "-C", clrActDir(dir), "add", "psf-dirty.txt")
	sp6Git(t, "-C", clrActDir(dir), "commit", "-q", "--no-verify", "-m", "psf: tracked file")
	if err := os.WriteFile(p, []byte("unstaged edit\n"), 0o644); err != nil {
		t.Fatalf("dirty psf-dirty.txt: %v", err)
	}
	if _, err := sp7GitOut("-C", clrActDir(dir), "rebase", "refs/remotes/origin/main"); err == nil {
		t.Fatalf("premise: a rebase in the dirty nested tree did not refuse")
	}
}

func TestPsf_SoftFailedClaimIsNeverAnUnconfirmedWin(t *testing.T) {
	for _, withRival := range []bool{true, false} {
		for _, push := range []bool{false, true} {
			name := "no rival"
			if withRival {
				name = "a rival claimed first"
			}
			if push {
				name += " --push"
			}
			t.Run(name, func(t *testing.T) {
				dir, bare, id := clrSiteWithOrigin(t)
				if withRival {
					won, _, _, code := u2RunClaim(t, clrRival(t, bare), id)
					if code != 0 || !won.Claimed {
						t.Fatalf("B's claim: exit %d claimed=%v, want a win", code, won.Claimed)
					}
				}
				psfDirtyNestedTree(t, dir)
				var extra []string
				if push {
					extra = []string{"--push"}
				}
				res, stdout, _, code := clrRunClaim(t, dir, id, extra...)
				if withRival {
					if code != 6 || res.Claimed || res.Error != "claim_unconfirmed" {
						t.Fatalf("soft-failed claim with a rival on origin: exit %d claimed=%v error=%q; want exit 6 claim_unconfirmed\n%s", code, res.Claimed, res.Error, stdout)
					}
					return
				}
				if code != 0 || !res.Claimed {
					t.Fatalf("soft-failed claim, origin has nothing newer: exit %d claimed=%v; want exit 0\n%s", code, res.Claimed, stdout)
				}
				if !wfxOriginHasHead(t, dir, bare) {
					t.Fatalf("a claim reported as won is not on origin")
				}
			})
		}
	}
}
