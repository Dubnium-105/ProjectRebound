package relayruntime

import "time"

type tokenBucket struct {
	rate       float64
	capacity   float64
	tokens     float64
	lastRefill time.Time
}

func newTokenBucket(rate, capacity float64, now time.Time) tokenBucket {
	return tokenBucket{rate: rate, capacity: capacity, tokens: capacity, lastRefill: now}
}

func (b *tokenBucket) Allow(cost float64, now time.Time) bool {
	if now.After(b.lastRefill) {
		b.tokens += now.Sub(b.lastRefill).Seconds() * b.rate
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
		b.lastRefill = now
	}
	if cost > b.tokens {
		return false
	}
	b.tokens -= cost
	return true
}

type replayWindow struct {
	max    uint64
	bitmap uint64
	set    bool
}

func (w *replayWindow) Accept(sequence uint64) bool {
	if !w.set {
		w.max, w.bitmap, w.set = sequence, 1, true
		return true
	}
	if sequence > w.max {
		shift := sequence - w.max
		if shift >= 64 {
			w.bitmap = 0
		} else {
			w.bitmap <<= shift
		}
		w.bitmap |= 1
		w.max = sequence
		return true
	}
	offset := w.max - sequence
	if offset >= 64 || w.bitmap&(uint64(1)<<offset) != 0 {
		return false
	}
	w.bitmap |= uint64(1) << offset
	return true
}
