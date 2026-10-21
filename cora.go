package streamdeck

// Cora is the framed TCP protocol used by the Elgato Stream Deck Network Dock
// (and newer Stream Deck Studio firmware). Every message is a 16-byte header
// followed by a payload:
//
//	0..3   magic 43 93 8a 41
//	4..5   flags (LE)
//	6      HID operation (0 = write, 1 = send feature report, 2 = get feature report)
//	8..11  message id (LE)
//	12..15 payload length (LE)
//
// Messages flagged "verbatim" carry plain USB HID reports for the attached
// Stream Deck, so the regular device definitions can be used unchanged.
//
// Reference: https://github.com/Julusian/node-elgato-stream-deck (packages/tcp)

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image/color"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

var coraMagic = []byte{0x43, 0x93, 0x8a, 0x41}

const (
	coraHeaderSize = 16

	coraFlagVerbatim = 0x8000 // Payload is for/from the attached HID device
	coraFlagReqAck   = 0x4000 // Ask the unit to acknowledge a message
	coraFlagAckNak   = 0x0200 // Reply to a keepalive / acknowledgement from the unit

	coraOpWrite         = 0x00
	coraOpSendReport    = 0x01
	coraOpGetReport     = 0x02
	coraKeepaliveWindow = 5 * time.Second // Connection is considered dead after this long without data

	// Writes are acknowledged by the unit, and at most this many may be unacknowledged.
	// Without this, image data can be sent faster than the dock handles it; the TCP
	// buffers fill up, keepalive replies get stuck behind the image data, and the dock drops the connection.
	coraMaxWritesInFlight = 8
	coraWriteAckTimeout   = 5 * time.Second

	networkDockProductID = 0xffff // Product id the dock reports for itself (not a real USB id)
)

var errCoraClosed = errors.New("cora connection closed")

// errCoraWritesIgnored: the attached Stream Deck answers requests but ignores writes.
// Seen with Network Dock firmware 1.01.013 on every second connection; reconnecting fixes it.
var errCoraWritesIgnored = errors.New("Stream Deck on Network Dock ignores writes")

type coraMessage struct {
	flags   uint16
	op      byte
	id      uint32
	payload []byte
}

// coraConn is one Cora TCP connection. It answers keepalives itself and
// hands every other message to the inbox channel. It implements
// DeviceInterface so it can be used as a Device transport.
type coraConn struct {
	conn   net.Conn
	reader io.Reader
	inbox  chan coraMessage

	writeMu  sync.Mutex
	nextID   uint32
	inFlight chan struct{} // One entry per write waiting for acknowledgement

	closeOnce sync.Once
	closed    chan struct{}
	err       error // Why the connection closed; read after closed is closed
}

func dialCora(addr string) (*coraConn, error) {
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return nil, err
	}
	return newCoraConn(conn, conn), nil
}

// newCoraConn starts handling a connection. Data is read from reader, which may hold bytes already peeked from conn.
func newCoraConn(conn net.Conn, reader io.Reader) *coraConn {
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}
	c := &coraConn{
		conn:     conn,
		reader:   reader,
		inbox:    make(chan coraMessage, 64),
		inFlight: make(chan struct{}, coraMaxWritesInFlight),
		closed:   make(chan struct{}),
	}
	go c.readLoop()
	return c
}

func (c *coraConn) readLoop() {
	var buf []byte
	tmp := make([]byte, 16384)
	for {
		c.conn.SetReadDeadline(time.Now().Add(coraKeepaliveWindow))
		n, err := c.reader.Read(tmp)
		if err != nil {
			c.closeWithError(err)
			return
		}
		buf = append(buf, tmp[:n]...)

		for {
			// Resync on the magic if needed
			idx := bytes.Index(buf, coraMagic)
			if idx < 0 {
				if len(buf) > 3 {
					buf = buf[len(buf)-3:]
				}
				break
			}
			buf = buf[idx:]
			if len(buf) < coraHeaderSize {
				break
			}
			length := int(binary.LittleEndian.Uint32(buf[12:]))
			if len(buf) < coraHeaderSize+length {
				break
			}
			msg := coraMessage{
				flags:   binary.LittleEndian.Uint16(buf[4:]),
				op:      buf[6],
				id:      binary.LittleEndian.Uint32(buf[8:]),
				payload: append([]byte(nil), buf[coraHeaderSize:coraHeaderSize+length]...),
			}
			buf = buf[coraHeaderSize+length:]

			if len(msg.payload) > 5 && msg.payload[0] == 0x01 && msg.payload[1] == 0x0a {
				// Keepalive: ack with "03 1a <connection no>"
				ack := make([]byte, 32)
				ack[0], ack[1], ack[2] = 0x03, 0x1a, msg.payload[5]
				if err := c.send(coraFlagAckNak, msg.op, msg.id, ack); err != nil {
					c.closeWithError(err)
					return
				}
				continue
			}

			if msg.flags&coraFlagAckNak != 0 {
				// Acknowledgement of one of our writes
				select {
				case <-c.inFlight:
				default:
				}
				continue
			}

			select {
			case c.inbox <- msg:
			case <-c.closed:
				return
			}
		}
	}
}

