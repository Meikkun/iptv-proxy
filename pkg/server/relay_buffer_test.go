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

	chunk, ok := buffer.chunkAtOrAfter(1)
	if !ok {
		t.Fatal("buffer.chunkAtOrAfter() = false, want true")
	}

	if string(chunk.data) != "bbb" {
		t.Fatalf("buffer.chunkAtOrAfter() = %q, want %q", string(chunk.data), "bbb")
	}
}
