package kcp

import (
	"crypto/rand"
	"encoding/binary"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

type errTimeout struct {
	error
}

func (errTimeout) Timeout() bool   { return true }
func (errTimeout) Temporary() bool { return true }
func (errTimeout) Error() string   { return "i/o timeout" }

const (
	// maximum packet size
	mtuLimit = 1500

	// accept backlog
	acceptBacklog = 128
)

var (
	errBrokenPipe       = errors.New("broken pipe")
	errInvalidOperation = errors.New("invalid operation")
)

var (
	// to mitigate high-frequency memory allocation for packets
	xmitBuf sync.Pool
)

func init() {
	xmitBuf.New = func() interface{} {
		return make([]byte, mtuLimit)
	}
}

type (
	// UDPSession defines a KCP session implemented by UDP
	UDPSession struct {
		updaterIdx int            // record slice index in updater
		conn       net.PacketConn // the underlying packet connection
		kcp        *KCP           // KCP ARQ protocol
		l          *Listener      // pointing to the Listener object if it's been accepted by a Listener

		// kcp receiving is based on packets
		// recvbuf turns packets into stream
		recvbuf []byte
		bufptr  []byte
		// header extended output buffer, if has header
		ext []byte

		// settings
		remote     net.Addr  // remote peer address
		rd         time.Time // read deadline
		wd         time.Time // write deadline
		headerSize int       // the header size additional to a KCP frame
		ackNoDelay bool      // send ack immediately for each incoming packet(testing purpose)
		writeDelay bool      // delay kcp.flush() for Write() for bulk transfer
		dup        int       // duplicate udp packets(testing purpose)

		// notifications
		die          chan struct{} // notify current session has Closed
		chReadEvent  chan struct{} // notify Read() can be called without blocking
		chWriteEvent chan struct{} // notify Write() can be called without blocking
		chReadError  chan error    // notify PacketConn.Read() have an error
		chWriteError chan error    // notify PacketConn.Write() have an error

		isClosed bool // flag the session has Closed
		mu       sync.Mutex
	}

	setReadBuffer interface {
		SetReadBuffer(bytes int) error
	}

	setWriteBuffer interface {
		SetWriteBuffer(bytes int) error
	}
)

// newUDPSession create a new udp session for client or server
func newUDPSession(conv uint32, l *Listener, conn net.PacketConn, remote net.Addr) *UDPSession {
	sess := new(UDPSession)
	sess.die = make(chan struct{})
	sess.chReadEvent = make(chan struct{}, 1)
	sess.chWriteEvent = make(chan struct{}, 1)
	sess.chReadError = make(chan error, 1)
	sess.chWriteError = make(chan error, 1)
	sess.remote = remote
	sess.conn = conn
	sess.l = l
	sess.recvbuf = make([]byte, mtuLimit)

	// we only need to allocate extended packet buffer if we have the additional header
	if sess.headerSize > 0 {
		sess.ext = make([]byte, mtuLimit)
	}

	sess.kcp = NewKCP(conv, func(buf []byte, size int) {
		if size >= IKCP_OVERHEAD {
			sess.output(buf[:size])
		}
	})
	sess.kcp.SetMtu(IKCP_MTU_DEF - sess.headerSize)

	// register current session to the global updater,
	// which call sess.update() periodically.
	updater.addSession(sess)

	if sess.l == nil { // it's a client connection
		go sess.readLoop()
	}

	return sess
}

// Read implements net.Conn
func (s *UDPSession) Read(b []byte) (n int, err error) {
	for {
		s.mu.Lock()
		if len(s.bufptr) > 0 { // copy from buffer into b
			n = copy(b, s.bufptr)
			s.bufptr = s.bufptr[n:]
			s.mu.Unlock()
			return n, nil
		}

		if s.isClosed {
			s.mu.Unlock()
			return 0, errBrokenPipe
		}

		if size := s.kcp.PeekSize(); size > 0 { // peek data size from kcp
			if len(b) >= size { // receive data into 'b' directly
				s.kcp.Recv(b)
				s.mu.Unlock()
				return size, nil
			}

			// if necessary resize the stream buffer to guarantee a sufficent buffer space
			if cap(s.recvbuf) < size {
				s.recvbuf = make([]byte, size)
			}

			// resize the length of recvbuf to correspond to data size
			s.recvbuf = s.recvbuf[:size]
			s.kcp.Recv(s.recvbuf)
			n = copy(b, s.recvbuf)   // copy to 'b'
			s.bufptr = s.recvbuf[n:] // pointer update
			s.mu.Unlock()
			return n, nil
		}

		// deadline for current reading operation
		var timeout *time.Timer
		var c <-chan time.Time
		if !s.rd.IsZero() {
			if time.Now().After(s.rd) {
				s.mu.Unlock()
				return 0, errTimeout{}
			}

			delay := s.rd.Sub(time.Now())
			timeout = time.NewTimer(delay)
			c = timeout.C
		}
		s.mu.Unlock()

		// wait for read event or timeout
		select {
		case <-s.chReadEvent:
		case <-c:
		case <-s.die:
		case err = <-s.chReadError:
			if timeout != nil {
				timeout.Stop()
			}
			return n, err
		}

		if timeout != nil {
			timeout.Stop()
		}
	}
}

// Write implements net.Conn
func (s *UDPSession) Write(b []byte) (n int, err error) {
	for {
		s.mu.Lock()
		if s.isClosed {
			s.mu.Unlock()
			return 0, errBrokenPipe
		}

		// controls how much data will be sent to kcp core
		// to prevent the memory from exhuasting
		if s.kcp.WaitSnd() < int(s.kcp.snd_wnd) {
			n = len(b)
			for {
				if len(b) <= int(s.kcp.mss) {
					s.kcp.Send(b)
					break
				} else {
					s.kcp.Send(b[:s.kcp.mss])
					b = b[s.kcp.mss:]
				}
			}

			// flush immediately if the queue is full
			if s.kcp.WaitSnd() >= int(s.kcp.snd_wnd) || !s.writeDelay {
				s.kcp.flush(false)
			}
			s.mu.Unlock()
			return n, nil
		}

		// deadline for current writing operation
		var timeout *time.Timer
		var c <-chan time.Time
		if !s.wd.IsZero() {
			if time.Now().After(s.wd) {
				s.mu.Unlock()
				return 0, errTimeout{}
			}
			delay := s.wd.Sub(time.Now())
			timeout = time.NewTimer(delay)
			c = timeout.C
		}
		s.mu.Unlock()

		// wait for write event or timeout
		select {
		case <-s.chWriteEvent:
		case <-c:
		case <-s.die:
		case err = <-s.chWriteError:
			if timeout != nil {
				timeout.Stop()
			}
			return n, err
		}

		if timeout != nil {
			timeout.Stop()
		}
	}
}

// Close closes the connection.
func (s *UDPSession) Close() error {
	// remove current session from updater & listener(if necessary)
	updater.removeSession(s)
	if s.l != nil { // notify listener
		s.l.closeSession(s.remote)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isClosed {
		return errBrokenPipe
	}
	close(s.die)
	s.isClosed = true
	if s.l == nil { // client socket close
		return s.conn.Close()
	}
	return nil
}

// LocalAddr returns the local network address. The Addr returned is shared by all invocations of LocalAddr, so do not modify it.
func (s *UDPSession) LocalAddr() net.Addr { return s.conn.LocalAddr() }

// RemoteAddr returns the remote network address. The Addr returned is shared by all invocations of RemoteAddr, so do not modify it.
func (s *UDPSession) RemoteAddr() net.Addr { return s.remote }

// SetDeadline sets the deadline associated with the listener. A zero time value disables the deadline.
func (s *UDPSession) SetDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rd = t
	s.wd = t
	s.notifyReadEvent()
	s.notifyWriteEvent()
	return nil
}

// SetReadDeadline implements the Conn SetReadDeadline method.
func (s *UDPSession) SetReadDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rd = t
	s.notifyReadEvent()
	return nil
}

