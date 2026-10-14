# v2v-bot — Telegram-фронтенд голосового/текстового AI-асистента

Тонкий адаптер каналу: приймає оновлення Telegram (long-poll), передає кожне
рушію через HTTP (`POST /v1/chats/{id}/turn`) і доставляє відповідь — текст,
голос, вибір теми. Уся логіка розмови (сесії, теми, ліміти, STT, діалог із
заземленням, TTS, логи, ліди) живе в рушії
[v2v-demo](https://github.com/valpere/v2v-demo).

**Статус:** перша реалізація; ще не перевірена наживо і не замінила бота в рушії. Вимоги й план переходу — [`docs/HANDOFF.md`](docs/HANDOFF.md).

## Запуск

1. Рушій (v2v-demo): `cmd/engine` з `API_ADDR=127.0.0.1:8090` і `API_KEY` (≥ 32 символи).
2. Тут: `.env` за зразком `.env.example` (`TELEGRAM_BOT_TOKEN` — **окремий** токен, не живого бота: один poller на токен; `ENGINE_URL`, `ENGINE_KEY` = `API_KEY` рушія).
3. `make run`. Перевірка: `make check`.

```
Telegram ──► v2v-bot ──HTTP──► рушій (v2v-demo)
```

Інші фронтенди того ж рушія: [v2v-tma](https://github.com/valpere/v2v-tma) (Telegram Mini App).

## Ліцензія

Код — [Apache-2.0](LICENSE).
