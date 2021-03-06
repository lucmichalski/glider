package ws

import (
	"errors"
	"io"
	"net"
	"net/textproto"
	"strings"

	"github.com/nadoo/glider/log"
	"github.com/nadoo/glider/pool"
	"github.com/nadoo/glider/proxy"
)

func init() {
	proxy.RegisterServer("ws", NewWSServer)
}

// NewWSServer returns a ws transport server.
func NewWSServer(s string, p proxy.Proxy) (proxy.Server, error) {
	transport := strings.Split(s, ",")

	w, err := NewWS(transport[0], nil, p)
	if err != nil {
		return nil, err
	}

	if len(transport) > 1 {
		w.server, err = proxy.ServerFromURL(transport[1], p)
		if err != nil {
			return nil, err
		}
	}

	return w, nil
}

// ListenAndServe listens on server's addr and serves connections.
func (s *WS) ListenAndServe() {
	l, err := net.Listen("tcp", s.addr)
	if err != nil {
		log.F("[ws] failed to listen on %s: %v", s.addr, err)
		return
	}
	defer l.Close()

	log.F("[ws] listening TCP on %s", s.addr)

	for {
		c, err := l.Accept()
		if err != nil {
			log.F("[ws] failed to accept: %v", err)
			continue
		}

		go s.Serve(c)
	}
}

// Serve serves a connection.
func (s *WS) Serve(cc net.Conn) {
	c, err := s.NewServerConn(cc)
	if err != nil {
		log.F("[ws] handshake error: %s", err)
		return
	}

	if s.server != nil {
		s.server.Serve(c)
		return
	}

	defer c.Close()

	rc, dialer, err := s.proxy.Dial("tcp", "")
	if err != nil {
		log.F("[ws] %s <-> %s via %s, error in dial: %v", c.RemoteAddr(), s.addr, dialer.Addr(), err)
		s.proxy.Record(dialer, false)
		return
	}

	defer rc.Close()

	log.F("[ws] %s <-> %s", c.RemoteAddr(), dialer.Addr())

	if err = proxy.Relay(c, rc); err != nil {
		log.F("[ws] %s <-> %s, relay error: %v", c.RemoteAddr(), dialer.Addr(), err)
		// record remote conn failure only
		if !strings.Contains(err.Error(), s.addr) {
			s.proxy.Record(dialer, false)
		}
	}

}

// ServerConn is a connection to ws client.
type ServerConn struct {
	net.Conn
	reader io.Reader
	writer io.Writer
}

// NewServerConn creates a new ws server connection.
func (s *WS) NewServerConn(rc net.Conn) (*ServerConn, error) {
	sc := &ServerConn{Conn: rc}
	err := sc.Handshake(s.host, s.path)
	return sc, err
}

// Handshake handshakes with the client.
func (c *ServerConn) Handshake(host, path string) error {
	br := pool.GetBufReader(c.Conn)
	defer pool.PutBufReader(br)

	tpr := textproto.NewReader(br)
	line, err := tpr.ReadLine()
	if err != nil {
		return err
	}

	_, path, _, ok := parseFirstLine(line)
	if !ok || path != path {
		return errors.New("[ws] error in ws handshake parseFirstLine: " + line)
	}

	reqHeader, err := tpr.ReadMIMEHeader()
	if err != nil {
		return err
	}

	// NOTE: in server mode, we do not validate the request Host now, check it.
	// if reqHeader.Get("Host") != host {
	// 	return fmt.Errorf("[ws] got wrong host: %s, expected: %s", reqHeader.Get("Host"), host)
	// }

	clientKey := reqHeader.Get("Sec-WebSocket-Key")
	serverKey := computeServerKey(clientKey)

	buf := pool.GetBytesBuffer()
	defer pool.PutBytesBuffer(buf)

	buf.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
	buf.WriteString("Upgrade: websocket\r\n")
	buf.WriteString("Connection: Upgrade\r\n")
	buf.WriteString("Sec-WebSocket-Accept: " + serverKey + "\r\n")
	buf.WriteString("Sec-WebSocket-Protocol: binary\r\n")
	buf.WriteString(("\r\n"))

	_, err = c.Conn.Write(buf.Bytes())
	return err
}

func (c *ServerConn) Write(b []byte) (n int, err error) {
	if c.writer == nil {
		c.writer = FrameWriter(c.Conn, true)
	}
	return c.writer.Write(b)
}

func (c *ServerConn) Read(b []byte) (n int, err error) {
	if c.reader == nil {
		c.reader = FrameReader(c.Conn, true)
	}
	return c.reader.Read(b)
}
