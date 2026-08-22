package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// ==================== Config ====================

var (
	miniAppURL  = "https://afro.blessed-equb.com"
	supportURL  = "https://t.me/afroequb"
	pingURL     = "https://affrbot.onrender.com"
	botTokenStr string
)

// ==================== Env loading ====================

func loadEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		os.Setenv(strings.TrimSpace(k), strings.Trim(v, `"'`))
	}
}

// ==================== Keyboards ====================

func webAppInfo() *models.WebAppInfo {
	return &models.WebAppInfo{URL: miniAppURL}
}

func mainMenu() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{
				{Text: "🎟 ቲኬት ይያዙ", WebApp: webAppInfo()},
			},
			{
				{Text: "💬 የድጋፍ አገልግሎትን ያግኙ", URL: supportURL},
			},
		},
	}
}

func replyKB() *models.ReplyKeyboardMarkup {
	return &models.ReplyKeyboardMarkup{
		Keyboard: [][]models.KeyboardButton{
			{{Text: "🎟 ቲኬት ይያዙ", WebApp: webAppInfo()}},
			{{Text: "ℹ️ እርዳታ"}, {Text: "📞 ድጋፍ"}},
		},
		IsPersistent:          true,
		ResizeKeyboard:        true,
		InputFieldPlaceholder: "/book ብለው ይላኩ ቲኬት ለመያዝ",
	}
}

// ==================== Messages ====================

const welcomeMessage = `🎟 ትኬት ይግዙ iPhone 17 Pro Max ይሸለሙ! 📱

የ1 ቲኬት ዋጋ = 500 ብር 💰

🏆 1ኛ እጣ = iPhone 17 Pro Max
🥈 2ኛ እጣ = 20,000 ብር
🥉 3ኛ እጣ = 10,000 ብር
💰 30 እድለኛ ተሳታፊዎች = 500 ብር የትኬት ክፍያቸው ይመለስላቸዋል!

✅ ሁሉም በቴሌግራም ውስጥ ይከናወናል!
1️⃣ «አሁን ይጀምሩ» የሚለውን ቁልፍ ይንኩ
2️⃣ እድል ቁጥርዎን ይምረጡ
3️⃣ ክፍያ ያስረክቡ እና ደረሰኝዎን ያስገቡ

⏳ እድልዎ ዛሬ ነው!

ለማንኛውም ጥያቄ እኛን ያነጋግሩን @AfroEqub`

const helpMessage = `ትኬቱን ለመግዛት ይሄንን ቀላል መንገድ ይከተሉ! 🎟

1️⃣ «🎟 ቲኬት ይያዙ» ቁልፍ ይንኩ ወይም /book ብለው ይላኩ
2️⃣ ከተከፈተለው ገጽ ላይ ክፍት የሆነ እድል ቁጥር ይምረጡ
3️⃣ 500 ብር ወደሚታየለት ሂሳብ (Telebirr) ያስረክቡ
4️⃣ የላኩትን ደረሰኝ screenshot በዚያው ገጽ ላይ ያስገቡ
5️⃣ ክፍያዎ ከተረጋገጠ ቲኬትዎ ተመዝግቧል ✅

ሁሉም ሂደት ውስጥ በቴሌግራም ውስጥ ይጠናቀቃል - መውጣት አያስፈልግም!

ማንኛውም ችግር ወይም ግር ያሎት ነገር ካለ እኛን ያነጋግሩን @afroequb`

const videoCaption = `🎥 ክፍያ እንዴት እንደሚፈጽሙ የሚያሳይ አጫጭር ቪዲዮ`

// ==================== Helpers ====================

func pingHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"message":"pong"}`)
}

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

func sendDepositVideo(ctx context.Context, b *bot.Bot, chatID int64) {
	f, err := os.Open("afro equb deposit.mp4")
	if err != nil {
		log.Printf("failed to open video: %v", err)
		return
	}
	defer f.Close()

	video := &models.InputFileUpload{Filename: "afro equb deposit.mp4", Data: f}
	b.SendVideo(ctx, &bot.SendVideoParams{
		ChatID:  chatID,
		Video:   video,
		Caption: videoCaption,
	})
}

// ==================== /start ====================

func startHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID

	fileData, err := os.ReadFile("image/having.jpg")
	if err == nil {
		photo := &models.InputFileUpload{Filename: "having.jpg", Data: bytes.NewReader(fileData)}
		b.SendPhoto(ctx, &bot.SendPhotoParams{
			ChatID:      chatID,
			Photo:       photo,
			Caption:     welcomeMessage,
			ReplyMarkup: mainMenu(),
		})
	} else {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:      chatID,
			Text:        welcomeMessage,
			ReplyMarkup: mainMenu(),
		})
	}

	// Persistent reply keyboard
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        "👇 ለመጀመር ከታች ያለውን ቁልፍ ይጠቀሙ",
		ReplyMarkup: replyKB(),
	})

	sendDepositVideo(ctx, b, chatID)
}

// ==================== /help ====================

func helpHandler(ctx context.Context, b *bot.Bot, chatID int64) {
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:  chatID,
		Text:    helpMessage,
		ReplyMarkup: mainMenu(),
	})
}

// ==================== Support ====================

func supportHandler(ctx context.Context, b *bot.Bot, chatID int64) {
	keyboard := &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: "💬 ደላላችንን ያግኙን", URL: supportURL}},
		},
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        "ማንኛውም ጥያቄ ወይም ችግር ካለ ደላላችንን ያግኙን, በፍጥነት እንመልሳለን! 🙏",
		ReplyMarkup: keyboard,
	})
}

// ==================== Text handler ====================

func textHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	text := strings.ToLower(strings.TrimSpace(update.Message.Text))
	chatID := update.Message.Chat.ID

	switch {
	case strings.Contains(text, "ትኬት") || strings.Contains(text, "book"):
		// Redirect to webapp
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:      chatID,
			Text:        "🎟 ቲኬት ለመያዝ ከታች ያለውን ቁልፍ ይጠቀሙ 👇",
			ReplyMarkup: replyKB(),
		})
	case strings.Contains(text, "እርዳታ") || strings.Contains(text, "help"):
		helpHandler(ctx, b, chatID)
	case strings.Contains(text, "ድጋፍ") || strings.Contains(text, "support"):
		supportHandler(ctx, b, chatID)
	default:
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:      chatID,
			Text:        "To book a ticket, type /help or tap the button below 👇",
			ReplyMarkup: replyKB(),
		})
	}
}

// ==================== /book (commented out - kept for future) ====================

/*
// BOOKING FLOW - commented out, ready to re-enable later

type Step string

const (
	AwaitNum     Step = "await_num"
	AwaitContact Step = "await_contact"
	AwaitConfirm Step = "await_confirm"
	AwaitReceipt Step = "await_receipt"
)

type UserState struct {
	Step    Step
	Ticket  int
	Phone   string
	Name    string
}

var (
	states   = map[int64]*UserState{}
	statesMu sync.Mutex
)

func getState(id int64) *UserState { statesMu.Lock(); defer statesMu.Unlock(); return states[id] }
func setState(id int64, s *UserState) { statesMu.Lock(); defer statesMu.Unlock(); states[id] = s }
func clearState(id int64) { statesMu.Lock(); defer statesMu.Unlock(); delete(states[id]) }

func bookHandler(ctx context.Context, b *bot.Bot, chatID int64) {
	setState(chatID, &UserState{Step: AwaitNum})
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "🎟 እባክዎ መряይ የሚሹትን ትኬት ቁጥር ይፃፉ\n\n📝 ለምሳሌ፡ 42\n\n(1 እስከ 5000)",
		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{
				{{Text: "❌ ሰርዝ", CallbackData: "CANCEL"}},
			},
		},
	})
}
*/

// ==================== Main ====================

func main() {
	loadEnv(".env")

	botTokenStr = os.Getenv("TELEGRAM_BOT_TOKEN")
	if botTokenStr == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN environment variable is required")
	}

	if url := os.Getenv("MINIAPP_URL"); url != "" {
		miniAppURL = url
	}

	ctx := context.Background()

	go keepAlive(ctx)

	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}

	http.HandleFunc("/", pingHandler)
	go func() {
		log.Printf("HTTP server starting on :%s", port)
		if err := http.ListenAndServe(":"+port, nil); err != nil {
			log.Fatalf("HTTP server failed: %v", err)
		}
	}()

	b, err := bot.New(botTokenStr)
	if err != nil {
		log.Fatalf("failed to create bot: %v", err)
	}

	me, err := b.GetMe(ctx)
	if err != nil {
		log.Fatalf("failed to call getMe: %v", err)
	}
	log.Printf("authorized on account %s", me.Username)

	// Command handlers
	b.RegisterHandler(bot.HandlerTypeMessageText, "start", bot.MatchTypeCommand, startHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "help", bot.MatchTypeCommand, func(ctx context.Context, b *bot.Bot, update *models.Update) {
		helpHandler(ctx, b, update.Message.Chat.ID)
	})

	// Text handler (must stay last)
	b.RegisterHandler(bot.HandlerTypeMessageText, "", bot.MatchTypeContains, textHandler)

	b.SetMyCommands(ctx, &bot.SetMyCommandsParams{
		Commands: []models.BotCommand{
			{Command: "start", Description: "🚀 ቦቱን ይጀምሩ"},
			{Command: "help", Description: "ℹ️ እንዴት እንደሚጠቀሙ ይመልከቱ"},
		},
	})

	log.Printf("🚀 Bot is running! Send /start to @%s to test.", me.Username)
	b.Start(ctx)
}
