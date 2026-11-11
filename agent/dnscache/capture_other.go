//go:build !linux

package dnscache

// openPacketSource is unavailable off Linux; the agent itself only runs on
// Linux, this exists so the package (and its tests) build everywhere.
func openPacketSource() (packetSource, error) {
	return nil, ErrCaptureUnsupported
}
