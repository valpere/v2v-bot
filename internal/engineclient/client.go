// Package engineclient is the HTTP client for the engine's channel API
// (v2v-demo docs/channel-api.md): one event in, the messages to deliver out.
package engineclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Message kinds the engine returns.
const (
	KindText   = "text"
	KindVoice  = "voice"
	KindPicker = "picker"
)

// Event is one inbound event; set exactly one of Start, Pick, Text, Voice.
type Event struct {
	Start bool
	Pick  string
	Text  string
	Voice *Voice
}

// Voice is an inbound OGG/Opus voice note.
type Voice struct {
	Seconds int
	OGG     []byte
}

// Button is one picker choice; Data is echoed back as Event.Pick on a tap.
type Button struct {
	Label string
	Data  string
}

// Message is one thing to deliver to the chat, in order.
type Message struct {
	Kind    string
	Text    string
	OGG     []byte
	Buttons []Button
}

// Output is the engine's answer to one event. Signal is informational.
type Output struct {
	Messages []Message
	Signal   string
}

// Client talks to one engine.
type Client struct {
	base string
	key  string
	http *http.Client
}

// New returns a client for the engine at baseURL. timeout bounds one whole
// request: set it above the engine's TURN_TIMEOUT.
func New(baseURL, key string, timeout time.Duration) *Client {
	return &Client{base: strings.TrimRight(baseURL, "/"), key: key, http: &http.Client{Timeout: timeout}}
}

type wireVoice struct {
	Seconds int    `json:"seconds"`
	OGGB64  string `json:"ogg_b64"`
}

type wireRequest struct {
	Start bool       `json:"start,omitempty"`
	Pick  string     `json:"pick,omitempty"`
	Text  string     `json:"text,omitempty"`
	Voice *wireVoice `json:"voice,omitempty"`
}

type wireResponse struct {
	Messages []struct {
		Kind    string `json:"kind"`
		Text    string `json:"text"`
		OGGB64  string `json:"ogg_b64"`
		Buttons []struct {
			Label string `json:"label"`
			Data  string `json:"data"`
		} `json:"buttons"`
	} `json:"messages"`
	Signal string `json:"signal"`
}

// Turn sends one event for chatID. It never retries: the call spends money and
// mutates the session, so a failure is returned for the caller to report.
func (c *Client) Turn(ctx context.Context, chatID int64, ev Event) (Output, error) {
	req := wireRequest{Start: ev.Start, Pick: ev.Pick, Text: ev.Text}
	kinds := 0
	for _, set := range []bool{ev.Start, ev.Pick != "", ev.Text != "", ev.Voice != nil} {
		if set {
			kinds++
		}
	}
	if kinds != 1 {
		return Output{}, errors.New("engineclient: an event needs exactly one of start, pick, text, voice")
	}
	if ev.Voice != nil {
		req.Voice = &wireVoice{Seconds: ev.Voice.Seconds, OGGB64: base64.StdEncoding.EncodeToString(ev.Voice.OGG)}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return Output{}, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/chats/"+strconv.FormatInt(chatID, 10)+"/turn", bytes.NewReader(body))
	if err != nil {
		return Output{}, err
	}
	hr.Header.Set("Authorization", "Bearer "+c.key)
	hr.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(hr)
	if err != nil {
		return Output{}, fmt.Errorf("engine turn: %w", withoutURL(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return Output{}, fmt.Errorf("engine turn: status %d", resp.StatusCode)
	}
	var wr wireResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&wr); err != nil {
		return Output{}, fmt.Errorf("engine turn: decode: %w", err)
	}
	out := Output{Signal: wr.Signal}
	for _, m := range wr.Messages {
		msg := Message{Kind: m.Kind, Text: m.Text}
		if m.Kind == KindVoice {
			if msg.OGG, err = base64.StdEncoding.DecodeString(m.OGGB64); err != nil {
				return Output{}, fmt.Errorf("engine turn: voice: %w", err)
			}
		}
		for _, b := range m.Buttons {
			msg.Buttons = append(msg.Buttons, Button{Label: b.Label, Data: b.Data})
		}
		out.Messages = append(out.Messages, msg)
	}
	return out, nil
}

// Verdict is the engine's answer to "may this order be paid?".
type Verdict struct {
	OK    bool
	Error string // text for the payer when OK is false
}

// paidAttempts bounds how often a payment confirmation is sent; retryWait is the
// pause before the next attempt (a var so tests need not really sleep).
const paidAttempts = 5

var retryWait = time.Second

// post sends one payments request and decodes the JSON answer into out. The
// returned status is 0 for a transport failure.
func (c *Client) post(ctx context.Context, path string, body, out any) (int, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	hr.Header.Set("Authorization", "Bearer "+c.key)
	hr.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(hr)
	if err != nil {
		return 0, fmt.Errorf("engine %s: %w", path, withoutURL(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return resp.StatusCode, fmt.Errorf("engine %s: status %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		return resp.StatusCode, fmt.Errorf("engine %s: decode: %w", path, err)
	}
	return resp.StatusCode, nil
}

// PreCheckout asks whether order may be paid for stars Stars. Not retried: the
// answer is due within Telegram's 10 s, and a failure is a refusal.
func (c *Client) PreCheckout(ctx context.Context, orderID string, stars int) (Verdict, error) {
	var r struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if _, err := c.post(ctx, "/v1/payments/pre-checkout", map[string]any{"order_id": orderID, "stars": stars}, &r); err != nil {
		return Verdict{}, err
	}
	return Verdict{OK: r.OK, Error: r.Error}, nil
}

// Paid records a completed payment. The engine side is idempotent, so a
// transport failure or 5xx is retried a few times; a 4xx is final. duplicate is
// true when the engine had already recorded this charge.
func (c *Client) Paid(ctx context.Context, orderID string, stars int, chargeID string) (duplicate bool, err error) {
	body := map[string]any{"order_id": orderID, "stars": stars, "charge_id": chargeID}
	for attempt := 1; ; attempt++ {
		var r struct {
			Duplicate bool `json:"duplicate"`
		}
		status, err := c.post(ctx, "/v1/payments/paid", body, &r)
		if err == nil {
			return r.Duplicate, nil
		}
		final4xx := status >= 400 && status < 500 && status != http.StatusRequestTimeout && status != http.StatusTooManyRequests
		if final4xx || attempt >= paidAttempts || ctx.Err() != nil {
			return false, err
		}
		select {
		case <-ctx.Done():
			return false, err
		case <-time.After(retryWait * time.Duration(attempt)):
		}
	}
}

// withoutURL strips the request URL (which names the engine) from a transport
// error, keeping the cause.
func withoutURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}
