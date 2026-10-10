package notifier

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

type Message struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	Type   string `json:"type"`
	DoseID string `json:"doseId,omitempty"`
	URL    string `json:"url"`
}

type Subscription struct {
	ID       string
	Endpoint string
	P256dh   string
	Auth     string
}

type Sender interface {
	Send(context.Context, Subscription, Message) error
}

type WebPush struct {
	publicKey  string
	privateKey string
	subject    string
	client     *http.Client
}

func NewWebPush(publicKey, privateKey, subject string) *WebPush {
	return &WebPush{
		publicKey: publicKey, privateKey: privateKey, subject: subject,
		client: &http.Client{Timeout: 12 * time.Second},
	}
}

func (w *WebPush) Send(ctx context.Context, sub Subscription, message Message) error {
	if w.publicKey == "" || w.privateKey == "" {
		return fmt.Errorf("web push disabled: configure WEBPUSH_PUBLIC_KEY and WEBPUSH_PRIVATE_KEY to enable browser push")
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode push payload: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	subscription := &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys: webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth},
	}
	_, err = webpush.SendNotification(payload, subscription, &webpush.Options{
		Subscriber:      w.subject,
		VAPIDPublicKey:  w.publicKey,
		VAPIDPrivateKey: w.privateKey,
		TTL:             60,
		HTTPClient:      w.client,
	})
	if err != nil {
		slog.Debug("web push delivery failed", "subscription_id", sub.ID, "error", err)
		return fmt.Errorf("send web push: %w", err)
	}
	return nil
}
