package connection

import (
	"io"
	"testing"
)

// Data written before Close must still reach the peer, followed by io.EOF.
// The gateway writes the last chunks from the client and then closes its end
// when the client's close message arrives.
func TestBufferedConn_ReadDrainsBufferAfterClose(t *testing.T) {
	const chunks = 10
	chunk := []byte("0123456789")

	for run := 0; run < 200; run++ {
		writerConn, readerConn := NewBufferedConnPair(TCPBufferSize, false)
		for i := 0; i < chunks; i++ {
			if _, err := readerConn.Write(chunk); err != nil {
				t.Fatalf("write: %v", err)
			}
		}
		readerConn.Close()

		got, err := io.ReadAll(writerConn)
		if err != nil {
			t.Fatalf("run %d: read error: %v", run, err)
		}
		if len(got) != chunks*len(chunk) {
			t.Fatalf("run %d: got %d bytes, want %d", run, len(got), chunks*len(chunk))
		}
	}
}
