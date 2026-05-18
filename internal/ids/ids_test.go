package ids

import (
	"strings"
	"testing"
)

func samplePayload() CreatePayload {
	return CreatePayload{
		Title:       "demo",
		Description: "a sample",
		Priority:    2,
		Type:        "task",
		Parent:      "",
		Accept:      []string{"works", "is tested"},
		Nonce:       "00112233445566778899aabbccddeeff",
	}
}

func TestNewNonceUnique(t *testing.T) {
	const calls = 100
	seen := make(map[string]struct{}, calls)
	for i := 0; i < calls; i++ {
		n, err := NewNonce()
		if err != nil {
			t.Fatalf("NewNonce error: %v", err)
		}
		if len(n) != 32 {
			t.Fatalf("nonce length = %d, want 32", len(n))
		}
		for _, r := range n {
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
				t.Fatalf("nonce %q contains non-hex char %q", n, r)
			}
		}
		if _, dup := seen[n]; dup {
			t.Fatalf("duplicate nonce after %d calls: %q", i, n)
		}
		seen[n] = struct{}{}
	}
}

func TestDeriveIDDeterministic(t *testing.T) {
	p := samplePayload()
	a, err := DeriveID(p)
	if err != nil {
		t.Fatalf("DeriveID error: %v", err)
	}
	b, err := DeriveID(p)
	if err != nil {
		t.Fatalf("DeriveID error: %v", err)
	}
	if a != b {
		t.Fatalf("DeriveID not deterministic: %q vs %q", a, b)
	}
	if !strings.HasPrefix(a, "act-") || len(a) != len("act-")+MinShortHexLen {
		t.Fatalf("DeriveID returned malformed id %q (want len=%d, got %d)", a, len("act-")+MinShortHexLen, len(a))
	}
}

func TestDeriveIDDiffersOnNonce(t *testing.T) {
	p1 := samplePayload()
	p2 := samplePayload()
	p2.Nonce = "ffeeddccbbaa99887766554433221100"
	a, err := DeriveID(p1)
	if err != nil {
		t.Fatalf("DeriveID error: %v", err)
	}
	b, err := DeriveID(p2)
	if err != nil {
		t.Fatalf("DeriveID error: %v", err)
	}
	if a == b {
		t.Fatalf("DeriveID should differ for different nonces, both = %q", a)
	}
}

func TestExtendIDPrefixGrowth(t *testing.T) {
	p := samplePayload()
	// MinShortHexLen is the generation floor (6 since act-f9a0); ExtendID
	// rejects anything below it. Exercise the growth path at the floor and
	// at two longer lengths.
	id6, err := ExtendID(p, MinShortHexLen)
	if err != nil {
		t.Fatalf("ExtendID(%d) error: %v", MinShortHexLen, err)
	}
	id7, err := ExtendID(p, MinShortHexLen+1)
	if err != nil {
		t.Fatalf("ExtendID(%d) error: %v", MinShortHexLen+1, err)
	}
	id10, err := ExtendID(p, MinShortHexLen+4)
	if err != nil {
		t.Fatalf("ExtendID(%d) error: %v", MinShortHexLen+4, err)
	}
	hex6 := strings.TrimPrefix(id6, "act-")
	hex7 := strings.TrimPrefix(id7, "act-")
	hex10 := strings.TrimPrefix(id10, "act-")
	if len(hex6) != MinShortHexLen || len(hex7) != MinShortHexLen+1 || len(hex10) != MinShortHexLen+4 {
		t.Fatalf("unexpected hex lengths: %d/%d/%d", len(hex6), len(hex7), len(hex10))
	}
	if !strings.HasPrefix(hex7, hex6) {
		t.Fatalf("hex7 %q is not extension of hex6 %q", hex7, hex6)
	}
	if !strings.HasPrefix(hex10, hex7) {
		t.Fatalf("hex10 %q is not extension of hex7 %q", hex10, hex7)
	}
}

func TestExtendIDOutOfRange(t *testing.T) {
	p := samplePayload()
	if _, err := ExtendID(p, MinShortHexLen-1); err == nil {
		t.Fatalf("ExtendID(%d) should error", MinShortHexLen-1)
	}
	if _, err := ExtendID(p, MaxShortHexLen+1); err == nil {
		t.Fatalf("ExtendID(%d) should error", MaxShortHexLen+1)
	}
}

