package adapter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/valpere/v2v-bot/internal/engineclient"
	"github.com/valpere/v2v-bot/internal/telegram"
)

type fakeTG struct {
	mu        sync.Mutex
	log       []string // "text:<t>", "voice", "buttons", "ack:<id>", "rec" in call order
	texts     map[int64][]string
	buttons   [][]telegram.Button
	voicePath string
}

func (f *fakeTG) rec(s string) {
	f.mu.Lock()
	f.log = append(f.log, s)
	f.mu.Unlock()
}
func (f *fakeTG) Updates(context.Context) (<-chan telegram.Update, error) { return nil, nil }
func (f *fakeTG) DownloadVoice(context.Context, string) (string, error) {
	if f.voicePath == "" {
		return "", errors.New("no file")
	}
	return f.voicePath, nil
}
func (f *fakeTG) SendVoice(context.Context, int64, []byte) error { f.rec("voice"); return nil }
func (f *fakeTG) SendRecordingAction(context.Context, int64) error {
	f.rec("rec")
	return nil
}
func (f *fakeTG) SendButtons(_ context.Context, _ int64, _ string, b []telegram.Button) error {
	f.mu.Lock()
	f.buttons = append(f.buttons, b)
	f.mu.Unlock()
	f.rec("buttons")
	return nil
}
func (f *fakeTG) AnswerCallback(_ context.Context, id string) error { f.rec("ack:" + id); return nil }
func (f *fakeTG) SendText(_ context.Context, chat int64, text string) error {
	f.mu.Lock()
	if f.texts == nil {
		f.texts = map[int64][]string{}
	}
	f.texts[chat] = append(f.texts[chat], text)
	f.mu.Unlock()
	f.rec("text:" + text)
	return nil
}
func (f *fakeTG) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.log...)
}
func (f *fakeTG) textsTo(chat int64) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.texts[chat]...)
}

type call struct {
	chat int64
	ev   engineclient.Event
}

type fakeEngine struct {
	mu    sync.Mutex
	calls []call
	reply func(chat int64, ev engineclient.Event) (engineclient.Output, error)
}

func (e *fakeEngine) Turn(_ context.Context, chat int64, ev engineclient.Event) (engineclient.Output, error) {
	e.mu.Lock()
	e.calls = append(e.calls, call{chat, ev})
	e.mu.Unlock()
	if e.reply != nil {
		return e.reply(chat, ev)
	}
	return engineclient.Output{}, nil
}
func (e *fakeEngine) got() []call {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]call(nil), e.calls...)
}

func textOut(s ...string) engineclient.Output {
	var o engineclient.Output
	for _, t := range s {
		o.Messages = append(o.Messages, engineclient.Message{Kind: engineclient.KindText, Text: t})
	}
	return o
}

func newAdapter(eng *fakeEngine) (*Adapter, *fakeTG) {
	tg := &fakeTG{}
	return New(tg, eng), tg
}

func TestStartShowsPickerWithTopicPayloads(t *testing.T) {
	eng := &fakeEngine{reply: func(int64, engineclient.Event) (engineclient.Output, error) {
		return engineclient.Output{Messages: []engineclient.Message{{Kind: engineclient.KindPicker, Buttons: []engineclient.Button{{Label: "A", Data: "a"}}}}}, nil
	}}
	a, tg := newAdapter(eng)
	a.handleUpdate(context.Background(), telegram.Update{ChatID: 1, IsStart: true})
	if c := eng.got(); len(c) != 1 || !c[0].ev.Start {
		t.Fatalf("engine calls: %+v", c)
	}
	if len(tg.buttons) != 1 || tg.buttons[0][0].Data != "topic:a" || tg.buttons[0][0].Label != "A" {
		t.Fatalf("buttons: %+v", tg.buttons)
	}
}

func TestTopicTapPicksThenAcksThenDelivers(t *testing.T) {
	eng := &fakeEngine{reply: func(int64, engineclient.Event) (engineclient.Output, error) { return textOut("GREETING"), nil }}
	a, tg := newAdapter(eng)
	a.handleUpdate(context.Background(), telegram.Update{ChatID: 1, CallbackID: "cb1", CallbackData: "topic:dental"})
	if c := eng.got(); len(c) != 1 || c[0].ev.Pick != "dental" {
		t.Fatalf("engine calls: %+v", c)
	}
	if got := tg.calls(); len(got) != 2 || got[0] != "ack:cb1" || got[1] != "text:GREETING" {
		t.Fatalf("order: %v", got)
	}
}

