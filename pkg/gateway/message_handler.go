package gateway

import (
	"fmt"

	"github.com/buhuipao/anyproxy/pkg/common/buffer"
	"github.com/buhuipao/anyproxy/pkg/common/protocol"
)

// readNextMessage reads the next message, using binary format completely
func (c *ClientConn) readNextMessage() (map[string]interface{}, error) {
	msgData, err := c.Conn.ReadMessage()
	if err != nil {
		return nil, err
	}

	if !protocol.IsBinaryMessage(msgData) {
		return nil, fmt.Errorf("received non-binary message")
	}

	version, msgType, data, err := protocol.UnpackBinaryHeader(msgData)
	if err != nil {
		return nil, err
	}

	_ = version

	return c.parseGatewayMessage(msgType, data)
}

// parseGatewayMessage parses messages received by gateway
func (c *ClientConn) parseGatewayMessage(msgType byte, data []byte) (map[string]interface{}, error) {
	switch msgType {
	case protocol.BinaryMsgTypeData:
		connID, payload, err := protocol.UnpackDataMessage(data)
		if err != nil {
			return nil, err
		}

		return map[string]interface{}{
			"type":       protocol.MsgTypeData,
			"id":         connID,
			"data":       payload,
			"_optimized": true,
		}, nil

	case protocol.BinaryMsgTypeConnectResponse:
		connID, success, errorMsg, err := protocol.UnpackConnectResponseMessage(data)
		if err != nil {
			return nil, err
		}

		return map[string]interface{}{
			"type":    protocol.MsgTypeConnectResponse,
			"id":      connID,
			"success": success,
			"error":   errorMsg,
		}, nil

	case protocol.BinaryMsgTypeClose:
		connID, err := protocol.UnpackCloseMessage(data)
		if err != nil {
			return nil, err
		}

		return map[string]interface{}{
			"type": protocol.MsgTypeClose,
			"id":   connID,
		}, nil

	case protocol.BinaryMsgTypePortForward:
		clientID, ports, err := protocol.UnpackPortForwardMessage(data)
		if err != nil {
			return nil, err
		}

		openPorts := make([]interface{}, len(ports))
		for i, port := range ports {
			openPorts[i] = map[string]interface{}{
				"remote_port": port.RemotePort,
				"local_port":  port.LocalPort,
				"local_host":  port.LocalHost,
				"protocol":    port.Protocol,
			}
		}

		return map[string]interface{}{
			"type":       protocol.MsgTypePortForwardReq,
			"client_id":  clientID,
			"open_ports": openPorts,
		}, nil

	case protocol.BinaryMsgTypeError:
		errorMsg, err := protocol.UnpackErrorMessage(data)
		if err != nil {
			return nil, err
		}

		return map[string]interface{}{
			"type":          protocol.MsgTypeError,
			"error_message": errorMsg,
		}, nil

	case protocol.BinaryMsgTypeHeartbeat:
		clientID, timestamp, err := protocol.UnpackHeartbeatMessage(data)
		if err != nil {
			return nil, err
		}

		return map[string]interface{}{
			"type":      protocol.MsgTypeHeartbeat,
			"client_id": clientID,
			"timestamp": timestamp,
		}, nil

	default:
		return nil, fmt.Errorf("unknown binary message type for gateway: 0x%02x", msgType)
	}
}

// writeConnectMessage sends connection request using binary format
func (c *ClientConn) writeConnectMessage(connID, network, address string) error {
	return c.Conn.WriteMessage(protocol.PackConnectMessage(connID, network, address))
}

// writeCloseMessage sends close message using binary format
func (c *ClientConn) writeCloseMessage(connID string) error {
	return c.Conn.WriteMessage(protocol.PackCloseMessage(connID))
}

// writeDataMessageWithStreamBuffer sends TCP data using StreamBuffer for zero-copy.
// Takes ownership of the StreamBuffer and releases it after writing.
// Priority: StreamBufferWriter (writev) -> NoCopyWriter -> WriteMessage
func (c *ClientConn) writeDataMessageWithStreamBuffer(connID string, sb *buffer.StreamBuffer) error {
	if err := protocol.PrependDataHeader(sb, connID); err != nil {
		sb.Release()
		return err
	}

	return c.smartWriter.WriteStreamBuffer(sb)
}

// initialDatagramMax is the conservative size for QUIC Datagram attempts before
// path MTU discovery completes. QUIC initial packet is ~1252 bytes; after headers
// (~50B) and DATAGRAM frame overhead (~3B), about 1200 bytes are available.
const initialDatagramMax = 1200

// postMTUDatagramMax is the datagram size allowed after MTU discovery succeeds.
// Covers Moonlight/Sunshine video packets (1200B) + protocol overhead (40B) = 1240B.
// Set to 1300 to include all game streaming formats while keeping larger bulk UDP
// (e.g. 1400B stress test packets) on the reliable stream for throughput.
const postMTUDatagramMax = 1300

// writeUDPDataMessageWithStreamBuffer sends UDP data using StreamBuffer for zero-copy.
// Takes ownership of the StreamBuffer and releases it after writing.
//
// Strategy for minimal game-streaming latency:
//   - Small packets (≤ 1200B): always try QUIC Datagram (low-latency, unreliable).
//   - Medium packets (1200-4096B): try datagram ONLY after MTU discovery confirms
//     the path supports larger datagrams. Before that, use reliable stream.
//   - Large packets (> 4096B): always use reliable QUIC stream.
//
// SmartWriter.SendDatagram is non-blocking (dedicated goroutine absorbs QUIC
// queue blocking). Falls back to stream when the datagram queue is full.
func (c *ClientConn) writeUDPDataMessageWithStreamBuffer(connID, targetAddr string, sb *buffer.StreamBuffer) error {
	if err := protocol.PrependUDPDataHeaderToBuffer(sb, connID, targetAddr); err != nil {
		sb.Release()
		return err
	}

	payload := sb.Full()
	maxDG := initialDatagramMax
	if c.smartWriter.MTUReady() {
		maxDG = postMTUDatagramMax
	}

	if len(payload) <= maxDG {
		if sent, err := c.smartWriter.SendDatagram(payload); sent && err == nil {
			sb.Release()
			return nil
		}
	}

	// Datagram skipped or queue full — use reliable stream.
	return c.smartWriter.WriteStreamBuffer(sb)
}