func (c *coraConn) send(flags uint16, op byte, id uint32, payload []byte) error {
	frame := make([]byte, coraHeaderSize+len(payload))
	copy(frame, coraMagic)
	binary.LittleEndian.PutUint16(frame[4:], flags)
	frame[6] = op
	binary.LittleEndian.PutUint32(frame[8:], id)
	binary.LittleEndian.PutUint32(frame[12:], uint32(len(payload)))
	copy(frame[coraHeaderSize:], payload)

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err := c.conn.Write(frame)
	return err
}

// getReport asks for a feature report and waits for the matching reply.
// toHost=true addresses the unit itself (dock/Studio), false the attached device.
// Only for use before the inbox is consumed by someone else.
func (c *coraConn) getReport(reportID byte, toHost bool, timeout time.Duration) ([]byte, error) {
	var flags uint16
	payload := []byte{reportID}
	if toHost {
		payload = []byte{0x03, reportID}
	} else {
		flags = coraFlagVerbatim
	}
	if err := c.send(flags, coraOpGetReport, atomic.AddUint32(&c.nextID, 1), payload); err != nil {
		return nil, err
	}

	// Replies to the host carry "03 <id>", replies from the attached device "<id>".
	// The dock answers 03 1c with an "01 0b" message.
	match := func(p []byte) bool {
		switch {
		case reportID == 0x1c && toHost:
			return len(p) > 1 && p[0] == 0x01 && p[1] == 0x0b
		case toHost:
			return len(p) > 1 && p[0] == 0x03 && p[1] == reportID
		default:
			return len(p) > 0 && p[0] == reportID
		}
	}

	deadline := time.After(timeout)
	for {
		select {
		case msg := <-c.inbox:
			if match(msg.payload) {
				return msg.payload, nil
			}
		case <-c.closed:
			return nil, c.err
		case <-deadline:
			return nil, errors.New("timeout waiting for report")
		}
	}
}

func (c *coraConn) closeWithError(err error) {
	c.closeOnce.Do(func() {
		c.err = err
		close(c.closed)
		c.conn.Close()
	})
}

// waitForWriteAcks waits until all writes are acknowledged.
func (c *coraConn) waitForWriteAcks(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for len(c.inFlight) > 0 {
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-c.closed:
			return false
		case <-time.After(5 * time.Millisecond):
		}
	}
	return true
}

// DeviceInterface implementation (the transport for an attached Stream Deck):

func (c *coraConn) Close() error {
	c.closeWithError(errCoraClosed)
	return nil
}

func (c *coraConn) Write(data []byte) (int, error) {
	// Wait for room in the window of unacknowledged writes
	timer := time.NewTimer(coraWriteAckTimeout)
	defer timer.Stop()
	select {
	case c.inFlight <- struct{}{}:
	case <-c.closed:
		return 0, c.err
	case <-timer.C:
		err := errors.New("Stream Deck stopped acknowledging writes")
		c.closeWithError(err)
		return 0, err
	}

	if err := c.send(coraFlagVerbatim|coraFlagReqAck, coraOpWrite, atomic.AddUint32(&c.nextID, 1), data); err != nil {
		c.closeWithError(err)
		return 0, err
	}
	return len(data), nil
}

func (c *coraConn) SendFeatureReport(data []byte) (int, error) {
	if err := c.send(coraFlagVerbatim, coraOpSendReport, 0, data); err != nil {
		c.closeWithError(err)
		return 0, err
	}
	return len(data), nil
}

// Read returns the next input report (keepalives are already handled).
func (c *coraConn) Read(data []byte) (int, error) {
	for {
		select {
		case msg := <-c.inbox:
			if len(msg.payload) == 0 || msg.payload[0] != 0x01 {
				continue // Late replies to report requests etc.
			}
			return copy(data, msg.payload), nil
		case <-c.closed:
			if c.err == nil || errors.Is(c.err, errCoraClosed) {
				return 0, io.EOF
			}
			return 0, c.err
		}
	}
}

