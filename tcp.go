package streamdeck

import (
	"net"
)

type TCPClient struct {
	conn net.Conn
}

func (t *TCPClient) Close() error {
	return t.conn.Close()
}

func (t *TCPClient) SendFeatureReport(payload []byte) (int, error) {
	const packetSize = 1024

	// Create a buffer of 1024 bytes
	buffer := make([]byte, packetSize)

	// Copy the payload into the buffer
	copy(buffer, payload)

	// Send the entire 1024-byte buffer, regardless of payload length
	return t.conn.Write(buffer)
}

func (t *TCPClient) Write(data []byte) (int, error) {
	return t.conn.Write(data)
}

func (t *TCPClient) Read(data []byte) (int, error) {
	return t.conn.Read(data)
}
