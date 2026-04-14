package server

import (
	"sort"
	"time"
)

type relayChunk struct {
	seq        uint64
	receivedAt time.Time
	data       []byte
}

type relayBuffer struct {
	chunks     []relayChunk
	nextSeq    uint64
	maxAge     time.Duration
	maxBytes   int
	totalBytes int
}

func newRelayBuffer(maxAge time.Duration, maxBytes int) relayBuffer {
	return relayBuffer{
		chunks:   make([]relayChunk, 0),
		nextSeq:  1,
		maxAge:   maxAge,
		maxBytes: maxBytes,
	}
}

func (b *relayBuffer) append(now time.Time, data []byte) {
	if len(data) == 0 {
		return
	}

	chunkData := append([]byte(nil), data...)
	b.chunks = append(b.chunks, relayChunk{
		seq:        b.nextSeq,
		receivedAt: now,
		data:       chunkData,
	})
	b.nextSeq++
	b.totalBytes += len(chunkData)
	b.trim(now)
}

func (b *relayBuffer) trim(now time.Time) {
	for len(b.chunks) > 1 {
		tooOld := b.maxAge > 0 && now.Sub(b.chunks[0].receivedAt) > b.maxAge
		tooLarge := b.maxBytes > 0 && b.totalBytes > b.maxBytes
		if !tooOld && !tooLarge {
			return
		}

		b.totalBytes -= len(b.chunks[0].data)
		b.chunks = b.chunks[1:]
	}
}

func (b *relayBuffer) hasData() bool {
	return len(b.chunks) > 0
}

func (b *relayBuffer) coverage() time.Duration {
	if len(b.chunks) < 2 {
		return 0
	}

	return b.chunks[len(b.chunks)-1].receivedAt.Sub(b.chunks[0].receivedAt)
}

func (b *relayBuffer) startSeqForDelay(delay time.Duration) uint64 {
	if len(b.chunks) == 0 {
		return 0
	}

	if delay <= 0 {
		return b.chunks[len(b.chunks)-1].seq
	}

	latest := b.chunks[len(b.chunks)-1].receivedAt
	cutoff := latest.Add(-delay)
	for _, chunk := range b.chunks {
		if !chunk.receivedAt.Before(cutoff) {
			return chunk.seq
		}
	}

	return b.chunks[0].seq
}

func (b *relayBuffer) chunkAtOrAfter(seq uint64) (relayChunk, bool) {
	if len(b.chunks) == 0 {
		return relayChunk{}, false
	}

	if seq <= b.chunks[0].seq {
		return b.chunks[0], true
	}

	index := sort.Search(len(b.chunks), func(i int) bool {
		return b.chunks[i].seq >= seq
	})
	if index >= len(b.chunks) {
		return relayChunk{}, false
	}

	return b.chunks[index], true
}
