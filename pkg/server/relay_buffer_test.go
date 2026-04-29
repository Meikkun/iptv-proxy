package server

import (
	"testing"
	"time"
)

func TestRelayBufferTrimsByAgeAndStartsNearTargetDelay(t *testing.T) {
	buffer := newRelayBuffer(10*time.Second, 1024)
	base := time.Unix(1700000000, 0)

	buffer.append(base.Add(0*time.Second), []byte("a"))
	buffer.append(base.Add(4*time.Second), []byte("b"))
	buffer.append(base.Add(8*time.Second), []byte("c"))
	buffer.append(base.Add(12*time.Second), []byte("d"))

	if got := buffer.coverage(); got != 8*time.Second {
		t.Fatalf("buffer.coverage() = %v, want %v", got, 8*time.Second)
	}

	if got := buffer.startSeqForDelay(4 * time.Second); got != 3 {
		t.Fatalf("buffer.startSeqForDelay() = %d, want 3", got)
	}
}

func TestRelayBufferTrimsByMaxBytes(t *testing.T) {
	buffer := newRelayBuffer(time.Minute, 5)
	now := time.Unix(1700000000, 0)

	buffer.append(now, []byte("aaa"))
	buffer.append(now.Add(time.Second), []byte("bbb"))

	if !buffer.hasData() {
		t.Fatal("buffer.hasData() = false, want true")
	}

	if got := buffer.bytes(); got != 3 {
		t.Fatalf("buffer.bytes() = %d, want 3", got)
	}

	chunk, ok := buffer.chunkAtOrAfter(1)
	if !ok {
		t.Fatal("buffer.chunkAtOrAfter() = false, want true")
	}

	if string(chunk.data) != "bbb" {
		t.Fatalf("buffer.chunkAtOrAfter() = %q, want %q", string(chunk.data), "bbb")
	}
}

func TestRelayBufferSeqAvailableDetectsTrimmedSequence(t *testing.T) {
	buffer := newRelayBuffer(time.Minute, 5)
	now := time.Unix(1700000000, 0)

	buffer.append(now, []byte("aaa"))
	buffer.append(now.Add(time.Second), []byte("bbb"))

	if buffer.seqAvailable(1) {
		t.Fatal("seqAvailable(1) = true, want false (seq 1 was trimmed)")
	}
	if !buffer.seqAvailable(2) {
		t.Fatal("seqAvailable(2) = false, want true")
	}
	if !buffer.seqAvailable(3) {
		t.Fatal("seqAvailable(3) = false, want true")
	}
}

func TestRelayBufferStartSeqForDelayIsInitialJoinOffset(t *testing.T) {
	buffer := newRelayBuffer(10*time.Second, 1024)
	base := time.Unix(1700000000, 0)

	buffer.append(base.Add(0*time.Second), []byte("a"))
	buffer.append(base.Add(2*time.Second), []byte("b"))
	buffer.append(base.Add(5*time.Second), []byte("c"))
	buffer.append(base.Add(9*time.Second), []byte("d"))

	// With a 4s target delay, start at the first chunk not older than 4s from the latest.
	if got := buffer.startSeqForDelay(4 * time.Second); got != 3 {
		t.Fatalf("startSeqForDelay(4s) = %d, want 3", got)
	}
	// With zero delay, start at the latest chunk.
	if got := buffer.startSeqForDelay(0); got != 4 {
		t.Fatalf("startSeqForDelay(0) = %d, want 4", got)
	}
}

func TestRelayBufferCompactsAfterManyTrims(t *testing.T) {
	// Small buffer that will trim frequently
	buffer := newRelayBuffer(time.Minute, 10)
	now := time.Unix(1700000000, 0)

	// Add many chunks to trigger multiple trims and compaction
	for i := 0; i < 100; i++ {
		buffer.append(now.Add(time.Duration(i)*time.Millisecond), []byte("data"))
	}

	// After many trims, buffer should have compacted
	// trimCount should be reset to 0 after compaction
	if buffer.trimCount > len(buffer.chunks) {
		t.Fatalf("buffer.trimCount = %d, should be <= len(chunks) = %d after compaction",
			buffer.trimCount, len(buffer.chunks))
	}

	// Buffer should still function correctly
	if !buffer.hasData() {
		t.Fatal("buffer.hasData() = false after compaction")
	}

	// Verify we can still read chunks
	chunk, ok := buffer.chunkAtOrAfter(buffer.chunks[0].seq)
	if !ok {
		t.Fatal("buffer.chunkAtOrAfter() = false after compaction")
	}
	if string(chunk.data) != "data" {
		t.Fatalf("chunk.data = %q, want %q", string(chunk.data), "data")
	}
}
