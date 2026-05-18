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
	if !strings.HasPrefix(a, "act-") || len(a) != 4+MinShortHexLen {
		t.Fatalf("DeriveID returned malformed id %q", a)
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
	id4, err := ExtendID(p, 4)
	if err != nil {
		t.Fatalf("ExtendID(4) error: %v", err)
	}
	id5, err := ExtendID(p, 5)
	if err != nil {
		t.Fatalf("ExtendID(5) error: %v", err)
	}
	id8, err := ExtendID(p, 8)
	if err != nil {
		t.Fatalf("ExtendID(8) error: %v", err)
	}
	hex4 := strings.TrimPrefix(id4, "act-")
	hex5 := strings.TrimPrefix(id5, "act-")
	hex8 := strings.TrimPrefix(id8, "act-")
	if len(hex4) != 4 || len(hex5) != 5 || len(hex8) != 8 {
		t.Fatalf("unexpected hex lengths: %d/%d/%d", len(hex4), len(hex5), len(hex8))
	}
	if !strings.HasPrefix(hex5, hex4) {
		t.Fatalf("hex5 %q is not extension of hex4 %q", hex5, hex4)
	}
	if !strings.HasPrefix(hex8, hex5) {
		t.Fatalf("hex8 %q is not extension of hex5 %q", hex8, hex5)
	}
}

func TestExtendIDOutOfRange(t *testing.T) {
	p := samplePayload()
	if _, err := ExtendID(p, 3); err == nil {
		t.Fatalf("ExtendID(3) should error")
	}
	if _, err := ExtendID(p, 17); err == nil {
		t.Fatalf("ExtendID(17) should error")
	}
}

func TestPickUniqueNoCollision(t *testing.T) {
	p := samplePayload()
	got, err := PickUnique(p, func(string) bool { return false })
	if err != nil {
		t.Fatalf("PickUnique error: %v", err)
	}
	want, err := ExtendID(p, 4)
	if err != nil {
		t.Fatalf("ExtendID error: %v", err)
	}
	if got != want {
		t.Fatalf("PickUnique = %q, want %q", got, want)
	}
}

func TestPickUniqueExtendsOnCollision(t *testing.T) {
	p := samplePayload()
	four, err := ExtendID(p, 4)
	if err != nil {
		t.Fatalf("ExtendID error: %v", err)
	}
	five, err := ExtendID(p, 5)
	if err != nil {
		t.Fatalf("ExtendID error: %v", err)
	}
	got, err := PickUnique(p, func(id string) bool { return id == four })
	if err != nil {
		t.Fatalf("PickUnique error: %v", err)
	}
	if got != five {
		t.Fatalf("PickUnique = %q, want %q", got, five)
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
// §ID model: short id is "act-" + N hex chars, 4 <= N <= 16). Each boundary
// asserts the exact-cap accept and the cap+/-1 reject so a future tightening
// or loosening of MaxShortHexLen / MinShortHexLen forces the spec citation to
// be revisited.
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
		{"min-1 rejects (3 hex)", hexN(MinShortHexLen - 1), false},
		{"min accepts (4 hex)", hexN(MinShortHexLen), true},
		{"min+1 accepts (5 hex)", hexN(MinShortHexLen + 1), true},
		{"max-1 accepts (15 hex)", hexN(MaxShortHexLen - 1), true},
		{"max accepts (16 hex)", hexN(MaxShortHexLen), true},
		{"max+1 rejects (17 hex)", hexN(MaxShortHexLen + 1), false},
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
