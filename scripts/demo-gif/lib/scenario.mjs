// Topology churn for the recording.
//
// The v3.0 demo simulator (backend/service/demo) seeds a fixed topology and
// never changes its shape, so history has nothing to show: time travel looks
// identical at every instant and "Compare to now" reports 0 changes. To give
// those features something real to display, this module plays the part of an
// agent and reports a *new* workload through the public ingest API
// (POST /api/v1/events and /api/v1/stats) part-way through the warm-up - the
// same path a real eBPF agent uses. No application code is involved.
//
// The new workload is a batch job that appears on demo-app-1 and starts
// talking to postgres and kafka. Everything is configurable from the
// storyboard (storyboard.mjs -> scenario).

const log = (...a) => console.log('[scenario]', ...a)

async function post(backendUrl, path, body) {
  const res = await fetch(`${backendUrl}/api/v1${path}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) throw new Error(`POST ${path} -> HTTP ${res.status}: ${await res.text()}`)
}

/**
 * Starts reporting `workload` and keeps it alive (re-reported every
 * `refreshMs`) until the returned stop() is called.
 */
export async function startWorkload(backendUrl, workload, { refreshMs = 2000 } = {}) {
  const { node, ip, comm, calls } = workload
  const totals = calls.map(() => ({ sent: 0, recv: 0 }))

  const report = async (withEvents) => {
    if (withEvents) {
      await post(backendUrl, '/events', {
        node_name: node,
        timestamp: new Date().toISOString(),
        events: calls.map(c => ({
          pid: 4242, comm, src_addr: ip, dst_addr: c.ip, dst_port: c.port, direction: 0, protocol: 'tcp',
        })),
      })
    }
    await post(backendUrl, '/stats', {
      node_name: node,
      timestamp: new Date().toISOString(),
      interval_ms: refreshMs,
      connections: calls.map((c, i) => {
        const jitter = () => 0.6 + Math.random() * 0.8
        const sentRate = c.sentKBps * 1024 * jitter()
        const recvRate = c.recvKBps * 1024 * jitter()
        totals[i].sent += sentRate * refreshMs / 1000
        totals[i].recv += recvRate * refreshMs / 1000
        return {
          pid: 4242, comm, src_addr: ip, dst_addr: c.ip, src_port: 40000 + i, dst_port: c.port, protocol: 'tcp',
          bytes_sent: Math.round(totals[i].sent), bytes_recv: Math.round(totals[i].recv),
          bytes_sent_rate: sentRate, bytes_recv_rate: recvRate,
          packets_sent: Math.round(totals[i].sent / 1400), packets_recv: Math.round(totals[i].recv / 1400),
        }
      }),
    })
  }

  await report(true)
  log(`reported new workload "${comm}" on ${node} -> ${calls.map(c => `${c.ip}:${c.port}`).join(', ')}`)
  // Connection events are sent once only. Each one also upserts the
  // *destination* service under the name an agent would give it (":5432"),
  // which the simulator only corrects on its next 60 s service refresh - so
  // re-sending them would keep renaming postgres/kafka on screen. Stats alone
  // keep the edges' rates live and the history intervals open, and the run
  // is far shorter than the 30 min prune window.
  const timer = setInterval(() => {
    report(false).catch(err => log('refresh failed:', err.message))
  }, refreshMs)
  return { stop: () => clearInterval(timer) }
}
