package dnscache

import (
	"errors"
	"net/netip"

	"golang.org/x/net/dns/dnsmessage"
)

// Bounds on work per message. A legitimate answer carries a handful of
// records; these only exist so a hostile packet cannot make one parse
// arbitrarily expensive.
const (
	maxRecordsScanned = 256 // answer-section records examined per message
	maxCNAMEsKept     = 32  // CNAME records remembered for chain-following
	maxAddrRecords    = 64  // A/AAAA records remembered
	maxAddrsPerAnswer = 64  // distinct addresses returned
	maxCNAMEChain     = 16  // CNAME hops followed from the question name
)

// Errors returned by the parsers. Callers generally only count them: a
// message that is not usable is skipped, never fatal.
var (
	ErrNotResponse   = errors.New("dns: not a response")
	ErrNotQuery      = errors.New("dns: not a query")
	ErrUnsupported   = errors.New("dns: not a standard IN A/AAAA question")
	ErrBadQuestion   = errors.New("dns: question name is not a valid hostname")
	ErrNoAddresses   = errors.New("dns: no addresses for the question name")
	ErrTooLarge      = errors.New("dns: message larger than accepted")
	errMalformedRest = errors.New("dns: malformed answer section")
)

// Answer is one address a response bound to the question name.
type Answer struct {
	Addr netip.Addr
	TTL  uint32 // seconds; the minimum along the CNAME chain that led to it
}

// Response is what a DNS answer taught us: the name the client asked for and
// the addresses it was given for it.
type Response struct {
	// Name is the question name, normalized (see NormalizeName). This is the
	// name the application asked for, even when the answer went through a
	// CNAME chain: "api.stripe.com", not the CDN name it aliases.
	Name    string
	Answers []Answer
}

// head is a message's header and single question.
type head struct {
	id    uint16
	qtype dnsmessage.Type
	qname []byte          // normalized; aliases the caller's buffer
	raw   dnsmessage.Name // the question name as sent, for owner comparison
}

// parseHead parses the header and the one IN A/AAAA question, leaving p
// positioned at the answer section. It rejects everything that is not a
// standard query (wantResponse=false) or successful response (true) with
// exactly one such question. nameBuf receives the normalized question name
// and must hold maxNameLen+1 bytes. No allocation.
func parseHead(p *dnsmessage.Parser, msg []byte, nameBuf []byte, wantResponse bool) (head, error) {
	if len(msg) > maxDNSMessage {
		return head{}, ErrTooLarge
	}
	hdr, err := p.Start(msg)
	if err != nil {
		return head{}, err
	}
	if hdr.Response != wantResponse {
		if wantResponse {
			return head{}, ErrNotResponse
		}
		return head{}, ErrNotQuery
	}
	if hdr.OpCode != 0 || (wantResponse && hdr.RCode != dnsmessage.RCodeSuccess) {
		return head{}, ErrUnsupported
	}
	q, err := p.Question()
	if err != nil {
		return head{}, err
	}
	if q.Class != dnsmessage.ClassINET || (q.Type != dnsmessage.TypeA && q.Type != dnsmessage.TypeAAAA) {
		return head{}, ErrUnsupported
	}
	// Multiple questions in one message are legal on paper but no resolver
	// sends them, and attributing answers between them would be guesswork.
	if _, err := p.Question(); !errors.Is(err, dnsmessage.ErrSectionDone) {
		return head{}, ErrUnsupported
	}
	n, ok := normalizeInto(nameBuf, q.Name.Data[:q.Name.Length])
	if !ok {
		return head{}, ErrBadQuestion
	}
	return head{id: hdr.ID, qtype: q.Type, qname: nameBuf[:n], raw: q.Name}, nil
}

// ParseQuery extracts the transaction ID, normalized question name and type
// from a standard IN A/AAAA query.
func ParseQuery(msg []byte) (id uint16, name string, qtype dnsmessage.Type, err error) {
	var p dnsmessage.Parser
	var buf [maxNameLen + 1]byte
	h, err := parseHead(&p, msg, buf[:], false)
	if err != nil {
		return 0, "", 0, err
	}
	return h.id, string(h.qname), h.qtype, nil
}

type cnameRec struct {
	owner, target dnsmessage.Name
	ttl           uint32
}

type addrRec struct {
	owner dnsmessage.Name
	addr  netip.Addr
	ttl   uint32
}

type chainLink struct {
	name dnsmessage.Name
	ttl  uint32 // minimum TTL of the aliases that led here
}

// answerScratch is reusable working space for parseAnswers, so parsing a
// response allocates nothing. One per goroutine (the capture loop owns one).
type answerScratch struct {
	cnames [maxCNAMEsKept]cnameRec
	addrs  [maxAddrRecords]addrRec
	chain  [maxCNAMEChain + 1]chainLink
}

