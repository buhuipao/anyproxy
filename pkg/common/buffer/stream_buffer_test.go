package buffer

import (
	"strings"
	"testing"

	"github.com/buhuipao/anyproxy/pkg/common/protocol"
)

func TestPrependUDPDataHeaderLongTarget(t *testing.T) {
	targets := []string{
		"ec2-54-123-45-67.compute-1.amazonaws.com:3478",
		// SOCKS5 and TUIC carry domains of up to 255 bytes
		strings.Repeat("a", 255) + ":65535",
	}
	for _, target := range targets {
		sb := AcquireLarge()
		sb.SetData([]byte("payload"))
		if err := protocol.PrependUDPDataHeaderToBuffer(sb, "conn-id-0123456789ab", target); err != nil {
			t.Fatalf("target length %d: %v", len(target), err)
		}

		_, msgType, payload, err := protocol.UnpackBinaryHeader(sb.Full())
		if err != nil || msgType != protocol.BinaryMsgTypeUDPData {
			t.Fatalf("UnpackBinaryHeader() type = %d, err = %v", msgType, err)
		}
		_, gotTarget, data, err := protocol.UnpackUDPDataMessage(payload)
		if err != nil || gotTarget != target || string(data) != "payload" {
			t.Errorf("UnpackUDPDataMessage() = %q, %q, %v", gotTarget, data, err)
		}
		sb.Release()
	}
}