// dockChildInfo is the "device 2" info the dock reports about the attached Stream Deck.
type dockChildInfo struct {
	vendorID  uint16
	productID uint16
	serial    string
	tcpPort   uint16
}

// parseDockChildInfo parses a 03 1c reply / 01 0b plug event. Returns nil if nothing is attached.
func parseDockChildInfo(p []byte) *dockChildInfo {
	if len(p) < 128 || p[4] != 0x02 {
		return nil
	}
	serial := p[94:125]
	if i := bytes.IndexByte(serial, 0); i >= 0 {
		serial = serial[:i]
	}
	return &dockChildInfo{
		vendorID:  binary.LittleEndian.Uint16(p[26:]),
		productID: binary.LittleEndian.Uint16(p[28:]),
		serial:    string(serial),
		tcpPort:   binary.LittleEndian.Uint16(p[126:]),
	}
}

// openCora finishes opening a Cora connection on the default port: either a
// Network Dock (connects on to the attached Stream Deck) or a unit that is
// itself a Stream Deck (Studio).
func openCora(host string, primary *coraConn) (*Device, error) {
	info, err := primary.getReport(0x80, true, 3*time.Second)
	if err != nil {
		primary.Close()
		return nil, err
	}
	if len(info) < 16 {
		primary.Close()
		return nil, errors.New("short device info reply")
	}
	productID := binary.LittleEndian.Uint16(info[14:])

	if productID != networkDockProductID {
		// The unit is the Stream Deck itself
		serialReply, err := primary.getReport(0x84, true, 3*time.Second)
		if err != nil {
			primary.Close()
			return nil, err
		}
		serial := ""
		if len(serialReply) > 4 {
			end := 4 + int(serialReply[3])
			if end > len(serialReply) {
				end = len(serialReply)
			}
			serial = string(serialReply[4:end])
		}
		d, err := newDeviceForProduct(productID, serial, primary)
		if err != nil {
			primary.Close()
			return nil, err
		}
		go d.eventListener()
		return d, nil
	}

	// Network Dock: find the attached Stream Deck
	reply, err := primary.getReport(0x1c, true, 3*time.Second)
	if err != nil {
		primary.Close()
		return nil, err
	}
	child := parseDockChildInfo(reply)
	if child == nil {
		primary.Close()
		return nil, errors.New("no Stream Deck attached to the Network Dock")
	}
	if child.tcpPort == 0 || child.tcpPort == DefaultTCPPort {
		primary.Close()
		return nil, errors.New("Network Dock reported an invalid port for the attached Stream Deck")
	}

	childConn, err := dialCora(net.JoinHostPort(host, strconv.Itoa(int(child.tcpPort))))
	if err != nil {
		primary.Close()
		return nil, err
	}
	d, err := newDeviceForProduct(child.productID, child.serial, childConn)
	if err != nil {
		childConn.Close()
		primary.Close()
		return nil, err
	}
	d.parent = primary

	// Make sure writes get through (see errCoraWritesIgnored); the panel is cleared by the application anyway
	if d.HasImageCapability() {
		d.WriteColorToButton(0, color.Black)
		if !childConn.waitForWriteAcks(1500 * time.Millisecond) {
			childConn.Close()
			primary.Close()
			return nil, errCoraWritesIgnored
		}
	}

	// Watch the dock: close the device when the module is unplugged/swapped or the dock goes away.
	go func() {
		for {
			select {
			case msg := <-primary.inbox:
				p := msg.payload
				if len(p) > 1 && p[0] == 0x01 && p[1] == 0x0b {
					now := parseDockChildInfo(p)
					if now == nil || now.serial != child.serial || now.tcpPort != child.tcpPort {
						childConn.closeWithError(errors.New("Stream Deck detached from Network Dock"))
						primary.Close()
						return
					}
				}
			case <-primary.closed:
				childConn.closeWithError(errors.New("connection to Network Dock lost"))
				return
			case <-childConn.closed:
				primary.Close()
				return
			}
		}
	}()

	go d.eventListener()
	return d, nil
}

func newDeviceForProduct(productID uint16, serial string, fd DeviceInterface) (*Device, error) {
	for _, devType := range deviceTypes {
		if devType.usbProductID == productID {
			d := &Device{fd: fd, deviceType: devType}
			d.deviceType.serial = serial
			return d, nil
		}
	}
	return nil, fmt.Errorf("unsupported Stream Deck on network, product id 0x%04x", productID)
}
