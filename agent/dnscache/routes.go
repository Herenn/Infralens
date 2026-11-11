package dnscache

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net/netip"
	"strconv"
	"strings"
)

// Route flags (linux/route.h, shared by IPv4 and IPv6 route dumps).
const (
	rtfUp      = 0x0001
	rtfGateway = 0x0002
	rtfReject  = 0x0200
)

// Route is one entry of the kernel's main routing table.
type Route struct {
	Prefix  netip.Prefix
	Iface   string
	Gateway bool
}

// RouteTable answers "is this address a workload on this node, and which
// interface delivers to it". It is how the tracker tells a local pod or
// container (whose DNS answers matter: it may connect through this agent's
// node) from a client on another node (whose answers a CoreDNS replica on
// this node happens to serve, and which this agent will never see connect),
// without any Kubernetes API access.
//
// The rule: the longest-prefix route for the address is directly connected
// (no gateway), and its interface is neither loopback nor one that carries a
// default route. That is what pod routes look like on the common CNIs:
//
//	Calico, AWS VPC CNI, kindnet:  10.244.1.7 dev caliXXXX scope link   (/32 per pod)
//	flannel, bridge, docker:       10.244.1.0/24 dev cni0 scope link    (local pod subnet)
//
// while pods on other nodes are reached through a gateway (flannel.1 /
// tunl0 / vxlan.calico "via ...") or the uplink's default route.
type RouteTable struct {
	routes  []Route
	ifindex map[string]int
	uplinks map[string]bool
}

// NewRouteTable builds a table from routes and an interface name -> index map.
func NewRouteTable(routes []Route, ifindex map[string]int) *RouteTable {
	rt := &RouteTable{ifindex: ifindex, uplinks: make(map[string]bool)}
	for _, r := range routes {
		if r.Prefix.Bits() == 0 {
			rt.uplinks[r.Iface] = true
		}
	}
	rt.routes = routes
	return rt
}

// LocalDevice returns the index of the interface that delivers to addr, if
// addr is a workload on this node by the rule above.
func (rt *RouteTable) LocalDevice(addr netip.Addr) (int, bool) {
	if rt == nil {
		return 0, false
	}
	addr = addr.Unmap()
	best := -1
	for i := range rt.routes {
		r := &rt.routes[i]
		if r.Prefix.Contains(addr) && (best < 0 || r.Prefix.Bits() > rt.routes[best].Prefix.Bits()) {
			best = i
		}
	}
	if best < 0 {
		return 0, false
	}
	r := rt.routes[best]
	if r.Gateway || r.Iface == "lo" || rt.uplinks[r.Iface] {
		return 0, false
	}
	idx, ok := rt.ifindex[r.Iface]
	return idx, ok && idx > 0
}

// ParseIPv4Routes parses /proc/net/route. Addresses there are the raw
// network-order 32-bit values printed as host-order hex, hence NativeEndian.
// Down, rejecting (blackhole/unreachable) and interface-less routes are
// skipped.
func ParseIPv4Routes(r io.Reader) ([]Route, error) {
	var out []Route
	sc := bufio.NewScanner(r)
	first := true
	for sc.Scan() {
		if first { // header line
			first = false
			continue
		}
		f := strings.Fields(sc.Text())
		if len(f) < 8 {
			continue
		}
		dst, err1 := strconv.ParseUint(f[1], 16, 32)
		flags, err2 := strconv.ParseUint(f[3], 16, 32)
		mask, err3 := strconv.ParseUint(f[7], 16, 32)
		if err1 != nil || err2 != nil || err3 != nil {
			return nil, fmt.Errorf("parsing /proc/net/route line %q", sc.Text())
		}
		if flags&rtfUp == 0 || flags&rtfReject != 0 || f[0] == "*" {
			continue
		}
		var d, m [4]byte
		binary.NativeEndian.PutUint32(d[:], uint32(dst))
		binary.NativeEndian.PutUint32(m[:], uint32(mask))
		bits := 0
		for _, b := range m {
			for b&0x80 != 0 {
				bits++
				b <<= 1
			}
		}
		p, err := netip.AddrFrom4(d).Prefix(bits)
		if err != nil {
			continue
		}
		out = append(out, Route{Prefix: p, Iface: f[0], Gateway: flags&rtfGateway != 0})
	}
	return out, sc.Err()
}

// ParseIPv6Routes parses /proc/net/ipv6_route (addresses in network order).
func ParseIPv6Routes(r io.Reader) ([]Route, error) {
	var out []Route
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 10 {
			continue
		}
		dst, err1 := hex.DecodeString(f[0])
		plen, err2 := strconv.ParseUint(f[1], 16, 8)
		flags, err3 := strconv.ParseUint(f[8], 16, 32)
		if err1 != nil || err2 != nil || err3 != nil || len(dst) != 16 || plen > 128 {
			return nil, fmt.Errorf("parsing /proc/net/ipv6_route line %q", sc.Text())
		}
		if flags&rtfUp == 0 || flags&rtfReject != 0 {
			continue
		}
		p, err := netip.AddrFrom16([16]byte(dst)).Prefix(int(plen))
		if err != nil {
			continue
		}
		out = append(out, Route{Prefix: p, Iface: f[9], Gateway: flags&rtfGateway != 0})
	}
	return out, sc.Err()
}
