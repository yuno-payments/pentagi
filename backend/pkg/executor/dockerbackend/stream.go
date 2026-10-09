package dockerbackend

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strings"

	"pentagi/pkg/executor"

	"github.com/moby/moby/client"
)

// ttyStream wraps a TTY exec: the hijacked stream carries raw, unmultiplexed
// bytes (stdout and stderr merged by the pty), so Stdout returns the reader
// directly and Stderr is empty — matching the pre-seam terminal read path.
type ttyStream struct {
	resp  *client.HijackedResponse
	stdin io.WriteCloser
}

func newTTYStream(resp client.HijackedResponse, attachStdin bool) executor.ExecStream {
	s := &ttyStream{resp: &resp}
	if attachStdin && resp.Conn != nil {
		s.stdin = resp.Conn
	}
	return s
}

func (s *ttyStream) Stdout() io.Reader     { return s.resp.Reader }
func (s *ttyStream) Stderr() io.Reader     { return bytes.NewReader(nil) }
func (s *ttyStream) Stdin() io.WriteCloser { return s.stdin }
func (s *ttyStream) Close() error          { s.resp.Close(); return nil }

// muxStream wraps a non-TTY exec: the hijacked stream is Docker's multiplexed
// frame format (8-byte header: stream id + big-endian size). A pump demuxes it
// live, forwarding stdout to a pipe and discarding stderr, and surfaces a
// daemon systemerr frame as a read error. This is the streaming twin of
// pkg/docker.demuxExecStdout (which does the same framing as a bounded batch
// read for directory listings); the framing constants must stay in sync.
type muxStream struct {
	resp  *client.HijackedResponse
	pr    *io.PipeReader
	stdin io.WriteCloser
}

func newMuxStream(resp client.HijackedResponse, attachStdin bool) executor.ExecStream {
	pr, pw := io.Pipe()
	s := &muxStream{resp: &resp, pr: pr}
	if attachStdin && resp.Conn != nil {
		s.stdin = resp.Conn
	}
	go pumpStdout(resp.Reader, pw)
	return s
}

func (s *muxStream) Stdout() io.Reader     { return s.pr }
func (s *muxStream) Stderr() io.Reader     { return bytes.NewReader(nil) }
func (s *muxStream) Stdin() io.WriteCloser { return s.stdin }

func (s *muxStream) Close() error {
	_ = s.pr.CloseWithError(io.ErrClosedPipe)
	s.resp.Close()
	return nil
}

func pumpStdout(r io.Reader, pw *io.PipeWriter) {
	header := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, header); err != nil {
			if err == io.EOF {
				pw.Close()
				return
			}
			pw.CloseWithError(fmt.Errorf("truncated exec stream: %w", err))
			return
		}
		size := int64(binary.BigEndian.Uint32(header[4:8]))
		if size == 0 {
			continue
		}
		switch header[0] {
		case 1: // stdout
			if _, err := io.CopyN(pw, r, size); err != nil {
				pw.CloseWithError(err)
				return
			}
		case 3: // systemerr — a daemon-level error injected mid-stream
			var msg bytes.Buffer
			_, _ = io.CopyN(&msg, r, size)
			pw.CloseWithError(fmt.Errorf("docker exec systemerr: %s", strings.TrimSpace(msg.String())))
			return
		default: // stderr and anything else — discard
			if _, err := io.CopyN(io.Discard, r, size); err != nil {
				pw.CloseWithError(err)
				return
			}
		}
	}
}