// equalFold compares two wire names ASCII-case-insensitively (DNS 0x20
// randomizes the question's case) without allocating.
func equalFold(a, b *dnsmessage.Name) bool {
	if a.Length != b.Length {
		return false
	}
	for i := 0; i < int(a.Length); i++ {
		x, y := a.Data[i], b.Data[i]
		if x >= 'A' && x <= 'Z' {
			x += 'a' - 'A'
		}
		if y >= 'A' && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// parseAnswers reads the answer section after parseHead and appends to out
// every address bound to the question name, directly or through a CNAME
// chain present in the same section. Records for any other name are ignored.
//
// A truncated (TC) or partially malformed answer section is not rejected
// outright: every record that parsed cleanly before the damage is still used
// (a stub resolver that receives a truncated answer retries over TCP, which
// this package cannot see).
func parseAnswers(p *dnsmessage.Parser, h *head, s *answerScratch, out []Answer) []Answer {
	ncn, nad := 0, 0
	for i := 0; i < maxRecordsScanned; i++ {
		rh, err := p.AnswerHeader()
		if err != nil {
			break // ErrSectionDone, or damage: keep what parsed cleanly
		}
		if rh.Class != dnsmessage.ClassINET {
			if p.SkipAnswer() != nil {
				break
			}
			continue
		}
		switch rh.Type {
		case dnsmessage.TypeA:
			// The length check is ours: the parser reads a fixed 4 bytes and
			// would accept a record whose declared length disagrees.
			if rh.Length != 4 {
				err = errMalformedRest
				break
			}
			var r dnsmessage.AResource
			if r, err = p.AResource(); err == nil && nad < len(s.addrs) {
				s.addrs[nad] = addrRec{rh.Name, netip.AddrFrom4(r.A), rh.TTL}
				nad++
			}
		case dnsmessage.TypeAAAA:
			if rh.Length != 16 {
				err = errMalformedRest
				break
			}
			var r dnsmessage.AAAAResource
			if r, err = p.AAAAResource(); err == nil && nad < len(s.addrs) {
				s.addrs[nad] = addrRec{rh.Name, netip.AddrFrom16(r.AAAA), rh.TTL}
				nad++
			}
		case dnsmessage.TypeCNAME:
			var r dnsmessage.CNAMEResource
			if r, err = p.CNAMEResource(); err == nil && ncn < len(s.cnames) {
				s.cnames[ncn] = cnameRec{rh.Name, r.CNAME, rh.TTL}
				ncn++
			}
		default:
			err = p.SkipAnswer()
		}
		if err != nil {
			break
		}
	}

	// Walk the CNAME chain from the question name. The first CNAME for an
	// owner wins; a second one is invalid DNS (RFC 1034 3.6.2) and must not
	// redirect the chain. Each link remembers the smallest TTL so far: an
	// address is only as fresh as the shortest-lived alias that led to it.
	s.chain[0] = chainLink{h.raw, ^uint32(0)}
	nch := 1
	for nch < len(s.chain) {
		cur := &s.chain[nch-1]
		var next *cnameRec
		for i := 0; i < ncn; i++ {
			if equalFold(&s.cnames[i].owner, &cur.name) {
				next = &s.cnames[i]
				break
			}
		}
		if next == nil {
			break
		}
		loop := false
		for i := 0; i < nch; i++ {
			if equalFold(&s.chain[i].name, &next.target) {
				loop = true
				break
			}
		}
		if loop {
			break
		}
		s.chain[nch] = chainLink{next.target, min(cur.ttl, next.ttl)}
		nch++
	}

	out = out[:0]
addrs:
	for i := 0; i < nad; i++ {
		a := &s.addrs[i]
		limit, onChain := uint32(0), false
		for j := 0; j < nch; j++ {
			if equalFold(&a.owner, &s.chain[j].name) {
				limit, onChain = s.chain[j].ttl, true
				break
			}
		}
		// Unmap before validating: ::ffff:0.0.0.0 is not "unspecified" as
		// IPv6 but becomes 0.0.0.0 once unmapped (found by fuzzing).
		addr := a.addr.Unmap()
		if !onChain || !addr.IsValid() || addr.IsUnspecified() {
			continue
		}
		ttl := min(a.ttl, limit)
		for k := range out {
			if out[k].Addr == addr {
				out[k].TTL = max(out[k].TTL, ttl)
				continue addrs
			}
		}
		if len(out) >= maxAddrsPerAnswer {
			break
		}
		out = append(out, Answer{Addr: addr, TTL: ttl})
	}
	return out
}

// ParseResponse extracts the question name and the A/AAAA addresses bound to
// it from a DNS response message. It is the stand-alone form of what the
// tracker does for a response that matched a query.
//
// Only standard successful queries (QR=1, OPCODE=QUERY, RCODE=NOERROR) with
// exactly one IN-class A or AAAA question are used, and answer records are
// only trusted if they belong to the question name or to a name reachable
// from it through CNAME records in the same answer section.
func ParseResponse(msg []byte) (Response, error) {
	var p dnsmessage.Parser
	var buf [maxNameLen + 1]byte
	h, err := parseHead(&p, msg, buf[:], true)
	if err != nil {
		return Response{}, err
	}
	var s answerScratch
	answers := parseAnswers(&p, &h, &s, nil)
	resp := Response{Name: string(h.qname), Answers: answers}
	if len(answers) == 0 {
		resp.Answers = nil
		return resp, ErrNoAddresses
	}
	return resp, nil
}
