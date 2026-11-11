//go:build linux

package dnscache

import (
	"errors"
	"net"
	"os"
)

// loadRoutes reads the main routing table of the agent's network namespace
// (the host's, with hostNetwork) and the interface name -> index map.
func loadRoutes() (*RouteTable, error) {
	var routes []Route
	var errs []error
	for _, src := range []struct {
		path  string
		parse func(f *os.File) ([]Route, error)
	}{
		{"/proc/net/route", func(f *os.File) ([]Route, error) { return ParseIPv4Routes(f) }},
		{"/proc/net/ipv6_route", func(f *os.File) ([]Route, error) { return ParseIPv6Routes(f) }},
	} {
		f, err := os.Open(src.path)
		if err != nil {
			errs = append(errs, err) // no IPv6 on this host is fine
			continue
		}
		rs, err := src.parse(f)
		_ = f.Close()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		routes = append(routes, rs...)
	}
	if routes == nil {
		return nil, errors.Join(errs...)
	}
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	idx := make(map[string]int, len(ifs))
	for _, i := range ifs {
		idx[i.Name] = i.Index
	}
	return NewRouteTable(routes, idx), nil
}
