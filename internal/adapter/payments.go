package adapter

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/valpere/v2v-bot/internal/telegram"
)

// preCheckoutBudget keeps the answer inside Telegram's 10 s deadline.
const preCheckoutBudget = 8 * time.Second

const (
	refuseLine  = "Не вдалося обробити оплату. Спробуйте пізніше. / Could not process the payment. Please try again later."
	notStarLine = "Приймаємо лише Telegram Stars. / Only Telegram Stars are accepted."
)

// handlePayment relays a payment update to the engine. Payment updates never
// wait in a chat's queue: a slow turn must not push a pre-checkout answer past
// Telegram's deadline.
func (a *Adapter) handlePayment(ctx context.Context, u telegram.Update) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("recovered panic in payment (chat %d): %v", u.ChatID, r)
		}
	}()
	switch {
	case u.PreCheckout != nil:
		a.preCheckout(ctx, u.PreCheckout)
	case u.Payment != nil:
		a.paid(ctx, u.ChatID, u.Payment)
	}
}

func (a *Adapter) preCheckout(ctx context.Context, p *telegram.PreCheckout) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), preCheckoutBudget)
	defer cancel()
	ok, msg := false, refuseLine
	switch {
	case p.Currency != "XTR":
		msg = notStarLine
	default:
		v, err := a.eng.PreCheckout(ctx, p.OrderID, p.Stars)
		switch {
		case err != nil:
			log.Printf("pre-checkout %s: %v", p.OrderID, err) // refuse: never let money move on an unchecked order
		case v.OK:
			ok, msg = true, ""
		case v.Error != "":
			msg = v.Error
		}
	}
	if err := a.tg.AnswerPreCheckout(ctx, p.ID, ok, msg); err != nil {
		log.Printf("answer pre-checkout %s: %v", p.OrderID, err)
	}
}

func (a *Adapter) paid(ctx context.Context, chatID int64, p *telegram.Payment) {
	if p.Currency != "XTR" || p.OrderID == "" {
		log.Printf("ignoring a payment that is not one of our Stars orders: chat=%d currency=%q order=%q charge=%s", chatID, p.Currency, p.OrderID, p.ChargeID)
		return
	}
	// The money has moved: finish even when shutdown has begun.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if _, err := a.eng.Paid(ctx, p.OrderID, p.Stars, p.ChargeID); err != nil {
		// Keep what a refund needs in the log, and give the payer the same reference.
		log.Printf("PAYMENT NOT RECORDED: order=%s stars=%d charge=%s chat=%d: %v", p.OrderID, p.Stars, p.ChargeID, chatID, err)
		a.send(ctx, chatID, fmt.Sprintf("Оплату %d ⭐ отримано, але замовлення зафіксувати не вдалося — ми розберемося. Номер платежу: %s / Payment of %d ⭐ received, but the order could not be recorded — we will sort it out. Payment id: %s", p.Stars, p.ChargeID, p.Stars, p.ChargeID))
		return
	}
	a.send(ctx, chatID, fmt.Sprintf("Дякуємо! Оплату %d ⭐ отримано, замовлення прийнято. / Thank you! Payment of %d ⭐ received, order accepted.", p.Stars, p.Stars))
}
