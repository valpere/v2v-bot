package adapter

import (
	"context"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/valpere/v2v-bot/internal/engineclient"
	"github.com/valpere/v2v-bot/internal/telegram"
)

// recordingTick is how often the "recording voice" chat action is re-sent while
// a turn runs (Telegram shows it for ~5 s).
const recordingTick = 4 * time.Second

// topicCallbackPrefix is the inline-keyboard payload prefix for a topic pick: "topic:<id>".
const topicCallbackPrefix = "topic:"

// apologyLine is sent when the engine cannot be reached or fails; the user can resend.
const apologyLine = "Вибачте, щось пішло не так — спробуйте, будь ласка, ще раз за хвилину. / Sorry, something went wrong — please try again in a minute."

// pickerIntro sits above the topic buttons — a bilingual set of links to the
// owner's site (this bot is a portfolio / pitch artifact), one resource per row
// (UK · EN), then the prompt. Sent with HTML parse mode by SendButtons; keep the
// <a> tags balanced — Telegram rejects the whole message otherwise.
const pickerIntro = `<a href="https://valpere.github.io/uk/portfolio/">Мій портфоліо</a> · <a href="https://valpere.github.io/portfolio/">My Portfolio</a>
<a href="https://valpere.github.io/uk/projects/">Мої проєкти</a> · <a href="https://valpere.github.io/projects/">My Projects</a>
<a href="https://valpere.github.io/uk/mvb/">Мої брифи</a> · <a href="https://valpere.github.io/mvb/">My Briefs</a>
<a href="https://valpere.github.io/uk/about/">Про мене</a> · <a href="https://valpere.github.io/about/">About Me</a>

Оберіть тему розмови · Choose a topic:`

func (a *Adapter) send(ctx context.Context, chatID int64, text string) {
	if err := a.tg.SendText(ctx, chatID, text); err != nil {
		log.Printf("send (chat %d): %v", chatID, err)
	}
}

// startRecordingTicker shows the "recording voice" action immediately and
// re-sends it every recordingTick until stop() is called or ctx is cancelled.
func (a *Adapter) startRecordingTicker(ctx context.Context, chatID int64) (stop func()) {
	action := func() {
		if err := a.tg.SendRecordingAction(ctx, chatID); err != nil {
			log.Printf("recording action (chat %d): %v", chatID, err)
		}
	}
	action()

	done := make(chan struct{})
	go func() {
		t := time.NewTicker(recordingTick)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				action()
			}
		}
	}()

	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// handleUpdate maps one inbound update to an engine event, runs the turn and
// delivers the result. The per-chat worker calls it serially.
func (a *Adapter) handleUpdate(ctx context.Context, u telegram.Update) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("recovered panic (chat %d): %v", u.ChatID, r)
		}
	}()

	// an inline-keyboard tap always resolves before anything else, and is always
	// acknowledged — even when its payload is not one of ours or the engine is down.
	if u.CallbackID != "" {
		// Ack first: Telegram wants the answer promptly and the turn may take a while.
		if err := a.tg.AnswerCallback(ctx, u.CallbackID); err != nil {
			log.Printf("answer callback (chat %d): %v", u.ChatID, err)
		}
		var out engineclient.Output
		failed := false
		if id, ok := strings.CutPrefix(u.CallbackData, topicCallbackPrefix); ok && id != "" {
			out, failed = a.turn(ctx, u.ChatID, engineclient.Event{Pick: id}, false)
		} else {
			log.Printf("unknown topic callback (chat %d): %q", u.ChatID, u.CallbackData)
		}
		a.finish(ctx, u.ChatID, out, failed)
		return
	}

	var ev engineclient.Event
	busy := false
	switch {
	case u.VoiceFileID != "":
		v, err := a.fetchVoice(ctx, u)
		if err != nil {
			log.Printf("voice (chat %d): %v", u.ChatID, err)
			a.send(ctx, u.ChatID, apologyLine)
			return
		}
		ev, busy = engineclient.Event{Voice: v}, true
	case u.IsStart:
		ev = engineclient.Event{Start: true}
	default:
		ev = engineclient.Event{Text: u.Text}
		busy = u.Text != "" && !strings.HasPrefix(strings.TrimSpace(u.Text), "/")
	}
	out, failed := a.turn(ctx, u.ChatID, ev, busy)
	a.finish(ctx, u.ChatID, out, failed)
}

// turn calls the engine, showing the recording indicator while a slow turn is
// in flight. The call is never retried (it spends money and mutates the session).
func (a *Adapter) turn(ctx context.Context, chatID int64, ev engineclient.Event, busy bool) (engineclient.Output, bool) {
	if busy {
		defer a.startRecordingTicker(ctx, chatID)()
	}
	out, err := a.eng.Turn(ctx, chatID, ev)
	if err != nil {
		log.Printf("engine turn (chat %d): %v", chatID, err)
		return engineclient.Output{}, true
	}
	return out, false
}

func (a *Adapter) finish(ctx context.Context, chatID int64, out engineclient.Output, failed bool) {
	if failed {
		a.send(ctx, chatID, apologyLine)
		return
	}
	a.deliver(ctx, chatID, out)
}

// fetchVoice downloads the voice note into memory and removes the temp file.
func (a *Adapter) fetchVoice(ctx context.Context, u telegram.Update) (*engineclient.Voice, error) {
	path, err := a.tg.DownloadVoice(ctx, u.VoiceFileID)
	if err != nil {
		return nil, err
	}
	defer os.Remove(path)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return &engineclient.Voice{Seconds: u.VoiceSeconds, OGG: raw}, nil
}

// deliver sends the engine's messages to the chat, in order. A failed send is
// logged and never stops the rest.
func (a *Adapter) deliver(ctx context.Context, chatID int64, out engineclient.Output) {
	for _, m := range out.Messages {
		switch m.Kind {
		case engineclient.KindText:
			a.send(ctx, chatID, m.Text)
		case engineclient.KindVoice:
			if err := a.tg.SendVoice(ctx, chatID, m.OGG); err != nil {
				log.Printf("send voice (chat %d): %v", chatID, err)
			}
		case engineclient.KindPicker:
			buttons := make([]telegram.Button, len(m.Buttons))
			for i, b := range m.Buttons {
				buttons[i] = telegram.Button{Label: b.Label, Data: topicCallbackPrefix + b.Data}
			}
			if err := a.tg.SendButtons(ctx, chatID, pickerIntro, buttons); err != nil {
				log.Printf("send topic picker (chat %d): %v", chatID, err)
			}
		}
	}
}
