package dnscache

import (
	"container/list"
	"net/netip"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	// QueryWindow is how long a query waits for its response. Stub resolvers
	// time out at 5s (glibc, musl) and retry with a fresh query.
	QueryWindow = 5 * time.Second

	// Bounds on outstanding queries: per client, so one busy or hostile pod
	// cannot crowd out the others, and in total.
	maxPendingPerClient = 128
	maxPendingTotal     = 16384
)

// queryKey identifies an outstanding query by everything a response must
// echo back: the client's address and port, the server address the query
// was sent to, and the transaction ID.
type queryKey struct {
	client, server netip.Addr
	port           uint16
	id             uint16
}

type pendingQuery struct {
	key   queryKey
	name  string
	qtype dnsmessage.Type
	at    time.Time
}

// pendingQueries is the table of queries seen leaving local clients. A
// response is believed only if it matches one entry on queryKey, question
// name and type, within QueryWindow - and it consumes that entry, so a query
// is answered at most once. Not safe for concurrent use: only the capture
// goroutine touches it.
type pendingQueries struct {
	byKey    map[queryKey]*list.Element
	order    *list.List // oldest first; queries arrive in time order
	perCount map[netip.Addr]int
}

func newPendingQueries() *pendingQueries {
	return &pendingQueries{
		byKey:    make(map[queryKey]*list.Element),
		order:    list.New(),
		perCount: make(map[netip.Addr]int),
	}
}

func (q *pendingQueries) len() int { return q.order.Len() }

func (q *pendingQueries) remove(el *list.Element) {
	pq := q.order.Remove(el).(*pendingQuery)
	delete(q.byKey, pq.key)
	if n := q.perCount[pq.key.client] - 1; n > 0 {
		q.perCount[pq.key.client] = n
	} else {
		delete(q.perCount, pq.key.client)
	}
}

// expire drops queries older than QueryWindow.
func (q *pendingQueries) expire(now time.Time) {
	for el := q.order.Front(); el != nil; el = q.order.Front() {
		if now.Sub(el.Value.(*pendingQuery).at) < QueryWindow {
			return
		}
		q.remove(el)
	}
}

// add records a query. It reports false when the query was not recorded
// because its client already has maxPendingPerClient outstanding or the table
// is full of live entries - in which case its response will not be believed,
// which only costs a name.
func (q *pendingQueries) add(k queryKey, name string, qtype dnsmessage.Type, now time.Time) bool {
	q.expire(now)
	if el, ok := q.byKey[k]; ok {
		// A retransmission reuses ID and port; keep the newest question.
		q.remove(el)
	}
	if q.perCount[k.client] >= maxPendingPerClient || q.order.Len() >= maxPendingTotal {
		return false
	}
	q.byKey[k] = q.order.PushBack(&pendingQuery{key: k, name: name, qtype: qtype, at: now})
	q.perCount[k.client]++
	return true
}

// take returns and removes the query a response answers, if any. qname is
// the response's normalized question name. A response with the right key but
// the wrong question does not consume the entry: it is not the answer, and
// the real one may still arrive.
func (q *pendingQueries) take(k queryKey, qname []byte, qtype dnsmessage.Type, now time.Time) (string, bool) {
	q.expire(now)
	el, ok := q.byKey[k]
	if !ok {
		return "", false
	}
	pq := el.Value.(*pendingQuery)
	if pq.qtype != qtype || pq.name != string(qname) {
		return "", false
	}
	q.remove(el)
	return pq.name, true
}
