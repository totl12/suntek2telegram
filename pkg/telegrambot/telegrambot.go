package telegrambot

import (
	"log"
	"time"

	tb "gopkg.in/tucnak/telebot.v2"
)

// NewBot creates and connects a Telegram bot using the given API key.
func NewBot(apiKey string) (*tb.Bot, error) {
	bot, err := tb.NewBot(tb.Settings{
		Token:  apiKey,
		Poller: &tb.LongPoller{Timeout: 10 * time.Second},
	})
	if err != nil {
		return nil, err
	}
	log.Println("Telegram bot connected")
	return bot, nil
}
