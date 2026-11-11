package dnscache

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"strings"
	"testing"
)

// procRouteLine renders one /proc/net/route line the way the kernel does:
// the network-order address printed as a host-order 32-bit hex number.
func procRouteLine(iface, dst, gw string, flags uint32, mask string) string {
	hx := func(s string) uint32 {
		a := netip.MustParseAddr(s).As4()
		return binary.NativeEndian.Uint32(a[:])
	}
	return fmt.Sprintf("%s\t%08X\t%08X\t%04X\t0\t0\t0\t%08X\t0\t0\t0", iface, hx(dst), hx(gw), flags, hx(mask))
}

const procRouteHeader = "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT"

func TestParseIPv4RoutesCalicoNode(t *testing.T) {
	fixture := strings.Join([]string{
		procRouteHeader,
		procRouteLine("eth0", "0.0.0.0", "192.168.1.1", rtfUp|rtfGateway, "0.0.0.0"),
		procRouteLine("eth0", "192.168.1.0", "0.0.0.0", rtfUp, "255.255.255.0"),
		procRouteLine("cali1234", "10.244.1.7", "0.0.0.0", rtfUp|0x4, "255.255.255.255"),
		procRouteLine("tunl0", "10.244.2.0", "192.168.1.21", rtfUp|rtfGateway, "255.255.255.192"),
		procRouteLine("*", "10.244.1.0", "0.0.0.0", rtfUp|rtfReject, "255.255.255.192"), // blackhole
		procRouteLine("down0", "10.9.0.0", "0.0.0.0", 0, "255.255.0.0"),                 // not up
	}, "\n")
	routes, err := ParseIPv4Routes(strings.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 4 {
		t.Fatalf("routes = %+v", routes)
	}
	rt := NewRouteTable(routes, map[string]int{"eth0": 2, "cali1234": 7, "tunl0": 9})

	cases := map[string]struct {
		dev   int
		local bool
	}{
		"10.244.1.7":   {7, true},  // local pod: /32 via its veth
		"10.244.1.8":   {0, false}, // in the local block but no pod: blackholed
		"10.244.2.5":   {0, false}, // pod on another node: via a gateway
		"192.168.1.30": {0, false}, // another host on the uplink subnet
		"8.8.8.8":      {0, false}, // the internet
	}
	for addr, want := range cases {
		dev, ok := rt.LocalDevice(netip.MustParseAddr(addr))
		if dev != want.dev || ok != want.local {
			t.Errorf("LocalDevice(%s) = %d, %v; want %d, %v", addr, dev, ok, want.dev, want.local)
		}
	}
}

func TestParseIPv4RoutesFlannelNode(t *testing.T) {
	fixture := strings.Join([]string{
		procRouteHeader,
		procRouteLine("ens5", "0.0.0.0", "10.0.0.1", rtfUp|rtfGateway, "0.0.0.0"),
		procRouteLine("ens5", "10.0.0.0", "0.0.0.0", rtfUp, "255.255.255.0"),
		procRouteLine("cni0", "10.244.1.0", "0.0.0.0", rtfUp, "255.255.255.0"),
		procRouteLine("flannel.1", "10.244.2.0", "10.244.2.0", rtfUp|rtfGateway, "255.255.255.0"),
		procRouteLine("docker0", "172.17.0.0", "0.0.0.0", rtfUp, "255.255.0.0"),
	}, "\n")
	routes, err := ParseIPv4Routes(strings.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	rt := NewRouteTable(routes, map[string]int{"ens5": 2, "cni0": 4, "flannel.1": 5, "docker0": 6})
	for addr, wantDev := range map[string]int{"10.244.1.9": 4, "172.17.0.2": 6, "10.244.2.9": 0, "10.0.0.7": 0} {
		dev, ok := rt.LocalDevice(netip.MustParseAddr(addr))
		if dev != wantDev || ok != (wantDev != 0) {
			t.Errorf("LocalDevice(%s) = %d, %v; want %d", addr, dev, ok, wantDev)
		}
	}
}

// AWS VPC CNI: pods on other nodes share the uplink's connected subnet. The
// uplink carries the default route, so that route never marks a client local;
// local pods have /32 routes via their own veths.
func TestRouteTableAWSVPCCNI(t *testing.T) {
	p := netip.MustParsePrefix
	rt := NewRouteTable([]Route{
		{Prefix: p("0.0.0.0/0"), Iface: "eth0", Gateway: true},
		{Prefix: p("10.0.0.0/24"), Iface: "eth0"},
		{Prefix: p("10.0.0.55/32"), Iface: "eni1a2b"},
	}, map[string]int{"eth0": 2, "eni1a2b": 11})
	if dev, ok := rt.LocalDevice(netip.MustParseAddr("10.0.0.55")); !ok || dev != 11 {
		t.Errorf("local pod: %d %v", dev, ok)
	}
	if _, ok := rt.LocalDevice(netip.MustParseAddr("10.0.0.56")); ok {
		t.Error("remote pod in the VPC subnet classified as local")
	}
	if _, ok := rt.LocalDevice(netip.MustParseAddr("::ffff:10.0.0.55")); !ok {
		t.Error("mapped address not unmapped")
	}
	var nilRT *RouteTable
	if _, ok := nilRT.LocalDevice(netip.MustParseAddr("10.0.0.55")); ok {
		t.Error("nil table must know nothing")
	}
	// An interface with no index (gone since the table was read) is not used.
	rt2 := NewRouteTable([]Route{{Prefix: p("10.0.0.55/32"), Iface: "gone"}}, map[string]int{})
	if _, ok := rt2.LocalDevice(netip.MustParseAddr("10.0.0.55")); ok {
		t.Error("route via an unknown interface used")
	}
}

func TestParseIPv6Routes(t *testing.T) {
	fixture := strings.Join([]string{
		"00000000000000000000000000000000 00 00000000000000000000000000000000 00 fe800000000000000000000000000001 00000400 00000001 00000000 00000003     eth0",
		"fd00001002440001000000000000000700 80 00000000000000000000000000000000 00 00000000000000000000000000000000 00000400 00000001 00000000 00000001 cali1234",
		"fd000010024400010000000000000007 80 00000000000000000000000000000000 00 00000000000000000000000000000000 00000400 00000001 00000000 00000001 cali1234",
		"fd000010024400020000000000000000 40 00000000000000000000000000000000 00 fd000000000000000000000000000021 00000400 00000001 00000000 00000003  vxlan6",
		"00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 ffffffff 00000001 00000000 00200200       lo",
		"00000000000000000000000000000001 80 00000000000000000000000000000000 00 00000000000000000000000000000000 00000000 00000004 00000000 80200001       lo",
	}, "\n")
	// The second line has a 34-hex-digit destination: malformed input is an error.
	if _, err := ParseIPv6Routes(strings.NewReader(fixture)); err == nil {
		t.Fatal("malformed line accepted")
	}
	lines := strings.Split(fixture, "\n")
	good := strings.Join(append(lines[:1:1], lines[2:]...), "\n")
	routes, err := ParseIPv6Routes(strings.NewReader(good))
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 4 { // the unreachable lo route is skipped
		t.Fatalf("routes = %+v", routes)
	}
	rt := NewRouteTable(routes, map[string]int{"eth0": 2, "cali1234": 7, "vxlan6": 8, "lo": 1})
	if dev, ok := rt.LocalDevice(netip.MustParseAddr("fd00:10:244:1::7")); !ok || dev != 7 {
		t.Errorf("v6 local pod: %d %v", dev, ok)
	}
	for _, a := range []string{"fd00:10:244:2::9", "2001:db8::1", "::1"} {
		if _, ok := rt.LocalDevice(netip.MustParseAddr(a)); ok {
			t.Errorf("%s classified local", a)
		}
	}
}

func TestParseIPv4RoutesMalformed(t *testing.T) {
	if _, err := ParseIPv4Routes(strings.NewReader(procRouteHeader + "\neth0\tZZZZ\t0\t1\t0\t0\t0\t0\t0\t0\t0")); err == nil {
		t.Error("malformed line accepted")
	}
	routes, err := ParseIPv4Routes(strings.NewReader(procRouteHeader + "\nshort line\n"))
	if err != nil || len(routes) != 0 {
		t.Errorf("short line: %v %v", routes, err)
	}
}
