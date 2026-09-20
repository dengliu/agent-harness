'use client'

import { useState } from 'react'
import type { AgentEvent, WireExchange } from '../lib/agent'

/**
 * A step groups the events the agent emitted for one Thought/Action/Observation
 * round, so the UI reads as a numbered chain of reasoning rather than a flat
 * event log.
 */
export interface Step {
  n: number
  thought?: string
  tool?: string
  input?: string
  observation?: string
  ok?: boolean
  prompt?: string // the user half of the request that produced `raw`
  wire?: WireExchange // the raw HTTP exchange behind this step
  raw?: string // the unparsed model reply for this step
  genMs?: number // how long the model took
  toolMs?: number // how long the tool took
  promptBytes?: number // size of the prompt that produced `raw`
}

/** groupSteps folds the raw event stream into per-step cards. */
export function groupSteps(events: AgentEvent[]): Step[] {
  const steps: Step[] = []
  const at = (n: number) => {
    let s = steps.find((s) => s.n === n)
    if (!s) {
      s = { n }
      steps.push(s)
    }
    return s
  }
  for (const e of events) {
    if (!e.step) continue
    const s = at(e.step)
    if (e.type === 'trace') {
      s.prompt = e.prompt
      s.wire = e.wire
      s.raw = e.text
      s.genMs = e.elapsedMs
      s.promptBytes = e.promptBytes
    }
    if (e.type === 'thought') s.thought = e.text
    if (e.type === 'action') {
      s.tool = e.tool
      s.input = e.input
    }
    if (e.type === 'observation') {
      s.observation = e.text
      s.ok = e.ok
      s.toolMs = e.elapsedMs
    }
  }
  return steps.sort((a, b) => a.n - b.n)
}

/**
 * TracePanel shows what the model actually returned for this step, before the
 * parser touched it. Everything in the left-hand column is an interpretation of
 * this text; keeping it visible is what lets you check that reading — and makes
 * a malformed block legible instead of mysterious.
 *
 * It folds only when it has to. The step's height is set by the reasoning on
 * the left, so a step with a long observation leaves room for the whole reply
 * and there is nothing to hide; a short step does not, and only then does the
 * panel clip and offer to unfold. Nothing is clamped to a fixed line count,
 * because the space available is not fixed either.
 */
function TracePanel({ step, system }: { step: Step; system?: string }) {
  const [tab, setTab] = useState<Tab>('response')
  const [open, setOpen] = useState(false)

  if (!step.raw) return null

  // The request as it went out: the system instruction, which is the same on
  // every turn and so arrives once, then this step's question-plus-scratchpad.
  const request = [system, step.prompt].filter(Boolean).join('\n\n───\n\n')
  const body =
    tab === 'request'
      ? request
      : tab === 'raw'
        ? formatWire(step.wire)
        : step.raw

  // One fold state for the whole section: the three views are three readings of
  // the same exchange, so folding any of them folds all of them. Picking a tab
  // opens the section, which makes the tab strip the way in as well as the way
  // between.
  const show = (next: Tab) => {
    setTab(next)
    setOpen(true)
  }

  const tabButton = (name: Tab, title?: string) => (
    <button
      type="button"
      className={`tab${open && tab === name ? ' on' : ''}`}
      aria-pressed={open && tab === name}
      onClick={() => show(name)}
      title={title}
    >
      {name}
    </button>
  )

  return (
    <div className={`step-trace${open ? ' open' : ''}`}>
      <div className="trace-head">
        <button
          type="button"
          className="trace-fold"
          aria-expanded={open}
          onClick={() => setOpen((v) => !v)}
          title={
            open ? 'Fold the exchange' : 'Show the exchange with the model'
          }
        >
          <span className="caret" aria-hidden="true">
            {open ? '▾' : '▸'}
          </span>
          trace
        </button>
        {tabButton('request')}
        {tabButton('response')}
        {step.wire &&
          tabButton(
            'raw',
            'The JSON actually sent to and returned by the model API',
          )}
        <span className="trace-cost mono" title="model latency · request size">
          {formatCost(step)}
        </span>
      </div>

      {open && <pre className="trace-raw mono">{body}</pre>}
    </div>
  )
}

type Tab = 'request' | 'response' | 'raw'

/**
 * formatWire renders the HTTP exchange as it happened. The payloads arrive as
 * the exact bytes sent and received; they are re-indented here only so a 4 kB
 * single-line JSON body is readable, and left untouched if they do not parse.
 */
function formatWire(wire?: WireExchange): string {
  if (!wire) return ''
  const pretty = (s?: string) => {
    if (!s) return '(empty)'
    try {
      return JSON.stringify(JSON.parse(s), null, 2)
    } catch {
      return s
    }
  }
  return [
    `── request ─── ${wire.url ?? ''}`,
    pretty(wire.request),
    '',
    `── response ── ${wire.status ?? ''}`,
    pretty(wire.response),
  ].join('\n')
}

function formatCost(step: Step): string {
  const parts: string[] = []
  if (step.genMs !== undefined) parts.push(`${(step.genMs / 1000).toFixed(1)}s`)
  if (step.promptBytes) parts.push(`${(step.promptBytes / 1000).toFixed(1)}kB`)
  return parts.join(' · ')
}

export function TraceStep({ step, system }: { step: Step; system?: string }) {
  return (
    <div className="step">
      <div className="step-main">
        <div className="stepno">step {step.n}</div>
        {step.thought && (
          <div className="row">
            <div className="label thought">Thought</div>
            <div className="body">{step.thought}</div>
          </div>
        )}
        {step.tool && (
          <div className="row">
            <div className="label action">Action</div>
            <div className="body call mono">
              <span className="tool-name">{step.tool}</span>({step.input ?? ''})
            </div>
          </div>
        )}
        {step.observation !== undefined && (
          <div className="row">
            <div className={`label ${step.ok ? 'ok' : 'bad'}`}>
              {step.ok ? 'Observation' : 'Failed'}
              {step.toolMs !== undefined && step.toolMs > 0 && (
                <span className="tool-ms mono">{step.toolMs}ms</span>
              )}
            </div>
            <div className="body obs mono">{step.observation}</div>
          </div>
        )}
      </div>

      <TracePanel step={step} system={system} />
    </div>
  )
}