func TestPickUniqueNoCollision(t *testing.T) {
	p := samplePayload()
	got, err := PickUnique(p, func(string) bool { return false })
	if err != nil {
		t.Fatalf("PickUnique error: %v", err)
	}
	want, err := ExtendID(p, MinShortHexLen)
	if err != nil {
		t.Fatalf("ExtendID error: %v", err)
	}
	if got != want {
		t.Fatalf("PickUnique = %q, want %q", got, want)
	}
}

func TestPickUniqueExtendsOnCollision(t *testing.T) {
	p := samplePayload()
	floor, err := ExtendID(p, MinShortHexLen)
	if err != nil {
		t.Fatalf("ExtendID error: %v", err)
	}
	floorPlus1, err := ExtendID(p, MinShortHexLen+1)
	if err != nil {
		t.Fatalf("ExtendID error: %v", err)
	}
	got, err := PickUnique(p, func(id string) bool { return id == floor })
	if err != nil {
		t.Fatalf("PickUnique error: %v", err)
	}
	if got != floorPlus1 {
		t.Fatalf("PickUnique = %q, want %q", got, floorPlus1)
	}
}

func TestPickUniqueAllCollide(t *testing.T) {
	p := samplePayload()
	_, err := PickUnique(p, func(string) bool { return true })
	if err == nil {
		t.Fatalf("PickUnique should error when every prefix collides")
	}
}

func TestIsValidID(t *testing.T) {
	good := []string{
		"act-1234",
		"act-abcdef",
		"act-0123456789abcdef", // 16 hex chars
		"act-aaaa",
	}
	for _, s := range good {
		if !IsValidID(s) {
			t.Errorf("IsValidID(%q) = false, want true", s)
		}
	}
	bad := []string{
		"act-",                  // empty hex
		"act-12",                // too short
		"act-12345678901234567", // 17 hex chars, too long
		"ACT-1234",              // uppercase prefix
		"act-zzzz",              // non-hex
		"act-ABCD",              // uppercase hex
		"foo-1234",              // wrong prefix
		"act 1234",              // wrong separator
		"",
	}
	for _, s := range bad {
		if IsValidID(s) {
			t.Errorf("IsValidID(%q) = true, want false", s)
		}
	}
}

// TestIsValidIDBoundaries pins the lower and upper hex-length bounds enforced
// by IsValidID against the spec authority cited in ids.go (docs/spec-v2.md
// §ID model: short id is "act-" + N hex chars, 4 <= N <= 16). The lower bound
// of the on-disk syntax (4) is intentionally below MinShortHexLen (6 since
// act-f9a0): historical ids minted under the prior generation floor must keep
// validating so existing repos can be read. New ids generated today are 6+,
// but a `act-aaaa` written before the bump is still a syntactically valid id.
// Each boundary asserts the exact-cap accept and the cap+/-1 reject so a
// future tightening or loosening of the on-disk bounds forces the spec
// citation to be revisited.
func TestIsValidIDBoundaries(t *testing.T) {
	// Build a hex string of length n using the digit '0'. Hex is the right
	// alphabet for these tests because IsValidID's only role is syntax.
	hexN := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = '0'
		}
		return "act-" + string(b)
	}

	cases := []struct {
		name string
		s    string
		want bool
	}{
		{"3 hex rejects", hexN(3), false},
		{"4 hex accepts (historical floor)", hexN(4), true},
		{"5 hex accepts (historical, post-collision)", hexN(5), true},
		{"6 hex accepts (current generation floor = MinShortHexLen)", hexN(MinShortHexLen), true},
		{"7 hex accepts (MinShortHexLen+1)", hexN(MinShortHexLen + 1), true},
		{"15 hex accepts (MaxShortHexLen-1)", hexN(MaxShortHexLen - 1), true},
		{"16 hex accepts (MaxShortHexLen)", hexN(MaxShortHexLen), true},
		{"17 hex rejects (MaxShortHexLen+1)", hexN(MaxShortHexLen + 1), false},
		{"sha256-width rejects (64 hex)", hexN(64), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsValidID(tc.s); got != tc.want {
				t.Errorf("IsValidID(%q) = %v, want %v", tc.s, got, tc.want)
			}
		})
	}
}
