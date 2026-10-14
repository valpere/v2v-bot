# Handoff — building v2v-bot

State on 2026-10-14: **empty skeleton** (docs + license + Makefile). The engine
side is done in a PR on v2v-demo; this repo has to become the Telegram adapter.

## 0. Prerequisites (check first, do not assume)
1. v2v-demo PR *"Extract internal/engine from cmd/bot; channel-adapter HTTP API"*
   (https://github.com/valpere/v2v-demo/pull/1) is merged. Read its
   `docs/channel-api.md` — that is the contract this repo implements. If it is
   not merged, read the contract from the branch `engine-extraction`.
2. Know how the current bot behaves: `~/wrk/demo/v2v-demo/v2v-demo/cmd/bot/`
   (adapter, ~250 lines of real logic) and `internal/telegram/` (transport).
   The adapter's tests in `cmd/bot/handler_test.go` are the behavioural spec.

## 1. What to build
```
cmd/v2v-bot/main.go        config, wiring, the update loop, graceful shutdown
internal/telegram/         PORT from v2v-demo/internal/telegram (transport only)
internal/engineclient/     NEW — HTTP client for the channel API
internal/adapter/          NEW — Update -> engine call -> deliver (the old handler.go)
```
Port `internal/telegram` as is (it already has retries, 4096-char splitting,
voice download with a size cap and `safeFilePath`, and tests) — it is the part
worth keeping verbatim. Copy files, do not import v2v-demo (`internal/` cannot be
imported from another module).

### Mapping Telegram update -> engine call
| Telegram update | `POST /v1/chats/{chat_id}/turn` body |
|---|---|
| `/start` | `{"start": true}` |
| inline button tap, data `topic:<id>` | `{"pick": "<id>"}` — **ack the callback first or regardless** |
| callback with any other data | log, ack, no engine call |
| text message | `{"text": "…"}` (slash commands `/voice a|b`, `/reset` included — the engine handles them) |
| voice note | download to memory/temp, `{"voice": {"seconds": N, "ogg_b64": "…"}}` |

Check Telegram's metadata (`VoiceSeconds`, `VoiceBytes`) is **not** needed on your
side: the engine enforces the caps — but do keep the hard download cap (10 MB) so
you never read an unbounded file.

### Delivering the response
Walk `messages` in order: `text` -> SendText; `voice` -> base64-decode ->
SendVoice (voice always precedes its text); `picker` -> SendButtons with
the **portfolio-links HTML intro** (copy `pickerIntro` from
`v2v-demo/cmd/bot/handler.go` verbatim, HTML parse mode, keep the `<a>` tags
balanced — Telegram rejects the whole message otherwise) and each button's
callback data `"topic:" + data`. An empty `messages` list is valid: send nothing.
A failed send is logged and never stops the rest.

## 2. Behaviours that must survive the move
- **Per-chat FIFO.** One serial worker per chat, fed by a 64-slot channel; a full
  queue drops the update and tells the sender once per 30 s window
  (`slowDownLine` in `v2v-demo/cmd/bot/main.go`); idle workers are evicted after
  15 min; `WaitGroup` + ordered shutdown (stop polling, wait for workers).
  Copy this logic and its tests from `v2v-demo/cmd/bot/main.go` / `main_test.go`.
- **"Recording voice" indicator** re-sent every 4 s while a request is in flight.
  The old bot showed it only during STT and during dialogue+TTS. The channel API
  does not expose phases, so show it while a *voice* turn or a non-command text
  turn is in flight (slightly more often than before — accepted; if it bothers
  anyone, ask for a `speaks` hint in the engine API rather than guessing).
- **Never retry `…/turn`.** Timeout = engine `TURN_TIMEOUT` (120 s) + margin, so
  150 s. On error/timeout/5xx send one fixed bilingual apology line (UK/EN, like
  the engine's) — the user can resend.
- **Recover from panics per update**; a bad update never stops the worker.
- **Order of side effects** for a callback: engine call -> ack -> deliver
  (the old bot acked before sending the greeting; acking just before delivery is
  equivalent and must still happen when the engine call fails).

## 3. Tests (write them before the code they cover)
Port the adapter-relevant cases of `v2v-demo/cmd/bot/handler_test.go`, replacing
the engine with an `httptest` server and Telegram with the same `fakeTG` pattern:
picker -> pick -> greeting; unknown callback is acked; voice order (voice then
text); voice upload size; `/reset`; ordering under load; queue overflow notice;
engine 5xx/timeout -> one apology and the worker survives; callback acked even
when the engine is down. Add a test that the turn call is **not** retried.

## 4. Engine-side follow-ups (not this repo — write them up, do not edit v2v-demo)
- **API-only engine binary.** Today the API is started inside v2v-demo's
  `cmd/bot` (needs `TELEGRAM_BOT_TOKEN` and also polls Telegram). Cutover needs
  `cmd/engine` that serves only the API. Instructions are in
  `~/wrk/demo/v2v-demo/v2v-demo/tmp/engine-binary.md` (if it is missing, it is
  the first thing to write there, then ask Valentyn).
- Remove `internal/telegram` and the Telegram adapter from v2v-demo only after
  v2v-bot has run live without regressions.

## 5. Local development
1. Run the engine with a **different** bot token than the live bot (two pollers
   on one token = 409): in v2v-demo `.env` set `API_ADDR=127.0.0.1:8090`,
   `API_KEY=$(openssl rand -hex 24)` and a dev `TELEGRAM_BOT_TOKEN`, then
   `make run-text` (text-only: `STT_BACKEND=none TTS_BACKEND=none`).
   Its own Telegram poller is then a second consumer of the dev token's *other*
   bot — or use `cmd/engine` once it exists, which is the clean way.
2. In this repo: `.env` with a second dev token, `ENGINE_URL`, `ENGINE_KEY`.
3. Smoke: `/start` -> picker -> tap a topic -> greeting -> a question -> reply;
   send a voice note; `/reset`; `/voice b`. v2v-demo's `docs/smoke-test.md` is the
   checklist; the voice rows need a human ear.

## 6. Cutover (a separate, supervised step — do not do it unprompted)
Live host: Oracle Always Free Micro, `ssh v2vdemo`, systemd `v2v-demo`
(`docs/deploy.md` in v2v-demo; bot idles ~140 MB RSS on 1 GB — a second small Go
process is fine). Plan: deploy `cmd/engine` + v2v-bot as two units, engine bound
to loopback with the key in an `EnvironmentFile` (0600), **stop** the old unit
before starting the new poller (one poller per token), verify with the smoke
list, keep the old binary for rollback. Valentyn decides the moment.

## 7. Definition of done for the first PR
`make check` green; a fake-engine integration test covering the table in §1; the
smoke list in §5 passes against a real engine on a dev token; `README` says how
to run it; nothing from v2v-demo copied except `internal/telegram` and the
documented constants.
