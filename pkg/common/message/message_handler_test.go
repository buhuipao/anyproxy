package message

import (
	"testing"

	"github.com/buhuipao/anyproxy/pkg/common/protocol"
)

// mockMessageConnection 用于测试的 mock 连接
type mockMessageConnection struct {
	readData  []byte
	readErr   error
	writeData []byte
	writeErr  error
}

func (m *mockMessageConnection) WriteMessage(data []byte) error {
	m.writeData = data
	return m.writeErr
}

func (m *mockMessageConnection) ReadMessage() ([]byte, error) {
	return m.readData, m.readErr
}

func (m *mockMessageConnection) Close() error {
	return nil
}

// TestClientMessageHandler_PortForward test the port forward functionality of the client message handler
func TestClientMessageHandler_PortForward(t *testing.T) {
	// create port forward response message
	successMsg := protocol.PackPortForwardResponseMessage(true, "", []protocol.PortForwardStatus{
		{Port: 18080, Success: true},
		{Port: 18081, Success: false},
	})

	mockConn := &mockMessageConnection{
		readData: successMsg,
	}

	handler := NewClientMessageHandler(mockConn)

	// 读取消息
	msg, err := handler.ReadNextMessage()
	if err != nil {
		t.Fatalf("ReadNextMessage failed: %v", err)
	}

	// 验证消息类型
	if msgType, ok := msg["type"].(string); !ok || msgType != "port_forward_response" {
		t.Errorf("Expected message type 'port_forward_response', got '%v'", msg["type"])
	}

	// 验证成功状态
	if success, ok := msg["success"].(bool); !ok || !success {
		t.Errorf("Expected success to be true, got %v", msg["success"])
	}

	// 验证端口状态
	if ports, ok := msg["ports"].(map[int]bool); ok {
		if !ports[18080] {
			t.Error("Expected port 18080 to be successful")
		}
		if ports[18081] {
			t.Error("Expected port 18081 to be unsuccessful")
		}
	} else {
		t.Error("Failed to get ports from message")
	}
}

// TestGatewayMessageHandler_PortForward 测试网关消息处理器的端口转发功能
func TestGatewayMessageHandler_PortForward(t *testing.T) {
	// 创建端口转发请求消息
	ports := []protocol.PortConfig{
		{RemotePort: 18080, LocalPort: 80, LocalHost: "localhost", Protocol: "tcp"},
		{RemotePort: 18081, LocalPort: 81, LocalHost: "localhost", Protocol: "udp"},
	}
	reqMsg := protocol.PackPortForwardMessage("client-123", ports)

	mockConn := &mockMessageConnection{
		readData: reqMsg,
	}

	handler := NewGatewayMessageHandler(mockConn)

	// 读取消息
	msg, err := handler.ReadNextMessage()
	if err != nil {
		t.Fatalf("ReadNextMessage failed: %v", err)
	}

	// 验证消息类型
	if msgType, ok := msg["type"].(string); !ok || msgType != protocol.MsgTypePortForwardReq {
		t.Errorf("Expected message type '%s', got '%v'", protocol.MsgTypePortForwardReq, msg["type"])
	}

	// 验证客户端ID
	if clientID, ok := msg["client_id"].(string); !ok || clientID != "client-123" {
		t.Errorf("Expected client_id 'client-123', got '%v'", msg["client_id"])
	}

	// 验证端口配置
	if openPorts, ok := msg["open_ports"].([]interface{}); ok {
		if len(openPorts) != 2 {
			t.Errorf("Expected 2 open ports, got %d", len(openPorts))
		}

		// 验证第一个端口
		if port0, ok := openPorts[0].(map[string]interface{}); ok {
			if remotePort, ok := port0["remote_port"].(int); !ok || remotePort != 18080 {
				t.Errorf("Expected remote_port 18080, got %v", port0["remote_port"])
			}
			if protocol, ok := port0["protocol"].(string); !ok || protocol != "tcp" {
				t.Errorf("Expected protocol 'tcp', got %v", port0["protocol"])
			}
		}
	} else {
		t.Error("Failed to get open_ports from message")
	}
}

// TestMessageHandler_DataMessage 测试数据消息处理
func TestMessageHandler_DataMessage(t *testing.T) {
	testData := []byte("test data")
	dataMsg := protocol.PackDataMessage("conn-123", testData)

	mockConn := &mockMessageConnection{
		readData: dataMsg,
	}

	// 测试客户端处理器
	clientHandler := NewClientMessageHandler(mockConn)
	msg, err := clientHandler.ReadNextMessage()
	if err != nil {
		t.Fatalf("Client ReadNextMessage failed: %v", err)
	}

	if msgType, ok := msg["type"].(string); !ok || msgType != protocol.MsgTypeData {
		t.Errorf("Expected message type '%s', got '%v'", protocol.MsgTypeData, msg["type"])
	}

	if data, ok := msg["data"].([]byte); !ok || string(data) != "test data" {
		t.Errorf("Expected data 'test data', got '%v'", msg["data"])
	}

	// 测试发送数据消息
	err = clientHandler.WriteDataMessage("conn-456", []byte("response data"))
	if err != nil {
		t.Fatalf("WriteDataMessage failed: %v", err)
	}

	// 验证写入的数据
	if mockConn.writeData == nil {
		t.Error("No data was written")
	}
}

// TestExtendedMessageHandler 测试扩展消息处理器
func TestExtendedMessageHandler(t *testing.T) {
	mockConn := &mockMessageConnection{}

	// 测试客户端扩展处理器
	clientHandler := NewClientExtendedMessageHandler(mockConn)

	// 测试 WriteConnectResponse
	err := clientHandler.WriteConnectResponse("conn-123", true, "")
	if err != nil {
		t.Fatalf("WriteConnectResponse failed: %v", err)
	}

	// 测试网关扩展处理器
	gatewayHandler := NewGatewayExtendedMessageHandler(mockConn)

	// 测试 WriteConnectMessage
	err = gatewayHandler.WriteConnectMessage("conn-456", "tcp", "example.com:80")
	if err != nil {
		t.Fatalf("WriteConnectMessage failed: %v", err)
	}
}
