'use client'

import type { ToolInfo } from '../lib/agent'

/**
 * The tool list is always on screen, not just before a run, because it is the
 * agent's entire vocabulary: it explains what the model can reach for, and
 * during a run it shows what it actually reached for.
 */
export function ToolList({
  tools,
  uses,
  unreachable,
}: {
  tools: ToolInfo[]
  uses: Record<string, number>
  unreachable?: boolean
}) {
  return (
    <aside className="sidebar">
      <h2 className="side-title">Tools</h2>

      {unreachable && <p className="side-note">Agent unreachable.</p>}
      {!unreachable && tools.length === 0 && (
        <p className="side-note">Loading…</p>
      )}

      <div className="tool-list">
        {tools.map((t) => {
          const n = uses[t.name] ?? 0
          return (
            <div className={`tool${n > 0 ? ' used' : ''}`} key={t.name}>
              <div className="tool-head">
                <span className="name mono">{t.name}</span>
                {n > 0 && <span className="uses">{n}×</span>}
              </div>
              <p className="desc" title={t.description}>
                {t.description}
              </p>
            </div>
          )
        })}
      </div>

      {tools.length > 0 && (
        <p className="side-note">
          The agent chooses these on its own. The descriptions above are exactly
          what the model reads when deciding.
        </p>
      )}
    </aside>
  )
}
