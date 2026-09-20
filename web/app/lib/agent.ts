// Types and the SSE client for the Go agent's /api endpoints.

export type EventType =
  'start' | 'trace' | 'thought' | 'action' | 'observation' | 'final' | 'error'

export interface WireExchange {
  url?: string
  status?: string
  request?: string
  response?: string
}

export interface AgentEvent {
  type: EventType
  step?: number
  text?: string
  tool?: string
  input?: string
  ok?: boolean
  elapsedMs?: number
  system?: string
  prompt?: string
  promptBytes?: number
  wire?: WireExchange
}

export interface ToolInfo {
  name: string
  description: string
}

export async function fetchTools(): Promise<{
  model: string
  tools: ToolInfo[]
}> {
  const res = await fetch('/api/tools')
  if (!res.ok) throw new Error(`tools: ${res.status}`)
  return res.json()
}

/**
 * runAgent POSTs a question and yields each trace event as the Go server
 * streams it. The endpoint is Server-Sent Events over a POST body, so this
 * parses the stream by hand rather than using EventSource (which is GET-only).
 */
export async function runAgent(
  question: string,
  onEvent: (e: AgentEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const res = await fetch('/api/run', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ question }),
    signal,
  })
  if (!res.ok || !res.body) {
    const detail = await res.text().catch(() => '')
    throw new Error(detail || `agent request failed: ${res.status}`)
  }

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''

  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })

    // Frames are separated by a blank line; a partial frame stays in buffer.
    let sep: number
    while ((sep = buffer.indexOf('\n\n')) !== -1) {
      const frame = buffer.slice(0, sep)
      buffer = buffer.slice(sep + 2)
      const data = frame
        .split('\n')
        .filter((l) => l.startsWith('data:'))
        .map((l) => l.slice(5).trim())
        .join('\n')
      if (!data) continue // keepalive comment
      try {
        onEvent(JSON.parse(data) as AgentEvent)
      } catch {
        // Ignore a frame we cannot parse rather than killing the run.
      }
    }
  }
}
