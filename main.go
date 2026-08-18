package main

import (
	"bufio"
	"bytes"
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

const welcomeMessage = `🎟 ትኬት ይግዙ iPhone 17 Pro Max ይሸለሙ! 📱

የ1 ቲኬት ዋጋ = 500 ብር 💰

🏆 1ኛ እጣ = iPhone 17 Pro Max
🥈 2ኛ እጣ = 20,000 ብር
🥉 3ኛ እጣ = 10,000 ብር
💰 30 እድለኛ ተሳታፊዎች = 500 ብር የትኬት ክፍያቸው ይመለስላቸዋል!

አሁኑኑ የእድል ቁጥርዎን ይምረጡ! እድልዎ ዛሬ ነው! ⏳

ለማንኛውም ጥያቄ እኛን ያነጋግሩን @AfroEqub`

const miniAppURL = "https://afro.blessed-equb.com"
const supportURL = "https://t.me/afroequb"
const pingURL = "https://affrbot.onrender.com"

func keepAlive(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, pingURL, nil)
			if err != nil {
				log.Printf("failed to create ping request: %v", err)
				continue
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				log.Printf("ping failed: %v", err)
				continue
			}
			resp.Body.Close()
			log.Printf("pinged %s (status %d)", pingURL, resp.StatusCode)
		}
	}
}

func loadEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		os.Setenv(strings.TrimSpace(key), strings.Trim(value, `"'`))
	}
}

func startHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	keyboard := &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: "እድለኛ ቁጥርዎን ይምረጡ, አሁን ይጀምሩ", WebApp: &models.WebAppInfo{URL: miniAppURL}}},
			{{Text: "የድጋፍ አገልግሎትን ያግኙ", URL: supportURL}},
		},
	}

	fileData, err := os.ReadFile("image/having.jpg")
	if err != nil {
		log.Printf("failed to read image: %v", err)
		return
	}

	photo := &models.InputFileUpload{Filename: "having.jpg", Data: bytes.NewReader(fileData)}

	if _, err := b.SendPhoto(ctx, &bot.SendPhotoParams{
		ChatID:      update.Message.Chat.ID,
		Photo:       photo,
		Caption:     welcomeMessage,
		ReplyMarkup: keyboard,
	}); err != nil {
		log.Printf("failed to send welcome: %v", err)
	}
}

func main() {
	loadEnv(".env")

	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN environment variable is required")
	}

	ctx := context.Background()

	go keepAlive(ctx)

	b, err := bot.New(token)
	if err != nil {
		log.Fatalf("failed to create bot: %v", err)
	}

	me, err := b.GetMe(ctx)
	if err != nil {
		log.Fatalf("failed to call getMe: %v", err)
	}

	log.Printf("authorized on account %s", me.Username)

	b.RegisterHandler(bot.HandlerTypeMessageText, "/start", bot.MatchTypeExact, startHandler)

	b.Start(ctx)
}