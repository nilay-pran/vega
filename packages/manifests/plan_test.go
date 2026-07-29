package manifests

import "testing"

func TestPlanChunkSizeRespectsLimits(t *testing.T) {
	cases := []struct {
		name string
		size int64
	}{
		{"tiny", 1 << 10},
		{"just below floor", MinPartSize - 1},
		{"1 GiB", 1 << 30},
		{"500 GiB", 500 << 30},
		{"5 TB", 5 << 40},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cs := PlanChunkSize(c.size)
			if c.size > MinPartSize && cs < MinPartSize {
				t.Fatalf("chunk size %d below floor", cs)
			}
			if cs > MaxPartSize {
				t.Fatalf("chunk size %d above ceiling", cs)
			}
			if parts := int64(len(PlanChunks(c.size, cs))); parts > MaxParts {
				t.Fatalf("%d parts exceeds %d", parts, MaxParts)
			}
		})
	}
}

func TestPlanChunksLayout(t *testing.T) {
	chunks := PlanChunks(12<<20, 5<<20) // 12 MiB into 5 MiB parts -> 5,5,2
	if len(chunks) != 3 {
		t.Fatalf("got %d chunks, want 3", len(chunks))
	}
	var total int64
	for i, c := range chunks {
		if c.Index != i {
			t.Fatalf("chunk %d has index %d", i, c.Index)
		}
		total += c.Length
	}
	if total != 12<<20 {
		t.Fatalf("chunk lengths sum to %d, want %d", total, 12<<20)
	}
	if chunks[2].Length != 2<<20 {
		t.Fatalf("last chunk = %d, want %d", chunks[2].Length, 2<<20)
	}
}

func TestPlanChunksZeroLength(t *testing.T) {
	if got := PlanChunks(0, MinPartSize); len(got) != 1 || got[0].Length != 0 {
		t.Fatalf("zero-length file should yield one empty chunk, got %+v", got)
	}
}
