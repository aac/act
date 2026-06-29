package fold

// FuzzFoldDeterminism uses Go's native fuzzing engine to drive op-sequence
// generation from raw input bytes. The fuzz function decodes the bytes into
// a list of op envelopes via a small custom decoder, folds the sequence,
// permutes the file order via a deterministic PRNG seeded from the input,
// folds again, and asserts the two canonical renders are byte-identical.
//
// The seed corpus is a few hand-crafted byte strings; the corpus directory
// under testdata/fuzz/FuzzFoldDeterminism/ accumulates findings as the
// fuzzer runs in fuzz mode (`go test -fuzz`).

import (
	"bytes"
	"encoding/binary"
	"math/rand"
	"testing"

	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

// fuzzMaxInputBytes caps the input size we'll process. Beyond this we
// truncate; the fuzzer runs many short cases per second instead of a few
// long ones.
const fuzzMaxInputBytes = 256

// fuzzMaxOps caps the number of ops we'll decode from a single input.
const fuzzMaxOps = 24

// FuzzFoldDeterminism fuzzes the cold/permuted fold-determinism contract.
func FuzzFoldDeterminism(f *testing.F) {
	// Hand-crafted seed corpus: a short, medium, and long input chosen to
	// exercise the decoder's different code paths (single op, multi op,
	// uniform vs jittered HLCs).
	f.Add([]byte{0x01, 0x00, 0x00, 0x00})
	f.Add([]byte{0x02, 0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08})
	f.Add([]byte{
		0x05,
		0x00, 0x01, 0x02, 0x03,
		0x04, 0x05, 0x06, 0x07,
		0x08, 0x09, 0x0a, 0x0b,
		0x0c, 0x0d, 0x0e, 0x0f,
		0x10, 0x11, 0x12, 0x13,
	})
	// One pathological seed: identical bytes repeated.
	f.Add(bytes.Repeat([]byte{0xaa}, 64))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fuzzMaxInputBytes {
			data = data[:fuzzMaxInputBytes]
		}
		envs := decodeFuzzInput(data)
		if len(envs) == 0 {
			return // empty op set has trivial determinism
		}

		baseline := foldEnvsToCanonical(t, envs)

		// Permute the order using a PRNG seeded from the input bytes so
		// repeat runs of the same input produce the same permutation.
		seed := int64(0)
		for i, b := range data {
			seed ^= int64(b) << uint((i%8)*8)
		}
		r := rand.New(rand.NewSource(seed))
		perm := make([]op.Envelope, len(envs))
		copy(perm, envs)
		r.Shuffle(len(perm), func(i, j int) { perm[i], perm[j] = perm[j], perm[i] })

		got := foldEnvsToCanonical(t, perm)
		if !bytes.Equal(baseline, got) {
			t.Fatalf("fold determinism violated\ninput: %x\nwant:  %s\ngot:   %s", data, baseline, got)
		}
	})
}

// decodeFuzzInput parses raw bytes into a list of op envelopes. The byte
// layout is intentionally simple so the fuzzer's mutation operators (bit
// flips, trim, etc.) explore the space efficiently:
//
//	byte 0: number of ops to emit (capped at fuzzMaxOps)
//	bytes 1..: per-op record of fixed length 4
//	  [0] op-type index mod len(allOpTypes)
//	  [1] issue index mod len(fixedIssues)
//	  [2] HLC wall delta (1..16)
//	  [3] node index mod len(fixedNodes)
//
// All output envelopes are well-formed: payloads come from generatePayload
// which is the same generator the property tests use.
func decodeFuzzInput(data []byte) []op.Envelope {
	if len(data) == 0 {
		return nil
	}
	n := int(data[0])
	if n > fuzzMaxOps {
		n = fuzzMaxOps
	}
	if n <= 0 {
		return nil
	}
	cursor := 1
	r := rand.New(rand.NewSource(deriveSeed(data)))

	// Pre-seed every issue with a create so subsequent ops have state to
	// mutate, mirroring makeRandomOps. We use a deterministic local clock
	// derived from input bytes.
	envs := make([]op.Envelope, 0, n+len(fixedIssues))
	var wall int64 = 1
	for i, id := range fixedIssues {
		create := op.CreatePayload{
			Title:    "fuzz-init-" + string(rune('a'+i)),
			Type:     "task",
			Nonce:    deterministicNonce(r),
			Priority: ptrInt(1),
		}
		envs = append(envs, buildEnvelope(id, "create", makeFuzzHLC(wall, 0, fixedNodes[0]), create))
		wall++
	}

	for i := 0; i < n; i++ {
		var rec [4]byte
		for j := 0; j < 4; j++ {
			if cursor < len(data) {
				rec[j] = data[cursor]
				cursor++
			} else {
				rec[j] = byte(j) // pad with stable bytes if input is short
			}
		}
		opType := allOpTypes[int(rec[0])%len(allOpTypes)]
		issue := fixedIssues[int(rec[1])%len(fixedIssues)]
		wall += int64(rec[2]&0x0f) + 1
		node := fixedNodes[int(rec[3])%len(fixedNodes)]
		payload := generatePayload(r, opType, issue)
		if payload == nil {
			continue
		}
		envs = append(envs, buildEnvelope(issue, opType, makeFuzzHLC(wall, uint32(rec[3]&0x07), node), payload))
	}
	return envs
}

// deriveSeed mixes input bytes into an int64 seed for the local PRNG used
// by generatePayload. Using binary.LittleEndian on an 8-byte zero-padded
// slice is sufficient — the fuzzer cares about determinism, not entropy.
func deriveSeed(data []byte) int64 {
	var buf [8]byte
	copy(buf[:], data)
	return int64(binary.LittleEndian.Uint64(buf[:]))
}

// makeFuzzHLC is a thin constructor mirroring hlc.HLC literal usage in the
// other test helpers; kept here so future fuzz-specific HLC adjustments
// (e.g., logical-counter saturation) have a localised home.
func makeFuzzHLC(wall int64, logical uint32, node string) hlc.HLC {
	return hlc.HLC{Wall: wall, Logical: logical, NodeID: node}
}
