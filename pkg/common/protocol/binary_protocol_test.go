package protocol

import (
	"bytes"
	"encoding/base64"
	"reflect"
	"testing"
)

const (
	testConnID = "d115k314nsj2he328ae0" // Test connection ID used in multiple test cases
)

func TestBinaryProtocol(t *testing.T) {
	t.Run("Header Packing", func(t *testing.T) {
		msg := PackBinaryMessage(BinaryMsgTypeData, []byte("test"))

		if msg[0] != BinaryProtocolVersion {
			t.Errorf("Expected version %d, got %d", BinaryProtocolVersion, msg[0])
		}

		if msg[1] != BinaryMsgTypeData {
			t.Errorf("Expected type %d, got %d", BinaryMsgTypeData, msg[1])
		}

		if !bytes.Equal(msg[2:], []byte("test")) {
			t.Error("Data mismatch")
		}
	})

	t.Run("Header Unpacking", func(t *testing.T) {
		msg := []byte{BinaryProtocolVersion, BinaryMsgTypeConnect, 'h', 'e', 'l', 'l', 'o'}

		version, msgType, data, err := UnpackBinaryHeader(msg)
		if err != nil {
			t.Fatal(err)
		}

		if version != BinaryProtocolVersion {
			t.Errorf("Version mismatch: %d", version)
		}

		if msgType != BinaryMsgTypeConnect {
			t.Errorf("Type mismatch: %d", msgType)
		}

		if string(data) != "hello" {
			t.Errorf("Data mismatch: %s", data)
		}
	})
}

func TestDataMessage(t *testing.T) {
	tests := []struct {
		name   string
		connID string
		data   []byte
	}{
		{
			name:   "normal message",
			connID: testConnID, // 20 字符的 xid
			data:   []byte("Hello, World!"),
		},
		{
			name:   "empty data",
			connID: "d115k314nsj2he328ae1",
			data:   []byte{},
		},
		{
			name:   "binary data",
			connID: "d115k314nsj2he328ae2",
			data:   []byte{0x00, 0xFF, 0x42, 0xCA, 0xFE},
		},
		{
			name:   "large data",
			connID: "d115k314nsj2he328ae3",
			data:   bytes.Repeat([]byte("X"), 65536),
		},
		{
			name:   "short connID",
			connID: "short-id",
			data:   []byte("test"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 打包
			packed := PackDataMessage(tt.connID, tt.data)

			// 验证是二进制消息
			if !IsBinaryMessage(packed) {
				t.Error("Expected binary message")
			}

			// 解包头部
			_, msgType, payload, err := UnpackBinaryHeader(packed)
			if err != nil {
				t.Fatal(err)
			}

			if msgType != BinaryMsgTypeData {
				t.Errorf("Wrong message type: %d", msgType)
			}

			// 解包数据消息
			connID, data, err := UnpackDataMessage(payload)
			if err != nil {
				t.Fatal(err)
			}

			// 验证 connID
			expectedConnID := tt.connID
			if len(expectedConnID) > ConnIDSize {
				expectedConnID = expectedConnID[:ConnIDSize]
			}
			if connID != expectedConnID {
				t.Errorf("ConnID mismatch: got %q, want %q", connID, expectedConnID)
			}

			// 验证数据
			if !bytes.Equal(data, tt.data) {
				t.Errorf("Data mismatch: got %d bytes, want %d bytes", len(data), len(tt.data))
			}
		})
	}
}

func TestConnectMessage(t *testing.T) {
	connID := testConnID
	network := "tcp"
	address := "example.com:8080"

	// 打包
	packed := PackConnectMessage(connID, network, address)

	// 验证是二进制消息
	if !IsBinaryMessage(packed) {
		t.Error("Expected binary message")
	}

	// 解包
	_, msgType, payload, _ := UnpackBinaryHeader(packed)
	if msgType != BinaryMsgTypeConnect {
		t.Errorf("Wrong message type: %d", msgType)
	}

	unpackedConnID, unpackedNetwork, unpackedAddress, err := UnpackConnectMessage(payload)
	if err != nil {
		t.Fatal(err)
	}

	if unpackedConnID != connID {
		t.Errorf("ConnID mismatch: %q != %q", unpackedConnID, connID)
	}

	if unpackedNetwork != network {
		t.Errorf("Network mismatch: %q != %q", unpackedNetwork, network)
	}

	if unpackedAddress != address {
		t.Errorf("Address mismatch: %q != %q", unpackedAddress, address)
	}
}

