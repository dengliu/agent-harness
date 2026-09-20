# ReAct agent — Go + GPT-5 + Next.js

A small, complete demo of the **ReAct** pattern, written to show how an agent
harness works where nothing is hidden. A language model answers a question by
alternating between reasoning in prose and calling tools:

```
Thought → Action → Observation → Thought → … → Final Answer
```

The tool protocol is this repository's, not the provider's: tools are described
to the model in the system prompt, it asks for them in text, and the Go loop
parses that text and runs them. The model never writes an `Observation` — the
loop does, from a real tool. That split is the whole point: it reasons over
evidence instead of inventing it.

## Run it

```bash
export OPEN_API_KEY=...            # https://platform.openai.com/api-keys

# one-shot, in the terminal
go run . -q "How many years ago was the novel Kokoro published?"
go run . -v -q "..."               # also print the raw prompts and replies

# API server on :8080
go run .

# web UI on :3000 (proxies /api to :8080)
cd web && npm install && npm run dev
```

Set `OPENAI_MODEL` to use a model other than `gpt-5`. `OPENAI_API_KEY` is
accepted as an alternative to `OPEN_API_KEY`.

### …or in containers

Both services run from this working tree — no image is built, the code is
bind-mounted into stock `golang` and `node` images and executed there. The Go
side runs under [air](https://github.com/air-verse/air), so an edit on the host
rebuilds and restarts the agent in about two seconds; Next.js hot-reloads the
same way.

```bash
export OPEN_API_KEY=...         # or put it in a .env file next to compose.yaml
make up                         # UI on :3000, API on :8080
```

| Target | What it does |
|---|---|
| `make up` | start both services and stream their logs |
| `make up-d` | same, in the background |
| `make down` | stop them |
| `make logs` / `make ps` | follow logs / show status |
| `make test` | run `go test ./...` in a throwaway container |
| `make ask Q="…"` | one-shot question in the terminal, in the container |
| `make sh` | shell into the agent container |
| `make clean` | stop and drop the caches and `node_modules` volumes |

`make help` lists them.

## Layout

| Path | What it is |
|---|---|
| `main.go` | CLI and server wiring, terminal trace printer |
| `react/agent.go` | the loop: generate → parse → run tool → append to scratchpad |
| `react/prompt.go` | what we send: the system prompt, the conversation, the scratchpad |
| `react/parse.go` | what we read back: turning the model's prose into a decision |
| `react/tool.go`, `react/tools_builtin.go` | the `Tool` interface and the demo tools |
| `react/openai.go` | the GPT-5 binding behind a one-method `Model` interface |
| `react/wire.go` | a capturing transport that keeps the raw HTTP exchange |
| `server/server.go` | `/api/run` streams the trace as Server-Sent Events |
| `web/` | Next.js UI that renders each step as it arrives |

## Tools

| Tool | Input | Why it exists |
|---|---|---|
| `calculator` | an arithmetic expression | models decide *what* to compute better than they compute |
| `clock` | an optional IANA timezone | a model has no idea what "today" means |
| `wikipedia` | a search phrase | facts the model should look up rather than recall |

Adding one is a few lines — call `react.NewTool(name, description, fn)` and pass
it to `react.DefaultToolbox`. The description you write *is* the interface: it
is the only thing the model reads when deciding whether to call it, so name the
input format and give an example.

## Design notes

- **The loop is stateless.** Every call rebuilds the whole conversation from the
  scratchpad, so a run is trivially inspectable and resumable, and there is no
  hidden conversation state to get out of sync.
- **Tools are described, not declared.** The system prompt's catalogue is the
  whole interface; nothing is registered with the provider. The parser is
  deliberately forgiving, because a rejected reply costs a round trip — see the
  table in design.md §6.
- **The model never writes an `Observation`.** A stop sequence would enforce
  that at the API, but GPT-5 rejects the `stop` parameter, so `parse.go` carries
  it: everything from a model-written `Observation:` onwards is discarded and
  never reaches the scratchpad.
- **Failures are observations, not errors.** An unknown tool, a bad expression
  or an HTTP timeout is fed back as text the model can read and recover from. A
  malformed reply likewise gets one corrective turn. Only the step limit
  (`-max-steps`, default 8) ends a run.
- **Observations are truncated** before they re-enter the prompt, so a chatty
  tool cannot grow the context quadratically.
- **`Model` is one method.** `react.Model` is
  `Generate(ctx, system, turns, stop)` — the provider is never told tools exist
  — so the tests drive the whole loop with a scripted fake and no API key:
  `go test ./react/`.

## API

```bash
curl -s localhost:8080/api/tools
curl -N localhost:8080/api/run -H 'Content-Type: application/json' \
     -d '{"question":"what time is it in Tokyo?"}'
```

`/api/run` responds with an SSE stream of `start`, `trace`, `thought`, `action`,
`observation`, `final` and `error` events; the UI in `web/app/page.tsx` groups
them into per-step cards. The `trace` event carries both halves of that step's
exchange with the model — the request (`prompt`, joined to the system
instruction sent once on `start`) and the unparsed reply — plus `wire`, the exact
JSON posted to the OpenAI API and the exact JSON it returned, with the API key
redacted. The UI shows these in a foldable section under each step, as
`request` / `response` / `raw` tabs sharing one fold state — folded to a single
line by default, opened by the caret or by picking a tab.
