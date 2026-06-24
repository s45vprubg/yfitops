// Package transport is the WebTransport/QUIC edge of the yfitops V2 server.
// It owns connections, frames messages on the wire, and bridges the network to
// the game engine via the game.InboundHandler / game.Broadcaster seams.
//
// Wire framing mirrors web/shared/client.ts exactly: every frame is
//
//	[4-byte big-endian uint32 length][UTF-8 JSON envelope]
//
// over a single bidirectional control stream. The codec lives here as pure
// functions so it can be exercised without a network (design_doc §11 keeps the
// door open for a binary format later; the length prefix is format-agnostic).
package transport

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"

	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// maxFrameLen caps a single decoded frame so a hostile or corrupt length prefix
// cannot make the reader allocate an unbounded buffer. Envelopes are small JSON
// blobs; 1 MiB is generous headroom (largest payload is a full board/lyrics
// sync).
const maxFrameLen = 1 << 20

// frameHeaderLen is the size of the big-endian length prefix, matching the
// 4-byte DataView.setUint32 write in client.ts.
const frameHeaderLen = 4

// encodeFrame wraps a JSON-encoded body in the length-prefixed frame format.
// The prefix is the body length as a big-endian uint32, identical to the
// browser client's `new DataView(frame.buffer).setUint32(0, body.length, false)`.
func encodeFrame(body []byte) []byte {
	frame := make([]byte, frameHeaderLen+len(body))
	binary.BigEndian.PutUint32(frame[:frameHeaderLen], uint32(len(body)))
	copy(frame[frameHeaderLen:], body)
	return frame
}

// frameReader de-frames length-prefixed JSON off an io.Reader. It buffers
// across reads so a frame split over multiple TCP/QUIC segments is reassembled,
// and it drains multiple frames that arrive in one read — mirroring the
// "drain as many complete frames as are buffered" loop in client.ts.readLoop.
type frameReader struct {
	r   io.Reader
	buf []byte // bytes read but not yet consumed as complete frames
	tmp []byte // scratch read buffer
}

func newFrameReader(r io.Reader) *frameReader {
	return &frameReader{r: r, tmp: make([]byte, 4096)}
}

// ReadFrame returns the next complete frame body, blocking and reassembling as
// needed. It returns io.EOF when the underlying stream is exhausted with no
// partial frame pending (a partial frame at EOF surfaces as io.ErrUnexpectedEOF).
func (fr *frameReader) ReadFrame() ([]byte, error) {
	for {
		// First try to satisfy a frame from already-buffered bytes. This handles
		// the multiple-frames-in-one-read case without touching the network.
		if body, ok, err := fr.takeFrame(); err != nil {
			return nil, err
		} else if ok {
			return body, nil
		}

		n, err := fr.r.Read(fr.tmp)
		if n > 0 {
			fr.buf = append(fr.buf, fr.tmp[:n]...)
		}
		if err != nil {
			if err == io.EOF && len(fr.buf) > 0 {
				// Stream ended mid-frame.
				return nil, io.ErrUnexpectedEOF
			}
			return nil, err
		}
	}
}

// takeFrame pulls one complete frame out of the buffer if present. ok is false
// when more bytes are needed.
func (fr *frameReader) takeFrame() (body []byte, ok bool, err error) {
	if len(fr.buf) < frameHeaderLen {
		return nil, false, nil
	}
	n := binary.BigEndian.Uint32(fr.buf[:frameHeaderLen])
	if n > maxFrameLen {
		return nil, false, fmt.Errorf("transport: frame length %d exceeds max %d", n, maxFrameLen)
	}
	total := frameHeaderLen + int(n)
	if len(fr.buf) < total {
		return nil, false, nil
	}
	// Copy the body out so the caller owns it independently of the buffer, then
	// drop the consumed bytes.
	body = make([]byte, n)
	copy(body, fr.buf[frameHeaderLen:total])
	fr.buf = fr.buf[total:]
	return body, true, nil
}

// unmarshalEnvelope decodes a frame body into a ClientEnvelope. Kept as a named
// helper so the read loop reads cleanly and the decode point is unambiguous.
func unmarshalEnvelope(body []byte, env *protocol.ClientEnvelope) error {
	return json.Unmarshal(body, env)
}
