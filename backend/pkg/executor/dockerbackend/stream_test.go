package dockerbackend

import (
	"bytes"
	"encoding/binary"
	"io"
	"strings"
	"testing"
)

func frame(stream byte, payload []byte) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:8], uint32(len(payload)))
	return append(h, payload...)
}

func TestMuxStream_PumpStdout_ForwardsStdoutAndDiscardsStderr(t *testing.T) {
	var in bytes.Buffer
	in.Write(frame(1, []byte("hello ")))
	in.Write(frame(2, []byte("STDERR-NOISE")))
	in.Write(frame(1, []byte("world")))

	pr, pw := io.Pipe()
	go pumpStdout(&in, pw)

	got, err := io.ReadAll(pr)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "hello world" {
		t.Fatalf("got %q want %q", got, "hello world")
	}
}

func TestMuxStream_PumpStdout_SurfacesSystemErr(t *testing.T) {
	var in bytes.Buffer
	in.Write(frame(1, []byte("partial")))
	in.Write(frame(3, []byte("daemon boom")))

	pr, pw := io.Pipe()
	go pumpStdout(&in, pw)

	_, err := io.ReadAll(pr)
	if err == nil || !strings.Contains(err.Error(), "systemerr") {
		t.Fatalf("want systemerr error, got %v", err)
	}
}

func TestMuxStream_PumpStdout_TruncatedHeaderIsError(t *testing.T) {
	// 3 bytes is less than the 8-byte frame header: a mid-frame cut must error,
	// not silently end, so the caller never treats a truncated read as complete.
	pr, pw := io.Pipe()
	go pumpStdout(bytes.NewReader([]byte{1, 0, 0}), pw)

	if _, err := io.ReadAll(pr); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("want truncated error, got %v", err)
	}
}