func TestConnectResponseMessage(t *testing.T) {
	tests := []struct {
		name     string
		connID   string
		success  bool
		errorMsg string
	}{
		{"success", testConnID, true, ""},
		{"failure", "d115k314nsj2he328ae1", false, "connection refused"},
		{"long error", "d115k314nsj2he328ae2", false, "Very long error message that describes what went wrong in detail"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 打包
			packed := PackConnectResponseMessage(tt.connID, tt.success, tt.errorMsg)

			// 解包
			_, msgType, payload, _ := UnpackBinaryHeader(packed)
			if msgType != BinaryMsgTypeConnectResponse {
				t.Errorf("Wrong message type: %d", msgType)
			}

			connID, success, errorMsg, err := UnpackConnectResponseMessage(payload)
			if err != nil {
				t.Fatal(err)
			}

			if connID != tt.connID {
				t.Errorf("ConnID mismatch: %q != %q", connID, tt.connID)
			}

			if success != tt.success {
				t.Errorf("Success mismatch: %v != %v", success, tt.success)
			}

			if errorMsg != tt.errorMsg {
				t.Errorf("Error message mismatch: %q != %q", errorMsg, tt.errorMsg)
			}
		})
	}
}

func TestCloseMessage(t *testing.T) {
	connID := testConnID

	// 打包
	packed := PackCloseMessage(connID)

	// 验证是二进制消息
	if !IsBinaryMessage(packed) {
		t.Error("Expected binary message")
	}

	// 解包
	_, msgType, payload, _ := UnpackBinaryHeader(packed)
	if msgType != BinaryMsgTypeClose {
		t.Errorf("Wrong message type: %d", msgType)
	}

	unpackedConnID, err := UnpackCloseMessage(payload)
	if err != nil {
		t.Fatal(err)
	}

	if unpackedConnID != connID {
		t.Errorf("ConnID mismatch: %q != %q", unpackedConnID, connID)
	}
}

func TestPortForwardMessage(t *testing.T) {
	clientID := "test-client-123"
	ports := []PortConfig{
		{RemotePort: 8080, LocalPort: 8080, LocalHost: "localhost", Protocol: "tcp"},
		{RemotePort: 8081, LocalPort: 8081, LocalHost: "127.0.0.1", Protocol: "tcp"},
		{RemotePort: 9000, LocalPort: 9090, LocalHost: "localhost", Protocol: "udp"},
	}

	// 打包
	packed := PackPortForwardMessage(clientID, ports)

	// 解包
	_, msgType, payload, _ := UnpackBinaryHeader(packed)
	if msgType != BinaryMsgTypePortForward {
		t.Errorf("Wrong message type: %d", msgType)
	}

	unpackedClientID, unpackedPorts, err := UnpackPortForwardMessage(payload)
	if err != nil {
		t.Fatal(err)
	}

	if unpackedClientID != clientID {
		t.Errorf("ClientID mismatch: %q != %q", unpackedClientID, clientID)
	}

	if !reflect.DeepEqual(unpackedPorts, ports) {
		t.Errorf("Ports mismatch: %v != %v", unpackedPorts, ports)
	}
}

