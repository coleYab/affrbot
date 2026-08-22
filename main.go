package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// User state management for conversation flow
type BookingState struct {
	Step      string // "awaiting_ticket", "awaiting_receipt"
	TicketNum int
	PaymentID int
}

var (
	userStates = make(map[int64]*BookingState)
	statesMu   sync.RWMutex
)

const (
	defaultMiniAppURL = "https://afro.blessed-equb.com"
	supportURL        = "https://t.me/afroequb"
	pingURL           = "https://affrbot.onrender.com"
)

var (
	miniAppURL    = defaultMiniAppURL
	webappAPIBase = "https://afro.blessed-equb.com/api/bot"
	botToken      string
)

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

const bookMessage = `🎟 ቲኬት መያዝ እጅግ ቀላል ነው!

1️⃣ ከታች «አሁን ቲኬት ይያዙ» የሚለውን ቁልፍ ይንኩ
2️⃣ ክፍት የሆነ እድል ቁጥር ይምረጡ
3️⃣ 500 ብር ወደሚታየለት ሂሳብ ያስረክቡ
4️⃣ የክፍያ ደረሰኝ screenshot ያስገቡ

ክፍያዎ ከተረጋገጠ በኋላ ቲኬትዎ ይቀመጣል ✅

ለማንኛውም ጥያቄ @AfroEqub`

const helpMessage = `ትኬቱን ለመግዛት ይሄንን ቀላል መንገድ ይከተሉ! 🎟

1️⃣ «🎟 ቲኬት ይያዙ» ቁልፍ ይንኩ ወይም /book ብለው ይላኩ
2️⃣ ከተከፈተለው ገጽ ላይ ክፍት የሆነ እድል ቁጥር ይምረጡ
3️⃣ 500 ብር ወደሚታየለት ሂሳብ (Telebirr) ያስረክቡ
4️⃣ የላኩትን ደረሰኝ screenshot በዚያው ገጽ ላይ ያስገቡ
5️⃣ ክፍያዎ ከተረጋገጠ ቲኬትዎ ተመዝግቧል ✅

ሁሉም ሂደት ውስጥ በቴሌግራም ውስጥ ይጠናቀቃል - መውጣት አያስፈልግም!

ማንኛውም ችግር ወይም ግር ያሎት ነገር ካለ እኛን ያነጋግሩን @afroequb`

const videoCaption = `🎥 ክፍያ እንዴት እንደሚፈጽሙ የሚያሳይ አጫጭር ቪዲዮ`

func pingHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"message": "pong"}`)
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

func webAppInfo() *models.WebAppInfo {
	return &models.WebAppInfo{URL: miniAppURL}
}

// bookingInlineKeyboard is attached to messages so users can jump straight
// into the booking flow inside the Telegram mini app.
func bookingInlineKeyboard() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: "🎟 አሁን ቲኬት ይያዙ", WebApp: webAppInfo()}},
			{{Text: "💬 የድጋፍ አገልግሎት", URL: supportURL}},
		},
	}
}

// bookingReplyKeyboard keeps a persistent one-tap button under the input
// field so booking is always a single tap away.
func bookingReplyKeyboard() *models.ReplyKeyboardMarkup {
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

func setBotCommands(ctx context.Context, b *bot.Bot) {
	_, err := b.SetMyCommands(ctx, &bot.SetMyCommandsParams{
		Commands: []models.BotCommand{
			{Command: "start", Description: "🚀 ቦቱን ይጀምሩ"},
			{Command: "book", Description: "🎟 ቲኬት ይያዙ"},
			{Command: "help", Description: "ℹ️ እንዴት እንደሚጠቀሙ ይመልከቱ"},
		},
	})
	if err != nil {
		log.Printf("failed to set bot commands: %v", err)
	}
}

func sendDepositVideo(ctx context.Context, b *bot.Bot, chatID int64, caption string) {
	f, err := os.Open("afro equb deposit.mp4")
	if err != nil {
		log.Printf("failed to open video: %v", err)
		return
	}
	defer f.Close()

	video := &models.InputFileUpload{Filename: "afro equb deposit.mp4", Data: f}

	if _, err := b.SendVideo(ctx, &bot.SendVideoParams{
		ChatID:  chatID,
		Video:   video,
		Caption: caption,
	}); err != nil {
		log.Printf("failed to send video: %v", err)
	}
}

func startHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	keyboard := &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: "🎟 አሁን ይጀምሩ", WebApp: webAppInfo()}},
			{{Text: "💬 የድጋፍ አገልግሎትን ያግኙ", URL: supportURL}},
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

	// Attach the persistent booking keyboard so a ticket is always one tap away.
	if _, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      update.Message.Chat.ID,
		Text:        "👇 ለመጀመር ከታች ያለውን ቁልፍ ይጠቀሙ",
		ReplyMarkup: bookingReplyKeyboard(),
	}); err != nil {
		log.Printf("failed to send keyboard: %v", err)
	}

	sendDepositVideo(ctx, b, update.Message.Chat.ID, videoCaption)
}

func bookHandler(ctx context.Context, b *bot.Bot, chatID int64) {
	// Start the conversational booking flow
	statesMu.Lock()
	userStates[chatID] = &BookingState{Step: "awaiting_ticket"}
	statesMu.Unlock()

	if _, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "🎟 Please enter the ticket number you want to reserve (e.g. 42).",
	}); err != nil {
		log.Printf("failed to send booking message: %v", err)
	}
}

func helpHandler(ctx context.Context, b *bot.Bot, chatID int64) {
	sendDepositVideo(ctx, b, chatID, helpMessage)
}

func supportHandler(ctx context.Context, b *bot.Bot, chatID int64) {
	keyboard := &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: "💬 ደላላችንን ያግኙን", URL: supportURL}},
		},
	}

	if _, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        "ማንኛውም ጥያቄ ወይም ችግር ካለ ደላላችንን ያግኙን, በፍጥነት እንመልሳለን! 🙏",
		ReplyMarkup: keyboard,
	}); err != nil {
		log.Printf("failed to send support message: %v", err)
	}
}

// checkTicketAvailability calls the Laravel API to check if a ticket is available.
func checkTicketAvailability(ticketNum int) (bool, error) {
	data := map[string]interface{}{
		"ticket_number": ticketNum,
		"bot_token":     os.Getenv("BOT_API_SECRET"),
	}
	jsonData, _ := json.Marshal(data)

	resp, err := http.Post(
		webappAPIBase+"/ticket/availability",
		"application/json",
		bytes.NewReader(jsonData),
	)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	var result struct {
		Available bool   `json:"available"`
		Message   string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, err
	}
	return result.Available, nil
}

// textHandler catches everything that is not a registered command: the
// persistent keyboard buttons and any free-form message. It always funnels
// the user back into the booking flow.
func textHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	text := strings.ToLower(strings.TrimSpace(update.Message.Text))
	chatID := update.Message.Chat.ID

	statesMu.RLock()
	state, exists := userStates[chatID]
	statesMu.RUnlock()

	if exists && state.Step == "awaiting_ticket" {
		// Try to parse ticket number
		num, err := strconv.Atoi(text)
		if err != nil || num < 1 {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: chatID,
				Text:   "Please enter a valid ticket number (e.g. 42).",
			})
			return
		}
		// Check availability
		available, err := checkTicketAvailability(num)
		if err != nil {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: chatID,
				Text:   "Error checking availability. Please try again.",
			})
			return
		}
		if !available {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: chatID,
				Text:   "This ticket is not available. Please choose another number.",
			})
			return
		}
		// Reserve ticket
		paymentID, err := reserveTicket(chatID, num, update.Message.From.FirstName, update.Message.From.Username)
		if err != nil {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: chatID,
				Text:   "Failed to reserve ticket. " + err.Error(),
			})
			return
		}
		// Update state
		statesMu.Lock()
		userStates[chatID] = &BookingState{Step: "awaiting_receipt", TicketNum: num, PaymentID: paymentID}
		statesMu.Unlock()
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   fmt.Sprintf("✅ Ticket #%d reserved! Please upload your payment receipt (screenshot) now.", num),
		})
		return
	}

	switch {
	case strings.Contains(text, "ትኬት") || strings.Contains(text, "book"):
		bookHandler(ctx, b, chatID)
	case strings.Contains(text, "እርዳታ") || strings.Contains(text, "help"):
		helpHandler(ctx, b, chatID)
	case strings.Contains(text, "ድጋፍ") || strings.Contains(text, "support"):
		supportHandler(ctx, b, chatID)
	default:
		if _, err := b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:      chatID,
			Text:        "To book a ticket, type /book or tap the button below 👇",
			ReplyMarkup: bookingReplyKeyboard(),
		}); err != nil {
			log.Printf("failed to send fallback message: %v", err)
		}
	}
}

// photoHandler handles receipt uploads
func photoHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID

	statesMu.RLock()
	state, exists := userStates[chatID]
	statesMu.RUnlock()

	if !exists || state.Step != "awaiting_receipt" {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "Please start the booking process with /book first.",
		})
		return
	}

	// Get the largest photo
	photos := update.Message.Photo
	if len(photos) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "No photo found. Please try again.",
		})
		return
	}
	largest := photos[len(photos)-1]

	// Download the photo from Telegram
	file, err := b.GetFile(ctx, &bot.GetFileParams{FileID: largest.FileID})
	if err != nil {
		log.Printf("failed to get file: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "Failed to process photo. Please try again.",
		})
		return
	}

	fileURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", botToken, file.FilePath)
	resp, err := http.Get(fileURL)
	if err != nil {
		log.Printf("failed to download photo: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "Failed to download photo. Please try again.",
		})
		return
	}
	defer resp.Body.Close()

	// Upload to webapp
	err = uploadReceipt(state.PaymentID, resp.Body, "receipt.jpg")
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "Failed to upload receipt. " + err.Error(),
		})
		return
	}

	// Clear state
	statesMu.Lock()
	delete(userStates, chatID)
	statesMu.Unlock()

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   fmt.Sprintf("🎉 Receipt uploaded! Your booking for ticket #%d is now pending approval. You can check your tickets in the webapp.", state.TicketNum),
	})
}

func reserveTicket(chatID int64, ticketNum int, firstName string, username string) (int, error) {
	data := map[string]interface{}{
		"telegram_id":   chatID,
		"ticket_number": ticketNum,
		"bot_token":     os.Getenv("BOT_API_SECRET"),
		"first_name":    firstName,
		"username":      username,
	}
	jsonData, _ := json.Marshal(data)

	resp, err := http.Post(
		webappAPIBase+"/ticket/reserve",
		"application/json",
		bytes.NewReader(jsonData),
	)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var result struct {
		Success   bool   `json:"success"`
		Message   string `json:"message"`
		PaymentID int    `json:"payment_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}
	if !result.Success {
		return 0, fmt.Errorf(result.Message)
	}
	return result.PaymentID, nil
}