// SetWriteDeadline implements the Conn SetWriteDeadline method.
func (s *UDPSession) SetWriteDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wd = t
	s.notifyWriteEvent()
	return nil
}

// SetWriteDelay delays write for bulk transfer until the next update interval
func (s *UDPSession) SetWriteDelay(delay bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeDelay = delay
}

// SetWindowSize set maximum window size
func (s *UDPSession) SetWindowSize(sndwnd, rcvwnd int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kcp.WndSize(sndwnd, rcvwnd)
}

// SetMtu sets the maximum transmission unit(not including UDP header)
func (s *UDPSession) SetMtu(mtu int) bool {
	if mtu > mtuLimit {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.kcp.SetMtu(mtu - s.headerSize)
	return true
}

// SetStreamMode toggles the stream mode on/off
func (s *UDPSession) SetStreamMode(enable bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if enable {
		s.kcp.stream = 1
	} else {
		s.kcp.stream = 0
	}
}

// SetACKNoDelay changes ack flush option, set true to flush ack immediately,
func (s *UDPSession) SetACKNoDelay(nodelay bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ackNoDelay = nodelay
}

// SetDUP duplicates udp packets for kcp output, for testing purpose only
func (s *UDPSession) SetDUP(dup int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dup = dup
}

// SetNoDelay calls nodelay() of kcp
// https://github.com/skywind3000/kcp/blob/master/README.en.md#protocol-configuration
func (s *UDPSession) SetNoDelay(nodelay, interval, resend, nc int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kcp.NoDelay(nodelay, interval, resend, nc)
}

// SetDSCP sets the 6bit DSCP field of IP header, no effect if it's accepted from Listener
func (s *UDPSession) SetDSCP(dscp int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.l == nil {
		if nc, ok := s.conn.(net.Conn); ok {
			if err := ipv4.NewConn(nc).SetTOS(dscp << 2); err != nil {
				return ipv6.NewConn(nc).SetTrafficClass(dscp)
			}
			return nil
		}
	}
	return errInvalidOperation
}

// SetReadBuffer sets the socket read buffer, no effect if it's accepted from Listener
func (s *UDPSession) SetReadBuffer(bytes int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.l == nil {
		if nc, ok := s.conn.(setReadBuffer); ok {
			return nc.SetReadBuffer(bytes)
		}
	}
	return errInvalidOperation
}

// SetWriteBuffer sets the socket write buffer, no effect if it's accepted from Listener
func (s *UDPSession) SetWriteBuffer(bytes int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.l == nil {
		if nc, ok := s.conn.(setWriteBuffer); ok {
			return nc.SetWriteBuffer(bytes)
		}
	}
	return errInvalidOperation
}

// post-processing for sending a packet from kcp core
// steps:
// 1. Header extending
// 2. WriteTo kernel
func (s *UDPSession) output(buf []byte) {
	var ecc [][]byte

	// 1. extend buf's header space(if necessary)
	ext := buf
	if s.headerSize > 0 {
		ext = s.ext[:s.headerSize+len(buf)]
		copy(ext[s.headerSize:], buf)
	}

	// 2. WriteTo kernel
	nbytes := 0
	npkts := 0
	for i := 0; i < s.dup+1; i++ {
		if n, err := s.conn.WriteTo(ext, s.remote); err == nil {
			nbytes += n
			npkts++
		} else {
			s.notifyWriteError(err)
		}
	}

	for k := range ecc {
		if n, err := s.conn.WriteTo(ecc[k], s.remote); err == nil {
			nbytes += n
			npkts++
		} else {
			s.notifyWriteError(err)
		}
	}
}

// kcp update, returns interval for next calling
func (s *UDPSession) update() (interval time.Duration) {
	s.mu.Lock()
	waitsnd := s.kcp.WaitSnd()
	interval = time.Duration(s.kcp.flush(false)) * time.Millisecond
	if s.kcp.WaitSnd() < waitsnd {
		s.notifyWriteEvent()
	}
	s.mu.Unlock()
	return
}

// GetConv gets conversation id of a session
func (s *UDPSession) GetConv() uint32 { return s.kcp.conv }

func (s *UDPSession) notifyReadEvent() {
	select {
	case s.chReadEvent <- struct{}{}:
	default:
	}
}

func (s *UDPSession) notifyWriteEvent() {
	select {
	case s.chWriteEvent <- struct{}{}:
	default:
	}
}

func (s *UDPSession) notifyWriteError(err error) {
	select {
	case s.chWriteError <- err:
	default:
	}
}

func (s *UDPSession) kcpInput(data []byte) {
	var kcpInErrors uint64

	s.mu.Lock()
	waitsnd := s.kcp.WaitSnd()
	if ret := s.kcp.Input(data, true, s.ackNoDelay); ret != 0 {
		kcpInErrors++
	}
	if n := s.kcp.PeekSize(); n > 0 {
		s.notifyReadEvent()
	}
	if s.kcp.WaitSnd() < waitsnd {
		s.notifyWriteEvent()
	}
	s.mu.Unlock()
}

// the read loop for a client session
func (s *UDPSession) readLoop() {
	buf := make([]byte, mtuLimit)
	var src string
	for {
		if n, addr, err := s.conn.ReadFrom(buf); err == nil {
			// make sure the packet is from the same source
			if src == "" { // set source address
				src = addr.String()
			} else if addr.String() != src {
				continue
			}

			if n >= s.headerSize+IKCP_OVERHEAD {
				data := buf[:n]
				s.kcpInput(data)
			}
		} else {
			s.chReadError <- err
			return
		}
	}
}

type (
	// Listener defines a server which will be waiting to accept incoming connections
	Listener struct {
		conn net.PacketConn // the underlying packet connection

		sessions        map[string]*UDPSession // all sessions accepted by this Listener
		sessionLock     sync.Mutex
		chAccepts       chan *UDPSession // Listen() backlog
		chSessionClosed chan net.Addr    // session close queue
		headerSize      int              // the additional header to a KCP frame
		die             chan struct{}    // notify the listener has closed
		rd              atomic.Value     // read deadline for Accept()
		wd              atomic.Value
	}
)

// monitor incoming data for all connections of server
func (l *Listener) monitor() {
	// a cache for session object last used
	var lastAddr string
	var lastSession *UDPSession
	buf := make([]byte, mtuLimit)
	for {
		if n, from, err := l.conn.ReadFrom(buf); err == nil {
			if n >= l.headerSize+IKCP_OVERHEAD {
				data := buf[:n]

				addr := from.String()
				var s *UDPSession
				var ok bool

				// the packets received from an address always come in batch,
				// cache the session for next packet, without querying map.
				if addr == lastAddr {
					s, ok = lastSession, true
				} else {
					l.sessionLock.Lock()
					if s, ok = l.sessions[addr]; ok {
						lastSession = s
						lastAddr = addr
					}
					l.sessionLock.Unlock()
				}

				if !ok { // new session
					if len(l.chAccepts) < cap(l.chAccepts) { // do not let the new sessions overwhelm accept queue
						var conv uint32
						convValid := false
						conv = binary.LittleEndian.Uint32(data)
						convValid = true

						if convValid { // creates a new session only if the 'conv' field in kcp is accessible
							s := newUDPSession(conv, l, l.conn, from)
							s.kcpInput(data)
							l.sessionLock.Lock()
							l.sessions[addr] = s
							l.sessionLock.Unlock()
							l.chAccepts <- s
						}
					}
				} else {
					s.kcpInput(data)
				}
			}
		} else {
			return
		}
	}
}

// SetReadBuffer sets the socket read buffer for the Listener
func (l *Listener) SetReadBuffer(bytes int) error {
	if nc, ok := l.conn.(setReadBuffer); ok {
		return nc.SetReadBuffer(bytes)
	}
	return errInvalidOperation
}

// SetWriteBuffer sets the socket write buffer for the Listener
func (l *Listener) SetWriteBuffer(bytes int) error {
	if nc, ok := l.conn.(setWriteBuffer); ok {
		return nc.SetWriteBuffer(bytes)
	}
	return errInvalidOperation
}

// SetDSCP sets the 6bit DSCP field of IP header
func (l *Listener) SetDSCP(dscp int) error {
	if nc, ok := l.conn.(net.Conn); ok {
		if err := ipv4.NewConn(nc).SetTOS(dscp << 2); err != nil {
			return ipv6.NewConn(nc).SetTrafficClass(dscp)
		}
		return nil
	}
	return errInvalidOperation
}

// Accept implements the Accept method in the Listener interface; it waits for the next call and returns a generic Conn.
func (l *Listener) Accept() (net.Conn, error) {
	return l.AcceptKCP()
}

// AcceptKCP accepts a KCP connection
func (l *Listener) AcceptKCP() (*UDPSession, error) {
	var timeout <-chan time.Time
	if tdeadline, ok := l.rd.Load().(time.Time); ok && !tdeadline.IsZero() {
		timeout = time.After(tdeadline.Sub(time.Now()))
	}

	select {
	case <-timeout:
		return nil, &errTimeout{}
	case c := <-l.chAccepts:
		return c, nil
	case <-l.die:
		return nil, errBrokenPipe
	}
}

// SetDeadline sets the deadline associated with the listener. A zero time value disables the deadline.
func (l *Listener) SetDeadline(t time.Time) error {
	l.SetReadDeadline(t)
	l.SetWriteDeadline(t)
	return nil
}

// SetReadDeadline implements the Conn SetReadDeadline method.
func (l *Listener) SetReadDeadline(t time.Time) error {
	l.rd.Store(t)
	return nil
}

// SetWriteDeadline implements the Conn SetWriteDeadline method.
func (l *Listener) SetWriteDeadline(t time.Time) error {
	l.wd.Store(t)
	return nil
}

// Close stops listening on the UDP address. Already Accepted connections are not closed.
func (l *Listener) Close() error {
	close(l.die)
	return l.conn.Close()
}

// closeSession notify the listener that a session has closed
func (l *Listener) closeSession(remote net.Addr) (ret bool) {
	l.sessionLock.Lock()
	defer l.sessionLock.Unlock()
	if _, ok := l.sessions[remote.String()]; ok {
		delete(l.sessions, remote.String())
		return true
	}
	return false
}

// Addr returns the listener's network address, The Addr returned is shared by all invocations of Addr, so do not modify it.
func (l *Listener) Addr() net.Addr { return l.conn.LocalAddr() }

// Listen listens for incoming KCP packets addressed to the local address laddr on the network "udp",
func Listen(laddr string) (*Listener, error) {
	udpaddr, err := net.ResolveUDPAddr("udp", laddr)
	if err != nil {
		return nil, errors.Wrap(err, "net.ResolveUDPAddr")
	}
	conn, err := net.ListenUDP("udp", udpaddr)
	if err != nil {
		return nil, errors.Wrap(err, "net.ListenUDP")
	}

	return ServeConn(conn)
}

// ServeConn serves KCP protocol for a single packet connection.
func ServeConn(conn net.PacketConn) (*Listener, error) {
	l := new(Listener)
	l.conn = conn
	l.sessions = make(map[string]*UDPSession)
	l.chAccepts = make(chan *UDPSession, acceptBacklog)
	l.chSessionClosed = make(chan net.Addr)
	l.die = make(chan struct{})

	go l.monitor()
	return l, nil
}

// Dial connects to the remote address "raddr" on the network "udp"
func Dial(raddr string) (*UDPSession, error) {
	// network type detection
	udpaddr, err := net.ResolveUDPAddr("udp", raddr)
	if err != nil {
		return nil, errors.Wrap(err, "net.ResolveUDPAddr")
	}
	network := "udp4"
	if udpaddr.IP.To4() == nil {
		network = "udp"
	}

	conn, err := net.ListenUDP(network, nil)
	if err != nil {
		return nil, errors.Wrap(err, "net.DialUDP")
	}

	return NewConn(raddr, conn)
}

// NewConn establishes a session and talks KCP protocol over a packet connection.
func NewConn(raddr string, conn net.PacketConn) (*UDPSession, error) {
	udpaddr, err := net.ResolveUDPAddr("udp", raddr)
	if err != nil {
		return nil, errors.Wrap(err, "net.ResolveUDPAddr")
	}

	var convid uint32
	binary.Read(rand.Reader, binary.LittleEndian, &convid)
	return newUDPSession(convid, nil, conn, udpaddr), nil
}

// monotonic reference time point
var refTime = time.Now()

// currentMs returns current elasped monotonic milliseconds since program startup
func currentMs() uint32 { return uint32(time.Now().Sub(refTime) / time.Millisecond) }
