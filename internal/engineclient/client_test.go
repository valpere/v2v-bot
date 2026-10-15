package engineclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const key = "k-0123456789abcdef0123456789abcdef"

func TestTurnSendsOneEventAndDecodesMessages(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		io.WriteString(w, `{"messages":[{"kind":"voice","ogg_b64":"`+base64.StdEncoding.EncodeToString([]byte("OggS"))+`"},{"kind":"text","text":"hi"},{"kind":"picker","buttons":[{"label":"L","data":"d"}]}],"signal":"continue"}`)
	}))
	defer srv.Close()
	c := New(srv.URL, key, time.Second)

	out, err := c.Turn(context.Background(), 42, Event{Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/chats/42/turn" || gotAuth != "Bearer "+key || gotBody["text"] != "hello" || len(gotBody) != 1 {
		t.Fatalf("request: %s %q %v", gotPath, gotAuth, gotBody)
	}
	if len(out.Messages) != 3 || string(out.Messages[0].OGG) != "OggS" || out.Messages[1].Text != "hi" ||
		out.Messages[2].Kind != KindPicker || out.Messages[2].Buttons[0].Data != "d" || out.Signal != "continue" {
		t.Fatalf("decoded: %+v", out)
	}
}

func TestTurnEventShapes(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = nil
		_ = json.NewDecoder(r.Body).Decode(&body)
		io.WriteString(w, `{"messages":[]}`)
	}))
	defer srv.Close()
	c := New(srv.URL, key, time.Second)
	for name, ev := range map[string]Event{
		"start": {Start: true},
		"pick":  {Pick: "dental"},
		"voice": {Voice: &Voice{Seconds: 3, OGG: []byte("OggS")}},
	} {
		if _, err := c.Turn(context.Background(), 1, ev); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, ok := body[name]; !ok || len(body) != 1 {
			t.Errorf("%s: body %v must carry exactly that one field", name, body)
		}
		if name == "voice" {
			v, _ := body["voice"].(map[string]any)
			if v["seconds"] != float64(3) || v["ogg_b64"] != base64.StdEncoding.EncodeToString([]byte("OggS")) {
				t.Errorf("voice payload: %v", v)
			}
		}
	}
}

// POST …/turn spends money and mutates the session: a failure is reported, never retried.
func TestTurnIsNeverRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()
	if _, err := New(srv.URL, key, time.Second).Turn(context.Background(), 1, Event{Text: "x"}); err == nil {
		t.Fatal("a 5xx must be an error")
	}
	if calls.Load() != 1 {
		t.Fatalf("%d calls, want exactly 1", calls.Load())
	}
}

func TestTurnTimeoutIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer srv.Close()
	if _, err := New(srv.URL, key, 50*time.Millisecond).Turn(context.Background(), 1, Event{Text: "x"}); err == nil {
		t.Fatal("want a timeout error")
	}
}

func TestTurnRejectsUnauthorizedAndBadEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := New(srv.URL, "bad", time.Second)
	if _, err := c.Turn(context.Background(), 1, Event{Text: "x"}); err == nil {
		t.Fatal("401 must be an error")
	}
	if _, err := c.Turn(context.Background(), 1, Event{}); err == nil {
		t.Fatal("an empty event must be refused before any request")
	}
}

func TestPreCheckout(t *testing.T) {
	var path, auth string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&body)
		io.WriteString(w, `{"ok":false,"error":"Замовлення застаріло"}`)
	}))
	defer srv.Close()
	v, err := New(srv.URL, key, time.Second).PreCheckout(context.Background(), "ord1", 35)
	if err != nil || v.OK || v.Error != "Замовлення застаріло" {
		t.Fatalf("verdict %+v err %v", v, err)
	}
	if path != "/v1/payments/pre-checkout" || auth != "Bearer "+key || body["order_id"] != "ord1" || body["stars"] != float64(35) {
		t.Fatalf("request %s %q %v", path, auth, body)
	}
}

func TestPaidRetriesTransientFailuresOnly(t *testing.T) {
	old := retryWait
	retryWait = time.Millisecond
	defer func() { retryWait = old }()

	var calls atomic.Int32
	var last map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&last)
		if calls.Add(1) < 3 {
			http.Error(w, "boom", http.StatusBadGateway)
			return
		}
		io.WriteString(w, `{"ok":true,"duplicate":true}`)
	}))
	defer srv.Close()
	c := New(srv.URL, key, time.Second)
	dup, err := c.Paid(context.Background(), "ord1", 35, "ch1")
	if err != nil || !dup || calls.Load() != 3 {
		t.Fatalf("dup=%v err=%v calls=%d", dup, err, calls.Load())
	}
	if last["order_id"] != "ord1" || last["charge_id"] != "ch1" || last["stars"] != float64(35) {
		t.Fatalf("body %v", last)
	}

	// a 4xx is the engine's final word: no retry
	calls.Store(0)
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, `{"error":"unknown order"}`, http.StatusNotFound)
	}))
	defer srv2.Close()
	if _, err := New(srv2.URL, key, time.Second).Paid(context.Background(), "x", 1, "c"); err == nil || calls.Load() != 1 {
		t.Fatalf("4xx: err=%v calls=%d", err, calls.Load())
	}

	// persistent 5xx: gives up after a bounded number of attempts
	calls.Store(0)
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv3.Close()
	if _, err := New(srv3.URL, key, time.Second).Paid(context.Background(), "x", 1, "c"); err == nil || calls.Load() != int32(paidAttempts) {
		t.Fatalf("5xx: err=%v calls=%d", err, calls.Load())
	}
}
