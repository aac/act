package hlc

import (
	"encoding/json"
	"errors"
	"testing"
)

const testNode = "0123abcd"

// fakeNow returns a closure-driven now() and a setter.
func fakeNow(start int64) (func() int64, func(int64)) {
	t := start
	return func() int64 { return t }, func(v int64) { t = v }
}

func TestSendMonotonicity(t *testing.T) {
	now, setNow := fakeNow(1_000_000)
	c := NewClock(testNode, now)
	prev := c.Send()
	for i := 0; i < 10_000; i++ {
		// Drift now forward irregularly, sometimes backward, sometimes stalled.
		switch i % 4 {
		case 0:
			setNow(prev.Wall + 1)
		case 1:
			setNow(prev.Wall) // stalled
		case 2:
			setNow(prev.Wall - 5) // local clock regressed
		case 3:
			setNow(prev.Wall + 100)
		}
		next := c.Send()
		if !prev.Less(next) {
			t.Fatalf("iter %d: prev %+v not Less than next %+v", i, prev, next)
		}
		prev = next
	}
}

func TestSendStalledClockIncrementsLogical(t *testing.T) {
	now, _ := fakeNow(2_000)
	c := NewClock(testNode, now)
	a := c.Send()
	b := c.Send()
	d := c.Send()
	if a.Wall != 2_000 || b.Wall != 2_000 || d.Wall != 2_000 {
		t.Fatalf("walls should all be 2000: %d %d %d", a.Wall, b.Wall, d.Wall)
	}
	if a.Logical != 0 || b.Logical != 1 || d.Logical != 2 {
		t.Fatalf("logical sequence should be 0,1,2; got %d,%d,%d",
			a.Logical, b.Logical, d.Logical)
	}
}

func TestSendAdvancingClockResetsLogical(t *testing.T) {
	now, setNow := fakeNow(1_000)
	c := NewClock(testNode, now)
	a := c.Send()
	b := c.Send()
	if a.Logical != 0 || b.Logical != 1 {
		t.Fatalf("expected 0,1 logical; got %d,%d", a.Logical, b.Logical)
	}
	setNow(2_000)
	d := c.Send()
	if d.Wall != 2_000 || d.Logical != 0 {
		t.Fatalf("expected wall=2000 logical=0; got wall=%d logical=%d",
			d.Wall, d.Logical)
	}
}

func TestReceiveFutureMessageJumpsWall(t *testing.T) {
	now, _ := fakeNow(1_000)
	c := NewClock(testNode, now)
	_ = c.Send() // prev.wall = 1000 logical = 0
	msg := HLC{Wall: 5_000, Logical: 7, NodeID: "ffffffff"}
	got := c.Receive(msg)
	if got.Wall != 5_000 {
		t.Fatalf("want wall=5000, got %d", got.Wall)
	}
	if got.Logical != 8 {
		t.Fatalf("want logical=msg.logical+1=8, got %d", got.Logical)
	}
	if got.NodeID != testNode {
		t.Fatalf("node_id should be local %q, got %q", testNode, got.NodeID)
	}
}

func TestReceiveBothEqualPicksMaxLogicalPlusOne(t *testing.T) {
	now, _ := fakeNow(1_000)
	c := NewClock(testNode, now)
	// Force prev.wall=1000, prev.logical=4
	for i := 0; i < 5; i++ {
		c.Send()
	}
	msg := HLC{Wall: 1_000, Logical: 9, NodeID: "ffffffff"}
	got := c.Receive(msg)
	if got.Wall != 1_000 {
		t.Fatalf("want wall 1000, got %d", got.Wall)
	}
	if got.Logical != 10 {
		t.Fatalf("want logical=max(4,9)+1=10, got %d", got.Logical)
	}
}

func TestReceivePrevWallAhead(t *testing.T) {
	now, setNow := fakeNow(5_000)
	c := NewClock(testNode, now)
	c.Send()   // prev wall=5000 logical=0
	c.Send()   // prev wall=5000 logical=1
	setNow(10) // local regressed; will not affect prev
	msg := HLC{Wall: 100, Logical: 200, NodeID: "ffffffff"}
	got := c.Receive(msg)
	if got.Wall != 5_000 || got.Logical != 2 {
		t.Fatalf("want wall=5000 logical=2; got wall=%d logical=%d",
			got.Wall, got.Logical)
	}
}

func TestPlausibility(t *testing.T) {
	now, _ := fakeNow(10 * 60 * 1000) // 10 minutes since epoch
	c := NewClock(testNode, now)
	ref := HLC{Wall: 10 * 60 * 1000, NodeID: testNode}

	// 4 minutes ahead: accepted.
	ok := HLC{Wall: ref.Wall + 4*60*1000, NodeID: "ffffffff"}
	if err := c.Plausible(ok, ref); err != nil {
		t.Fatalf("4-min drift should be accepted: %v", err)
	}
	// Exactly at budget: accepted.
	atBudget := HLC{Wall: ref.Wall + 5*60*1000, NodeID: "ffffffff"}
	if err := c.Plausible(atBudget, ref); err != nil {
		t.Fatalf("at-budget drift should be accepted: %v", err)
	}
	// 6 minutes ahead: rejected.
	bad := HLC{Wall: ref.Wall + 6*60*1000, NodeID: "ffffffff"}
	err := c.Plausible(bad, ref)
	if err == nil {
		t.Fatalf("6-min drift should be rejected")
	}
	if !errors.Is(err, ErrHLCImplausible) {
		t.Fatalf("err should be ErrHLCImplausible: %v", err)
	}
	// Symmetric: 6 minutes behind also rejected.
	badBehind := HLC{Wall: ref.Wall - 6*60*1000, NodeID: "ffffffff"}
	if err := c.Plausible(badBehind, ref); !errors.Is(err, ErrHLCImplausible) {
		t.Fatalf("6-min-behind drift should be rejected, got %v", err)
	}
}

