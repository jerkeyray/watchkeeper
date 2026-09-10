package benchmark

import (
	"io"
	"net"
	"net/url"
	"sync"
	"time"
)

// dbProxy interrupts real PostgreSQL connections, including pooled sessions.
// Only strategy storage uses it; the controller's experiment journal stays available.
type dbProxy struct {
	listener    net.Listener
	target      string
	mu          sync.Mutex
	until       time.Time
	connections map[net.Conn]bool
	closed      bool
}

func newDBProxy(dsn string) (*dbProxy, string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, "", err
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", err
	}
	p := &dbProxy{listener: l, target: u.Host, connections: map[net.Conn]bool{}}
	if _, _, err = net.SplitHostPort(p.target); err != nil {
		p.target = net.JoinHostPort(u.Hostname(), "5432")
	}
	u.Host = l.Addr().String()
	go p.serve()
	return p, u.String(), nil
}
func (p *dbProxy) serve() {
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		blocked := p.closed || time.Now().Before(p.until)
		p.mu.Unlock()
		if blocked {
			conn.Close()
			continue
		}
		go p.forward(conn)
	}
}
func (p *dbProxy) forward(a net.Conn) {
	b, err := net.DialTimeout("tcp", p.target, 2*time.Second)
	if err != nil {
		a.Close()
		return
	}
	p.mu.Lock()
	if p.closed || time.Now().Before(p.until) {
		p.mu.Unlock()
		a.Close()
		b.Close()
		return
	}
	p.connections[a] = true
	p.connections[b] = true
	p.mu.Unlock()
	defer func() {
		a.Close()
		b.Close()
		p.mu.Lock()
		delete(p.connections, a)
		delete(p.connections, b)
		p.mu.Unlock()
	}()
	done := make(chan struct{})
	go func() { io.Copy(a, b); a.Close(); close(done) }()
	io.Copy(b, a)
	b.Close()
	<-done
}
func (p *dbProxy) interrupt(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.until = time.Now().Add(d)
	for c := range p.connections {
		c.Close()
	}
}
func (p *dbProxy) close() {
	p.mu.Lock()
	p.closed = true
	for c := range p.connections {
		c.Close()
	}
	p.mu.Unlock()
	p.listener.Close()
}
