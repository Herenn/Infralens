//go:build linux

package dnscache

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// The only automated test of the real capture path: AF_PACKET socket, the
// classic-BPF filter in the kernel, sockaddr_ll leg metadata, the routing and
// local-address loaders. Needs root (CAP_NET_RAW, and binding port 53);
// skipped otherwise. CI runs it with sudo - see .github/workflows/ci.yml.
//
//	go test -c -o dnscache.test ./agent/dnscache
//	sudo ./dnscache.test -test.run TestCaptureIntegration -test.v
//
// A tiny DNS server on a loopback address answers real queries from a real
// client socket. Loopback is a host client, so this exercises the host legs
// (query transmitted, answer received); pod legs need a veth and are covered
// by the fake-source unit tests.

// itServer answers A queries with a fixed address per name, and can also
// send answers nobody asked for.
type itServer struct {
	conn    *net.UDPConn
	answers map[string]string // qname (with trailing dot) -> IPv4
	idDelta uint16            // added to the echoed ID (to test mismatches)
	mu      sync.Mutex
}

func listenDNS(t *testing.T) *itServer {
	t.Helper()
	var lastErr error
	// Addresses nothing else should be using; port 53 is required by the filter.
	for _, a := range []string{"127.0.0.77", "127.0.0.78", "127.0.0.79", "127.0.0.80"} {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(a), Port: 53})
		if err == nil {
			s := &itServer{conn: c, answers: map[string]string{}}
			go s.serve()
			t.Cleanup(func() { _ = c.Close() })
			return s
		}
		lastErr = err
	}
	t.Fatalf("cannot bind a loopback DNS server on port 53: %v", lastErr)
	return nil
}

func (s *itServer) serve() {
	buf := make([]byte, 1500)
	for {
		n, from, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		var p dnsmessage.Parser
		h, err := p.Start(buf[:n])
		if err != nil {
			continue
		}
		q, err := p.Question()
		if err != nil {
			continue
		}
		s.mu.Lock()
		ip, delta := s.answers[q.Name.String()], s.idDelta
		s.mu.Unlock()
		if ip == "" {
			continue
		}
		s.reply(from, h.ID+delta, q.Name.String(), ip)
	}
}

func (s *itServer) reply(to *net.UDPAddr, id uint16, name, ip string) {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id, Response: true, RecursionAvailable: true})
	_ = b.StartQuestions()
	n := dnsmessage.MustNewName(name)
	_ = b.Question(dnsmessage.Question{Name: n, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET})
	_ = b.StartAnswers()
	_ = b.AResource(dnsmessage.ResourceHeader{Name: n, Class: dnsmessage.ClassINET, TTL: 300},
		dnsmessage.AResource{A: netip.MustParseAddr(ip).As4()})
	msg, err := b.Finish()
	if err == nil {
		_, _ = s.conn.WriteToUDP(msg, to)
	}
}

func (s *itServer) set(name, ip string, idDelta uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answers[name] = ip
	s.idDelta = idDelta
}

// lookup sends one query from a fresh client socket and waits briefly for
// the answer (or its absence). Returns the client's address.
func lookup(t *testing.T, server *net.UDPAddr, id uint16, name string) netip.AddrPort {
	t.Helper()
	c, err := net.DialUDP("udp4", nil, server)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write(buildQuery(t, id, name, dnsmessage.TypeA)); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1500)
	_, _ = c.Read(buf)
	return c.LocalAddr().(*net.UDPAddr).AddrPort()
}

func waitLearned(t *testing.T, tr *Tracker, client, ip, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := tr.Hostname(client, ip); got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Hostname(%s, %s) never became %q; stats: %s", client, ip, want, tr.Stats())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCaptureIntegration(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root (CAP_NET_RAW and port 53); run the compiled test binary with sudo")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tr := NewTracker(Config{})
	var logMu sync.Mutex
	var logs []string
	if err := tr.Start(ctx, func(f string, a ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		logs = append(logs, f)
	}); err != nil {
		if errors.Is(err, ErrCaptureUnsupported) {
			t.Fatal("capture unsupported on linux")
		}
		t.Fatalf("Start: %v", err)
	}
	srv := listenDNS(t)
	server := srv.conn.LocalAddr().(*net.UDPAddr)
	time.Sleep(200 * time.Millisecond) // let the capture loop reach its first read

	// 1. A real query and its answer: learned.
	srv.set("it-learned.infralens.test.", "198.51.100.77", 0)
	client := lookup(t, server, 0x1111, "it-learned.infralens.test.")
	waitLearned(t, tr, client.Addr().String(), "198.51.100.77", "it-learned.infralens.test")
	// Loopback is the host identity: visible from the host's other addresses too.
	if tr.Hostname("127.0.0.1", "198.51.100.77") == "" {
		t.Error("host identity not applied to loopback")
	}

	// 2. An answer nobody asked for, delivered to a real socket.
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	srv.reply(c.LocalAddr().(*net.UDPAddr), 0x2222, "it-unsolicited.infralens.test.", "198.51.100.78")
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := c.ReadFromUDP(make([]byte, 1500)); err != nil {
		t.Fatalf("unsolicited answer not delivered (test setup): %v", err)
	}

	// 3. A query answered with the wrong transaction ID.
	srv.set("it-wrongid.infralens.test.", "198.51.100.79", 1)
	lookup(t, server, 0x3333, "it-wrongid.infralens.test.")

	// Barrier: a later real lookup is learned, so everything before it has
	// been through the capture loop.
	srv.set("it-barrier.infralens.test.", "198.51.100.80", 0)
	barrier := lookup(t, server, 0x4444, "it-barrier.infralens.test.")
	waitLearned(t, tr, barrier.Addr().String(), "198.51.100.80", "it-barrier.infralens.test")

	for ip, what := range map[string]string{"198.51.100.78": "unsolicited answer", "198.51.100.79": "wrong-ID answer"} {
		if got := tr.Hostname("127.0.0.1", ip); got != "" {
			t.Errorf("%s was learned as %q", what, got)
		}
	}

	s := tr.Stats()
	t.Logf("stats: %s", s)
	if s.Learned < 2 || s.Unmatched < 2 || !s.CaptureActive {
		t.Errorf("unexpected stats: %s", s)
	}
	// Loopback is seen on both legs; the other copy of each packet is skipped.
	if s.OtherLeg == 0 {
		t.Error("no other-leg copies seen: leg metadata (sockaddr_ll pkttype) not working as assumed")
	}
	if s.RecvBuffer <= 0 {
		t.Error("effective receive buffer not reported")
	}
	logMu.Lock()
	defer logMu.Unlock()
	if len(logs) != 0 {
		t.Errorf("capture loop logged: %s", strings.Join(logs, "; "))
	}
}
