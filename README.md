# v2v-bot — Telegram-фронтенд голосового/текстового AI-асистента

Тонкий адаптер каналу: приймає оновлення Telegram (long-poll), передає кожне
рушію через HTTP (`POST /v1/chats/{id}/turn`) і доставляє відповідь — текст,
голос, вибір теми. Уся логіка розмови (сесії, теми, ліміти, STT, діалог із
заземленням, TTS, логи, ліди) живе в рушії
[v2v-demo](https://github.com/valpere/v2v-demo).

**Статус: каркас.** Коду ще немає; план і вимоги — [`docs/HANDOFF.md`](docs/HANDOFF.md).

```
Telegram ──► v2v-bot ──HTTP──► рушій (v2v-demo)
```

Інші фронтенди того ж рушія: [v2v-tma](https://github.com/valpere/v2v-tma) (Telegram Mini App).

## Ліцензія

Код — [Apache-2.0](LICENSE).
