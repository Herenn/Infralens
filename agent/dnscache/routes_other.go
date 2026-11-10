//go:build !linux

package dnscache

// loadRoutes has no routing table to read off Linux; capture is unavailable
// there anyway (see openPacketSource).
func loadRoutes() (*RouteTable, error) {
	return NewRouteTable(nil, nil), nil
}