func TestUnknownCallbackIsAckedWithoutEngineCall(t *testing.T) {
	eng := &fakeEngine{}
	a, tg := newAdapter(eng)
	for _, data := range []string{"", "other:1", "topic:"} {
		a.handleUpdate(context.Background(), telegram.Update{ChatID: 1, CallbackID: "cb", CallbackData: data})
	}
	if len(eng.got()) != 0 {
		t.Fatalf("engine must not be called: %+v", eng.got())
	}
	if got := tg.calls(); len(got) != 3 || got[0] != "ack:cb" {
		t.Fatalf("every callback must be acked: %v", got)
	}
}

func TestCallbackAckedEvenWhenEngineIsDown(t *testing.T) {
	eng := &fakeEngine{reply: func(int64, engineclient.Event) (engineclient.Output, error) {
		return engineclient.Output{}, errors.New("down")
	}}
	a, tg := newAdapter(eng)
	a.handleUpdate(context.Background(), telegram.Update{ChatID: 1, CallbackID: "cb", CallbackData: "topic:x"})
	if got := tg.calls(); len(got) != 2 || got[0] != "ack:cb" || got[1] != "text:"+apologyLine {
		t.Fatalf("got %v", got)
	}
}

func TestVoiceNoteGoesToEngineAndReplyOrderIsVoiceThenText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.ogg")
	if err := os.WriteFile(path, []byte("OggS-in"), 0o600); err != nil {
		t.Fatal(err)
	}
	eng := &fakeEngine{reply: func(int64, engineclient.Event) (engineclient.Output, error) {
		return engineclient.Output{Messages: []engineclient.Message{
			{Kind: engineclient.KindVoice, OGG: []byte("OggS")}, {Kind: engineclient.KindText, Text: "answer"}}}, nil
	}}
	a, tg := newAdapter(eng)
	tg.voicePath = path
	a.handleUpdate(context.Background(), telegram.Update{ChatID: 1, VoiceFileID: "f", VoiceSeconds: 4})
	c := eng.got()
	if len(c) != 1 || c[0].ev.Voice == nil || c[0].ev.Voice.Seconds != 4 || string(c[0].ev.Voice.OGG) != "OggS-in" {
		t.Fatalf("engine calls: %+v", c)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the downloaded temp file must be removed")
	}
	got := tg.calls()
	if len(got) < 3 || got[0] != "rec" || got[len(got)-2] != "voice" || got[len(got)-1] != "text:answer" {
		t.Fatalf("want recording indicator, then voice, then text: %v", got)
	}
}

func TestVoiceDownloadFailureApologisesWithoutEngineCall(t *testing.T) {
	eng := &fakeEngine{}
	a, tg := newAdapter(eng)
	a.handleUpdate(context.Background(), telegram.Update{ChatID: 1, VoiceFileID: "f"})
	if len(eng.got()) != 0 || len(tg.textsTo(1)) != 1 {
		t.Fatalf("calls %+v texts %v", eng.got(), tg.textsTo(1))
	}
}

func TestTextAndCommandsAreForwardedAndRecordingOnlyForRealText(t *testing.T) {
	eng := &fakeEngine{reply: func(int64, engineclient.Event) (engineclient.Output, error) { return textOut("ok"), nil }}
	a, tg := newAdapter(eng)
	a.handleUpdate(context.Background(), telegram.Update{ChatID: 1, Text: "/reset"})
	a.handleUpdate(context.Background(), telegram.Update{ChatID: 1, Text: "/voice b"})
	for _, g := range tg.calls() {
		if g == "rec" {
			t.Fatalf("slash commands are fast: no recording indicator: %v", tg.calls())
		}
	}
	a.handleUpdate(context.Background(), telegram.Update{ChatID: 1, Text: "скільки коштує?"})
	c := eng.got()
	if len(c) != 3 || c[0].ev.Text != "/reset" || c[1].ev.Text != "/voice b" || c[2].ev.Text != "скільки коштує?" {
		t.Fatalf("calls: %+v", c)
	}
	if tg.calls()[len(tg.calls())-2] != "rec" {
		t.Fatalf("a dialogue turn shows the recording indicator: %v", tg.calls())
	}
}

func TestEngineFailureApologisesOnceAndWorkerSurvives(t *testing.T) {
	fail := true
	eng := &fakeEngine{}
	eng.reply = func(int64, engineclient.Event) (engineclient.Output, error) {
		if fail {
			fail = false
			return engineclient.Output{}, errors.New("5xx")
		}
		return textOut("back"), nil
	}
	a, tg := newAdapter(eng)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.dispatch(ctx, telegram.Update{ChatID: 1, Text: "a"})
	a.dispatch(ctx, telegram.Update{ChatID: 1, Text: "b"})
	waitFor(t, func() bool { return len(tg.textsTo(1)) == 2 })
	if got := tg.textsTo(1); got[0] != apologyLine || got[1] != "back" {
		t.Fatalf("got %v", got)
	}
	if len(eng.got()) != 2 {
		t.Fatalf("a failed turn must not be retried: %+v", eng.got())
	}
}

