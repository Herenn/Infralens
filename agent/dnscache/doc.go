// Package dnscache learns which DNS names the workloads on this host used to
// reach the IPs they connect to, so the agent can report "api.stripe.com"
// instead of a bare "52.1.2.3". EXPERIMENTAL and off by default in the agent.
//
// # How names are learned
//
// The agent passively observes DNS over UDP on the host with an AF_PACKET
// socket and a classic-BPF filter (queries to port 53, responses from it) -
// the mechanism tcpdump uses. No eBPF/kernel-side code is involved, and
// nothing is sent on the network: there are no reverse (PTR) lookups.
//
// A response is believed only if it answers a query the agent saw the same
// client send, within a few seconds, matching client address and port,
// server address, transaction ID, question name and type; each query is
// answered at most once. Both packets must be the copies on the client's own
// leg: for a pod, received from / transmitted to the interface that routes
// to it; for the host, transmitted / received. Clients that are not on this
// host (e.g. pods on other nodes served by a CoreDNS replica here) are
// ignored. See Tracker for the details and RouteTable for "on this host".
//
// The question name (what the application asked for) is mapped to the final
// A/AAAA addresses through any CNAME chain in the answer and stored per
// client in a bounded cache with per-client quotas and a hard retention cap.
// All of the host's own addresses and loopback share one "host" identity.
//
// # Trust model
//
// Matching defeats off-path forgery: a forger must guess a 16-bit ID and the
// client's (normally random) source port, know the question, and win the
// race against the real answer. It does not defend against the resolver
// itself or anyone on the path between client and resolver - they can answer
// anything, and the application believes them too. Names are display labels
// derived from what the workload was told, not verified facts.
//
// # Privacy
//
// The cache lives only in agent memory. A name is only ever sent to the
// backend attached to a report for a flow this host opened to an address
// that same client resolved; names for lookups that were never followed by
// a connection never leave the agent.
//
// # Robustness
//
// Everything here parses untrusted network input: explicit bounds checks for
// IP/UDP, golang.org/x/net/dns/dnsmessage for DNS (which bounds compression
// pointer chasing), no per-record allocation, a 4 KiB message cap, a packet
// budget, and fuzz tests. Capture is fail-open: if the socket cannot be
// opened, Start returns an error, the agent logs it once, and lookups miss.
package dnscache