func TestPortForwardResponseMessage(t *testing.T) {
	success := true
	errorMsg := ""
	statuses := []PortForwardStatus{
		{Port: 8080, Success: true},
		{Port: 8081, Success: false},
		{Port: 9000, Success: true},
	}

	// 打包
	packed := PackPortForwardResponseMessage(success, errorMsg, statuses)

	// 解包
	_, msgType, payload, _ := UnpackBinaryHeader(packed)
	if msgType != BinaryMsgTypePortForwardResp {
		t.Errorf("Wrong message type: %d", msgType)
	}

	unpackedSuccess, unpackedErrorMsg, unpackedStatuses, err := UnpackPortForwardResponseMessage(payload)
	if err != nil {
		t.Fatal(err)
	}

	if unpackedSuccess != success {
		t.Errorf("Success mismatch: %v != %v", unpackedSuccess, success)
	}

	if unpackedErrorMsg != errorMsg {
		t.Errorf("Error message mismatch: %q != %q", unpackedErrorMsg, errorMsg)
	}

	if !reflect.DeepEqual(unpackedStatuses, statuses) {
		t.Errorf("Statuses mismatch: %v != %v", unpackedStatuses, statuses)
	}
}

// BenchmarkBinaryVsBase64 对比二进制协议和 base64 编码的性能
func BenchmarkBinaryVsBase64(b *testing.B) {
	connID := testConnID
	data := bytes.Repeat([]byte("A"), 1024) // 1KB 数据

	b.Run("Binary Protocol", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			// 打包
			packed := PackDataMessage(connID, data)

			// 解包
			_, _, payload, _ := UnpackBinaryHeader(packed)
			UnpackDataMessage(payload)
		}
		b.ReportAllocs()
	})

	b.Run("Base64 Encoding", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			// 基准测试：base64 编码/解码
			encoded := base64.StdEncoding.EncodeToString(data)
			base64.StdEncoding.DecodeString(encoded)
		}
		b.ReportAllocs()
	})
}

func BenchmarkMessageTypes(b *testing.B) {
	connID := testConnID
	data := bytes.Repeat([]byte("X"), 4096) // 4KB

	benchmarks := []struct {
		name string
		fn   func()
	}{
		{
			"DataMessage",
			func() {
				packed := PackDataMessage(connID, data)
				_, _, payload, _ := UnpackBinaryHeader(packed)
				UnpackDataMessage(payload)
			},
		},
		{
			"ConnectMessage",
			func() {
				packed := PackConnectMessage(connID, "tcp", "example.com:8080")
				_, _, payload, _ := UnpackBinaryHeader(packed)
				UnpackConnectMessage(payload)
			},
		},
		{
			"CloseMessage",
			func() {
				packed := PackCloseMessage(connID)
				_, _, payload, _ := UnpackBinaryHeader(packed)
				UnpackCloseMessage(payload)
			},
		},
	}

	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				bm.fn()
			}
			b.ReportAllocs()
		})
	}
}

func TestBinaryProtocolDataMessage(t *testing.T) {
	// Test case for verifying data message handling with []byte
	testData := []byte("Hello, World! This is test data with special chars: 你好世界 🌍")
	connID := "test-conn-123"

	// Pack the data message
	packed := PackDataMessage(connID, testData)

	// First unpack the header to verify message type
	version, msgType, msgData, err := UnpackBinaryHeader(packed)
	if err != nil {
		t.Fatalf("Failed to unpack header: %v", err)
	}

	if version != BinaryProtocolVersion {
		t.Errorf("Expected version %d, got %d", BinaryProtocolVersion, version)
	}

	if msgType != BinaryMsgTypeData {
		t.Errorf("Expected message type %d, got %d", BinaryMsgTypeData, msgType)
	}

	// Unpack the data message
	unpackedConnID, unpackedData, err := UnpackDataMessage(msgData)
	if err != nil {
		t.Fatalf("Failed to unpack data message: %v", err)
	}

	// Verify connection ID
	if unpackedConnID != connID {
		t.Errorf("Expected conn ID %s, got %s", connID, unpackedConnID)
	}

	// Verify data
	if !bytes.Equal(unpackedData, testData) {
		t.Errorf("Data mismatch. Expected: %v, Got: %v", testData, unpackedData)
	}

	// Test with empty data
	emptyData := []byte{}
	packed = PackDataMessage(connID, emptyData)

	_, _, msgData, err = UnpackBinaryHeader(packed)
	if err != nil {
		t.Fatalf("Failed to unpack empty data header: %v", err)
	}

	_, unpackedData, err = UnpackDataMessage(msgData)
	if err != nil {
		t.Fatalf("Failed to unpack empty data message: %v", err)
	}

	if len(unpackedData) != 0 {
		t.Errorf("Expected empty data, got %v", unpackedData)
	}

	// Test with large data
	largeData := make([]byte, 65536) // 64KB
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	packed = PackDataMessage(connID, largeData)

	_, _, msgData, err = UnpackBinaryHeader(packed)
	if err != nil {
		t.Fatalf("Failed to unpack large data header: %v", err)
	}

	_, unpackedData, err = UnpackDataMessage(msgData)
	if err != nil {
		t.Fatalf("Failed to unpack large data message: %v", err)
	}

	if !bytes.Equal(unpackedData, largeData) {
		t.Errorf("Large data mismatch. Expected length: %d, Got length: %d", len(largeData), len(unpackedData))
	}
}