func TestPanicInOneUpdateDoesNotStopTheWorker(t *testing.T) {
	n := 0
	eng := &fakeEngine{reply: func(int64, engineclient.Event) (engineclient.Output, error) {
		n++
		if n == 1 {
			panic("boom")
		}
		return textOut("alive"), nil
	}}
	a, tg := newAdapter(eng)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.dispatch(ctx, telegram.Update{ChatID: 1, Text: "a"})
	a.dispatch(ctx, telegram.Update{ChatID: 1, Text: "b"})
	waitFor(t, func() bool { return len(tg.textsTo(1)) == 1 })
	if tg.textsTo(1)[0] != "alive" {
		t.Fatalf("got %v", tg.textsTo(1))
	}
}

func TestPerChatFIFOAndChatsRunInParallel(t *testing.T) {
	release := make(chan struct{})
	eng := &fakeEngine{reply: func(chat int64, ev engineclient.Event) (engineclient.Output, error) {
		if chat == 1 && ev.Text == "slow" {
			<-release
		}
		return textOut("re:" + ev.Text), nil
	}}
	a, tg := newAdapter(eng)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.dispatch(ctx, telegram.Update{ChatID: 1, Text: "slow"})
	a.dispatch(ctx, telegram.Update{ChatID: 1, Text: "second"})
	a.dispatch(ctx, telegram.Update{ChatID: 2, Text: "other"})
	waitFor(t, func() bool { return len(tg.textsTo(2)) == 1 }) // chat 2 is not blocked by chat 1
	if len(tg.textsTo(1)) != 0 {
		t.Fatal("chat 1 must still be waiting")
	}
	close(release)
	waitFor(t, func() bool { return len(tg.textsTo(1)) == 2 })
	if got := tg.textsTo(1); got[0] != "re:slow" || got[1] != "re:second" {
		t.Fatalf("one chat's updates must keep arrival order: %v", got)
	}
}

func TestQueueOverflowDropsAndNotifiesOncePerWindow(t *testing.T) {
	block := make(chan struct{})
	eng := &fakeEngine{reply: func(int64, engineclient.Event) (engineclient.Output, error) { <-block; return textOut("x"), nil }}
	a, tg := newAdapter(eng)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < chatQueueSize+10; i++ {
		a.dispatch(ctx, telegram.Update{ChatID: 1, Text: "m"})
	}
	waitFor(t, func() bool { return len(tg.textsTo(1)) == 1 })
	if tg.textsTo(1)[0] != slowDownLine {
		t.Fatalf("got %v", tg.textsTo(1))
	}
	close(block)
}

func TestIdleWorkerEvictedAndRespawned(t *testing.T) {
	eng := &fakeEngine{reply: func(int64, engineclient.Event) (engineclient.Output, error) { return textOut("ok"), nil }}
	a, tg := newAdapter(eng)
	a.idleEvict = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.dispatch(ctx, telegram.Update{ChatID: 1, Text: "a"})
	waitFor(t, func() bool { a.mu.Lock(); defer a.mu.Unlock(); return len(a.inbox) == 0 })
	a.dispatch(ctx, telegram.Update{ChatID: 1, Text: "b"})
	waitFor(t, func() bool { return len(tg.textsTo(1)) == 2 })
}

func TestRunWaitsForWorkersOnShutdown(t *testing.T) {
	started := make(chan struct{})
	finish := make(chan struct{})
	eng := &fakeEngine{reply: func(int64, engineclient.Event) (engineclient.Output, error) {
		close(started)
		<-finish
		return textOut("done"), nil
	}}
	a, tg := newAdapter(eng)
	updates := make(chan telegram.Update, 1)
	updates <- telegram.Update{ChatID: 1, Text: "a"}
	close(updates)
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() { a.Run(ctx, updates); close(ran) }()
	<-started
	cancel() // shutdown begins while the turn is in flight
	select {
	case <-ran:
		t.Fatal("Run returned while a turn was in flight")
	case <-time.After(50 * time.Millisecond):
	}
	close(finish)
	<-ran
	if len(tg.textsTo(1)) != 1 {
		t.Fatalf("the in-flight reply must be delivered: %v", tg.textsTo(1))
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
