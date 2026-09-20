'use client'

import { useCallback, useEffect, useRef, useState } from 'react'
import { groupSteps, TraceStep } from './components/Trace'
import { ToolList } from './components/Tools'
import {
  fetchTools,
  runAgent,
  type AgentEvent,
  type ToolInfo,
} from './lib/agent'

const EXAMPLES = [
  'How many years ago was the novel Kokoro published?',
  'What is 17% of 2,340, and how does that compare to the number of moons Jupiter has?',
  'What time is it in Tokyo, and how many hours until midnight there?',
  'Who invented the Kalman filter, and how old would they be today?',
]

export default function Home() {
  const [question, setQuestion] = useState('')
  const [events, setEvents] = useState<AgentEvent[]>([])
  const [running, setRunning] = useState(false)
  const [error, setError] = useState('')
  const [model, setModel] = useState('')
  const [tools, setTools] = useState<ToolInfo[]>([])
  const [unreachable, setUnreachable] = useState(false)
  const abortRef = useRef<AbortController | null>(null)
  const endRef = useRef<HTMLDivElement | null>(null)

  useEffect(() => {
    fetchTools()
      .then((d) => {
        setModel(d.model)
        setTools(d.tools ?? [])
      })
      .catch(() => {
        setUnreachable(true)
        setError(
          'Cannot reach the Go agent. Start it with `make up`, or `go run .` on :8080.',
        )
      })
  }, [])

  // Keep the newest step in view while the trace grows.
  useEffect(() => {
    endRef.current?.scrollIntoView({ behavior: 'smooth', block: 'end' })
  }, [events.length])

  const ask = useCallback(async (q: string) => {
    const trimmed = q.trim()
    if (!trimmed) return
    abortRef.current?.abort()
    const ac = new AbortController()
    abortRef.current = ac

    setEvents([])
    setError('')
    setRunning(true)
    try {
      await runAgent(
        trimmed,
        (e) => setEvents((prev) => [...prev, e]),
        ac.signal,
      )
    } catch (err) {
      if (!ac.signal.aborted) {
        setError(err instanceof Error ? err.message : String(err))
      }
    } finally {
      if (!ac.signal.aborted) setRunning(false)
    }
  }, [])

  const stop = () => {
    abortRef.current?.abort()
    setRunning(false)
  }

  const steps = groupSteps(events)
  const uses = events.reduce<Record<string, number>>((acc, e) => {
    if (e.type === 'action' && e.tool) acc[e.tool] = (acc[e.tool] ?? 0) + 1
    return acc
  }, {})
  const start = events.find((e) => e.type === 'start')
  const asked = start?.text
  const final = events.find((e) => e.type === 'final')
  const failed = events.find((e) => e.type === 'error')

  return (
    <main className="page">
      <header className="masthead">
        <div>
          <h1>ReAct Agent</h1>
          <div className="sub">
            Go + GPT-5, reasoning out loud: thought → action → observation
          </div>
        </div>
        {model && <span className="pill mono">{model}</span>}
      </header>

      <div className="shell">
        <ToolList tools={tools} uses={uses} unreachable={unreachable} />

        <div className="main">
          <form
            className="ask"
            onSubmit={(e) => {
              e.preventDefault()
              ask(question)
            }}
          >
            <textarea
              value={question}
              placeholder="Ask something that needs a lookup, a date, or some arithmetic…"
              onChange={(e) => setQuestion(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && !e.shiftKey) {
                  e.preventDefault()
                  ask(question)
                }
              }}
              disabled={running}
            />
            {running ? (
              <button type="button" onClick={stop}>
                Stop
              </button>
            ) : (
              <button type="submit" disabled={!question.trim()}>
                Ask
              </button>
            )}
          </form>

          <div className="examples">
            {EXAMPLES.map((e) => (
              <button
                key={e}
                type="button"
                disabled={running}
                onClick={() => {
                  setQuestion(e)
                  ask(e)
                }}
              >
                {e}
              </button>
            ))}
          </div>

          {(asked || error) && (
            <div className="trace">
              {asked && <div className="question">{asked}</div>}

              {steps.map((s) => (
                <TraceStep key={s.n} step={s} system={start?.system} />
              ))}

              {running && (
                <div className="working">
                  <span className="dot" />
                  thinking…
                </div>
              )}

              {final && (
                <div className="answer">
                  <h2>Final answer</h2>
                  <div className="text">{final.text}</div>
                  <div className="meta">
                    {final.step} steps ·{' '}
                    {((final.elapsedMs ?? 0) / 1000).toFixed(1)}s
                  </div>
                </div>
              )}

              {(failed || error) && (
                <div className="error">{failed?.text ?? error}</div>
              )}
              <div ref={endRef} />
            </div>
          )}
        </div>
      </div>
    </main>
  )
}
