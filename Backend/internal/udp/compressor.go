package udp

import (
	"sync"

	"github.com/klauspost/compress/zstd"
)

var zstdEncoderPool = sync.Pool{
	New: func() any {
		enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest))
		return enc
	},
}

func compressZstd(data []byte) []byte {
	enc := zstdEncoderPool.Get().(*zstd.Encoder)
	defer zstdEncoderPool.Put(enc)
	return enc.EncodeAll(data, make([]byte, 0, len(data)/2))
}

// maybeCompress applies compression based on the algorithm hint.
// Returns [0x00][raw] or [0x01][compressed]. Falls back to uncompressed
// if compression doesn't reduce size.
func maybeCompress(data []byte, algo string) []byte {
	switch algo {
	case "zstd":
		compressed := compressZstd(data)
		if len(compressed) < len(data) {
			out := make([]byte, 1+len(compressed))
			out[0] = 0x01
			copy(out[1:], compressed)
			return out
		}
		fallthrough
	default:
		out := make([]byte, 1+len(data))
		out[0] = 0x00
		copy(out[1:], data)
		return out
	}
}