func uploadReceipt(paymentID int, fileReader io.Reader, filename string) error {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("receipt", filename)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, fileReader); err != nil {
		return err
	}

	writer.WriteField("payment_id", strconv.Itoa(paymentID))
	writer.WriteField("bot_token", os.Getenv("BOT_API_SECRET"))

	if err := writer.Close(); err != nil {
		return err
	}

	req, err := http.NewRequest("POST", webappAPIBase+"/ticket/upload-receipt", body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var result struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if !result.Success {
		return fmt.Errorf(result.Message)
	}
	return nil
}

func main() {
	loadEnv(".env")

	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN environment variable is required")
	}
	botToken = token

	if url := os.Getenv("MINIAPP_URL"); url != "" {
		miniAppURL = url
	}

	if apiBase := os.Getenv("WEBAPP_API_BASE"); apiBase != "" {
		webappAPIBase = apiBase
	}

	ctx := context.Background()

	go keepAlive(ctx)

	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}

	http.HandleFunc("/", pingHandler)
	go func() {
		if err := http.ListenAndServe(":"+port, nil); err != nil {
			log.Fatalf("http server failed: %v", err)
		}
	}()

	b, err := bot.New(token)
	if err != nil {
		log.Fatalf("failed to create bot: %v", err)
	}

	me, err := b.GetMe(ctx)
	if err != nil {
		log.Fatalf("failed to call getMe: %v", err)
	}

	log.Printf("authorized on account %s", me.Username)

	b.RegisterHandler(bot.HandlerTypeMessageText, "start", bot.MatchTypeCommand, startHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "book", bot.MatchTypeCommand, func(ctx context.Context, b *bot.Bot, update *models.Update) {
		bookHandler(ctx, b, update.Message.Chat.ID)
	})
	b.RegisterHandler(bot.HandlerTypeMessageText, "help", bot.MatchTypeCommand, func(ctx context.Context, b *bot.Bot, update *models.Update) {
		helpHandler(ctx, b, update.Message.Chat.ID)
	})

	// Must stay last: it handles every other text message.
	b.RegisterHandler(bot.HandlerTypeMessageText, "", bot.MatchTypeContains, textHandler)

	// Handle photos for receipt upload
	b.RegisterHandlerMatchFunc(func(update *models.Update) bool {
		return update.Message != nil && len(update.Message.Photo) > 0
	}, photoHandler)

	setBotCommands(ctx, b)

	b.Start(ctx)
}
