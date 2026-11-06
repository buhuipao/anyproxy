package client

import (
	"fmt"

	"github.com/buhuipao/anyproxy/pkg/common/buffer"
	"github.com/buhuipao/anyproxy/pkg/common/protocol"
)

// readNextMessage reads the next message, using binary format completely
func (c *Client) readNextMessage() (map[string]interface{}, error) {
	state := c.getConnState()
	if state == nil || state.conn == nil {
		return nil, fmt.Errorf("connection not initialized")
	}

	msgData, err := state.conn.ReadMessage()
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

	return c.parseClientMessage(msgType, data)
}

// parseClientMessage parses messages received by client
func (c *Client) parseClientMessage(msgType byte, data []byte) (map[string]interface{}, error) {
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

	case protocol.BinaryMsgTypeUDPData:
		connID, targetAddr, payload, err := protocol.UnpackUDPDataMessage(data)
		if err != nil {
			return nil, err
		}

		return map[string]interface{}{
			"type":        protocol.MsgTypeData,
			"id":          connID,
			"data":        payload,
			"target_addr": targetAddr,
			"_optimized":  true,
			"_is_udp":     true,
		}, nil

	case protocol.BinaryMsgTypeConnect:
		connID, network, address, err := protocol.UnpackConnectMessage(data)
		if err != nil {
			return nil, err
		}

		return map[string]interface{}{
			"type":    protocol.MsgTypeConnect,
			"id":      connID,
			"network": network,
			"address": address,
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

	case protocol.BinaryMsgTypePortForwardResp:
		success, errorMsg, statuses, err := protocol.UnpackPortForwardResponseMessage(data)
		if err != nil {
			return nil, err
		}

		statusMap := make(map[int]bool)
		for _, status := range statuses {
			statusMap[status.Port] = status.Success
		}

		return map[string]interface{}{
			"type":    "port_forward_response",
			"success": success,
			"error":   errorMsg,
			"ports":   statusMap,
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

	case protocol.BinaryMsgTypeHeartbeatAck:
		timestamp, err := protocol.UnpackHeartbeatAckMessage(data)
		if err != nil {
			return nil, err
		}

		return map[string]interface{}{
			"type":      protocol.MsgTypeHeartbeatAck,
			"timestamp": timestamp,
		}, nil

	default:
		return nil, fmt.Errorf("unknown binary message type for client: 0x%02x", msgType)
	}
}

// writeConnectResponse sends connection response using binary format
func (c *Client) writeConnectResponse(connID string, success bool, errorMsg string) error {
	state := c.getConnState()
	if state == nil || state.conn == nil {
		return fmt.Errorf("connection not initialized")
	}
	return state.conn.WriteMessage(protocol.PackConnectResponseMessage(connID, success, errorMsg))
}

// writeCloseMessage sends close message using binary format
func (c *Client) writeCloseMessage(connID string) error {
	state := c.getConnState()
	if state == nil || state.conn == nil {
		return fmt.Errorf("connection not initialized")
	}
	return state.conn.WriteMessage(protocol.PackCloseMessage(connID))
}

// writeDataMessageWithStreamBuffer sends TCP data using StreamBuffer for zero-copy.
// Takes ownership of the StreamBuffer and releases it after writing.
// Priority: StreamBufferWriter (writev) -> NoCopyWriter -> WriteMessage
func (c *Client) writeDataMessageWithStreamBuffer(connID string, sb *buffer.StreamBuffer) error {
	if err := protocol.PrependDataHeader(sb, connID); err != nil {
		sb.Release()
		return err
	}

	state := c.getConnState()
	if state == nil || state.smartWriter == nil {
		sb.Release()
		return fmt.Errorf("smart writer not initialized")
	}
	return state.smartWriter.WriteStreamBuffer(sb)
}

// MTU-aware datagram thresholds — mirror the gateway-side values so both
// directions apply the same policy. Before path MTU discovery completes only
// small packets (≤1200B) are sent via datagram. After warmup, packets up to
// 1300B qualify; that covers Moonlight/Sunshine video packets (1200B default)
// plus our ~40B BinaryMsgTypeData header while staying inside a single QUIC
// datagram on a typical 1500B Ethernet path.
const (
	clientInitialDatagramMax = 1200
	clientPostMTUDatagramMax = 1300
)

// writeDataMessageDatagram sends UDP payload via QUIC Datagram (unreliable,
// low-latency). It uses the stream only for oversized packets and for
// transports without datagrams (WebSocket, gRPC). This is the
// latency-critical path for game streaming: the reliable QUIC stream has
// congestion control and head-of-line blocking that the underlying UDP flow
// doesn't need and can't tolerate at 100+ Mbps 4K120 bitrates.
//
// When the datagram queue is full we DROP (UDP semantics). Falling back to
// the reliable stream here would interleave ordered+unordered delivery and
// Moonlight's jitter buffer would discard reordered arrivals as loss —
// amplifying apparent packet loss. FEC at the receiver handles real loss
// better than fake loss-via-reordering.
//
// The datagram carries exactly the same BinaryMsgTypeData framing the
// reliable stream uses, so the gateway's handleDatagrams can route it
// through the same handleDataMessage sink. Takes ownership of the
// StreamBuffer and releases it after writing.
func (c *Client) writeDataMessageDatagram(connID string, sb *buffer.StreamBuffer) error {
	if err := protocol.PrependDataHeader(sb, connID); err != nil {
		sb.Release()
		return err
	}

	state := c.getConnState()
	if state == nil || state.smartWriter == nil {
		sb.Release()
		return fmt.Errorf("smart writer not initialized")
	}

	payload := sb.Full()
	maxDG := clientInitialDatagramMax
	if state.smartWriter.MTUReady() {
		maxDG = clientPostMTUDatagramMax
	}

	if len(payload) <= maxDG && state.smartWriter.SupportsDatagram() {
		// A full queue drops the packet. See doc comment above for rationale.
		_, _ = state.smartWriter.SendDatagram(payload)
		sb.Release()
		return nil
	}

	// An oversized packet, or a transport without datagrams (WebSocket,
	// gRPC): the stream is the only option.
	return state.smartWriter.WriteStreamBuffer(sb)
}