func TestPlausibilityRefIsMaxOfNowAndRepo(t *testing.T) {
	// Local now is way behind repoRef; reference is repoRef.
	now, _ := fakeNow(0)
	c := NewClock(testNode, now)
	ref := HLC{Wall: 1_000_000_000, NodeID: testNode}
	good := HLC{Wall: ref.Wall + 4*60*1000, NodeID: "ffffffff"}
	if err := c.Plausible(good, ref); err != nil {
		t.Fatalf("expected accept, got %v", err)
	}
}

func TestJSONRoundTripPreservesMillis(t *testing.T) {
	cases := []int64{
		0,
		1_000,
		1_735_689_600_123, // 2025-01-01T00:00:00.123Z
		1_735_689_600_001, // .001
		1_735_689_600_010, // .010
		1_735_689_600_100, // .100
		1_735_689_600_999, // .999
		946_684_800_000,   // 2000-01-01T00:00:00.000Z
		4_102_444_800_000, // 2100-01-01T00:00:00.000Z
	}
	for _, ms := range cases {
		in := HLC{Wall: ms, Logical: 42, NodeID: testNode}
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatalf("marshal %d: %v", ms, err)
		}
		var out HLC
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("unmarshal %s: %v", b, err)
		}
		if out.Wall != in.Wall {
			t.Fatalf("wall round-trip: in=%d out=%d (json=%s)", in.Wall, out.Wall, b)
		}
		if out.Logical != in.Logical || out.NodeID != in.NodeID {
			t.Fatalf("logical/node_id mismatch: in=%+v out=%+v", in, out)
		}
	}
}

func TestJSONFormatShape(t *testing.T) {
	in := HLC{Wall: 1_735_689_600_123, Logical: 7, NodeID: testNode}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"wall":"2025-01-01T00:00:00.123Z","logical":7,"node_id":"0123abcd"}`
	if string(b) != want {
		t.Fatalf("unexpected json:\n got %s\nwant %s", b, want)
	}
}

func TestJSONUnmarshalRejectsBadWall(t *testing.T) {
	bad := []string{
		`{"wall":"2025-01-01T00:00:00Z","logical":0,"node_id":"0123abcd"}`,          // no millis
		`{"wall":"2025-01-01T00:00:00.123+00:00","logical":0,"node_id":"0123abcd"}`, // offset, not Z
		`{"wall":"2025-01-01T00:00:00.123","logical":0,"node_id":"0123abcd"}`,       // missing Z
		`{"wall":"2025-01-01T00:00:00.123Z","logical":0,"node_id":"ABCD0123"}`,      // uppercase node
		`{"wall":"2025-01-01T00:00:00.123Z","logical":0,"node_id":"zzzzzzzz"}`,      // non-hex node
		`{"wall":"2025-01-01T00:00:00.123Z","logical":0,"node_id":"0123abc"}`,       // 7 chars
	}
	for _, s := range bad {
		var h HLC
		if err := json.Unmarshal([]byte(s), &h); err == nil {
			t.Errorf("expected error for %s", s)
		}
	}
}

func TestLessTupleOrdering(t *testing.T) {
	// Walls differ.
	a := HLC{Wall: 1, Logical: 99, NodeID: "ffffffff"}
	b := HLC{Wall: 2, Logical: 0, NodeID: "00000000"}
	if !a.Less(b) || b.Less(a) {
		t.Fatalf("wall ordering broken: a=%+v b=%+v", a, b)
	}
	// Walls equal, logicals differ.
	c := HLC{Wall: 5, Logical: 1, NodeID: "ffffffff"}
	d := HLC{Wall: 5, Logical: 2, NodeID: "00000000"}
	if !c.Less(d) || d.Less(c) {
		t.Fatalf("logical ordering broken: c=%+v d=%+v", c, d)
	}
	// Walls and logicals equal, node_ids differ.
	e := HLC{Wall: 5, Logical: 7, NodeID: "00000001"}
	f := HLC{Wall: 5, Logical: 7, NodeID: "00000002"}
	if !e.Less(f) || f.Less(e) {
		t.Fatalf("node_id ordering broken: e=%+v f=%+v", e, f)
	}
	// Equal tuple: Less returns false both ways.
	g := HLC{Wall: 5, Logical: 7, NodeID: "00000001"}
	if e.Less(g) || g.Less(e) {
		t.Fatalf("equal HLCs should not be Less either way")
	}
}

func TestSendReceiveInterleavedMonotonicity(t *testing.T) {
	now, setNow := fakeNow(1_000)
	c := NewClock(testNode, now)
	prev := c.Send()
	for i := 0; i < 1000; i++ {
		switch i % 3 {
		case 0:
			setNow(prev.Wall + int64(i%5))
			next := c.Send()
			if !prev.Less(next) {
				t.Fatalf("send iter %d: prev %+v !Less next %+v", i, prev, next)
			}
			prev = next
		case 1:
			msg := HLC{Wall: prev.Wall + int64(i%7), Logical: uint32(i), NodeID: "ffffffff"}
			next := c.Receive(msg)
			if !prev.Less(next) {
				t.Fatalf("recv iter %d: prev %+v !Less next %+v", i, prev, next)
			}
			prev = next
		case 2:
			// receive an old msg; prev must still strictly increase.
			msg := HLC{Wall: prev.Wall - 100, Logical: 0, NodeID: "ffffffff"}
			next := c.Receive(msg)
			if !prev.Less(next) {
				t.Fatalf("recv-old iter %d: prev %+v !Less next %+v", i, prev, next)
			}
			prev = next
		}
	}
}
