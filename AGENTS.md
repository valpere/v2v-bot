# AGENTS.md — v2v-bot

The **Telegram frontend** of the v2v voice/text assistant. It is a thin channel
adapter: it long-polls Telegram, turns each update into one call to the engine's
channel API, and delivers the returned messages (text, voice, topic picker). It
holds **no conversation logic** — sessions, topics, spend limits, STT, the
grounded dialogue, TTS, turn/lead logs all live in the engine
(`~/wrk/demo/v2v-demo/v2v-demo`, package `internal/engine`).

**Status: skeleton.** There is no Go code yet. Start with `docs/HANDOFF.md`: it
lists what to port from v2v-demo, the contract to implement against
(`docs/channel-api.md` in v2v-demo), the behaviours that must survive the move,
and the cutover plan for the live bot.

## Architecture

```
Telegram ──long-poll──►  v2v-bot  ──HTTP /v1/chats/{id}/turn──►  engine (v2v-demo)
   ▲                        │                                       │
   └── text/voice/picker ◄──┘◄────────── messages[], signal ◄───────┘
```

## Conventions
- Go 1.26+, standard library first; at most one dependency per concern
  (`github.com/go-telegram/bot` for the transport is the known one).
- `make check` (gofmt, vet, `go test -race`, build) must pass before every commit.
  Test first for bug fixes; never rewrite tests together with the code they cover.
- No secrets in code or logs; config from env (`.env.example`): `TELEGRAM_BOT_TOKEN`,
  `ENGINE_URL`, `ENGINE_KEY`. Never commit `.env*`.
- The repo is **public**: do not describe an open weakness in commits, issues or
  tracked docs before it is fixed.
- Work on a branch and open a PR (`<type>-<slug>`); one commit per logical change.
- `./tmp/` is gitignored scratch; scripts you reuse go to `./minions/` (Bash, not Python).
- Do not write to other repositories (v2v-demo, v2v-tma) without explicit approval
  from Valentyn; write instructions into `./tmp/<project>-<topic>.md` instead.
- Communicate in Ukrainian; code, identifiers and comments in English.

## Guardrails
1. **No conversation logic here.** If you need to decide what the assistant says,
   that is an engine change — write it up for the engine's repo.
2. **`POST …/turn` is not idempotent** (it spends money and mutates the session):
   never retry it blindly. On a transport error or timeout send the adapter's own
   fixed apology line and move on.
3. **One chat's events are delivered to the engine strictly in arrival order**,
   one at a time; different chats run in parallel.
4. Every callback query must be acknowledged, even one with an unknown payload.