func TestErrorMessage(t *testing.T) {
	tests := []struct {
		name     string
		errorMsg string
	}{
		{
			name:     "simple error",
			errorMsg: "connection failed",
		},
		{
			name:     "empty error",
			errorMsg: "",
		},
		{
			name:     "unicode error",
			errorMsg: "连接失败: 网络不可达 🚫",
		},
		{
			name:     "long error message",
			errorMsg: "This is a very long error message that describes in detail what went wrong during the connection process. It contains multiple sentences and explains the root cause of the failure.",
		},
		{
			name:     "error with special characters",
			errorMsg: `Error: "Connection refused" - Host unreachable (Code: 123) @2023-12-01`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Pack error message
			packed := PackErrorMessage(tt.errorMsg)

			// Verify it's a binary message
			if !IsBinaryMessage(packed) {
				t.Error("Expected binary message")
			}

			// Unpack header
			version, msgType, payload, err := UnpackBinaryHeader(packed)
			if err != nil {
				t.Fatalf("Failed to unpack header: %v", err)
			}

			if version != BinaryProtocolVersion {
				t.Errorf("Expected version %d, got %d", BinaryProtocolVersion, version)
			}

			if msgType != BinaryMsgTypeError {
				t.Errorf("Expected message type %d, got %d", BinaryMsgTypeError, msgType)
			}

			// Unpack error message
			unpackedErrorMsg, err := UnpackErrorMessage(payload)
			if err != nil {
				t.Fatalf("Failed to unpack error message: %v", err)
			}

			// Verify error message
			if unpackedErrorMsg != tt.errorMsg {
				t.Errorf("Error message mismatch: got %q, want %q", unpackedErrorMsg, tt.errorMsg)
			}
		})
	}
}

func TestErrorMessageEdgeCases(t *testing.T) {
	t.Run("invalid error message data", func(t *testing.T) {
		// Test with data too short
		_, err := UnpackErrorMessage([]byte{})
		if err == nil {
			t.Error("Expected error for empty data")
		}

		_, err = UnpackErrorMessage([]byte{0x00})
		if err == nil {
			t.Error("Expected error for data too short")
		}

		// Test with invalid length
		invalidData := []byte{0x00, 0xFF} // Length 255 but no data
		_, err = UnpackErrorMessage(invalidData)
		if err == nil {
			t.Error("Expected error for invalid length")
		}
	})

	t.Run("maximum length error message", func(t *testing.T) {
		// Test with maximum possible error message length (65535 bytes)
		maxErrorMsg := string(bytes.Repeat([]byte("E"), 65535))

		packed := PackErrorMessage(maxErrorMsg)
		_, _, payload, err := UnpackBinaryHeader(packed)
		if err != nil {
			t.Fatalf("Failed to unpack header: %v", err)
		}

		unpackedErrorMsg, err := UnpackErrorMessage(payload)
		if err != nil {
			t.Fatalf("Failed to unpack max length error message: %v", err)
		}

		if unpackedErrorMsg != maxErrorMsg {
			t.Errorf("Max length error message mismatch: got length %d, want length %d", len(unpackedErrorMsg), len(maxErrorMsg))
		}
	})
}
