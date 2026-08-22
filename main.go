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

// ---------- State management ----------

type BookingStep string

const (
	StepIdle            BookingStep = ""
	StepAwaitingTicket  BookingStep = "awaiting_ticket"
	StepConfirmReserve  BookingStep = "confirm_reserve"
	StepAwaitingReceipt BookingStep = "awaiting_receipt"
)

type BookingState struct {
	Step      BookingStep
	TicketNum int
	PaymentID int
}

var (
	userStates = make(map[int64]*BookingState)
	statesMu   sync.RWMutex
)

// ---------- Constants ----------

const (
	defaultMiniAppURL = "https://afro.blessed-equb.com"
	supportURL        = "https://t.me/afroequb"
	pingURL           = "https://affrbot.onrender.com"
	defaultAPIBase    = "https://afro.blessed-equb.com/api/bot"
)

var (
	miniAppURL    = defaultMiniAppURL
	webappAPIBase = defaultAPIBase
	botToken      string // set in main()
)

// ---------- Callback data constants ----------

const (
	CallbackBookStart   = "book_start"
	CallbackBookConfirm = "book_confirm:%d" // %d = ticket number
	CallbackBookCancel  = "book_cancel"
	CallbackHelp        = "help"
	CallbackSupport     = "support"
)

// ---------- Messages ----------

const welcomeMessage = `🎟 ትኬት ይግዙ iPhone 17 Pro Max ይሸለሙ! 📱

የ1 ቲኬት ዋጋ = 500 ብር 💰

🏆 1ኛ እጣ = iPhone 17 Pro Max
🥈 2ኛ እጣ = 20,000 ብር
🥉 3ኛ እጣ = 10,000 ብር
💰 30 እድለኛ ተሳታፊዎች = 500 ብር የትኬት ክፍያቸው ይመለስላቸዋል!

✅ ሁሉም በቴሌግራም ውስጥ ይከናወናል!`

const bookInstructions = `🎟 ቲኬት መያዝ እጅግ ቀላል ነው!

1️⃣ ከታች «አሁን ቲኬት ይያዙ» የሚለውን ቁልፍ ይንኩ
2️⃣ ክፍት የሆነ እድል ቁጥር ይምረጡ
3️⃣ 500 ብር ወደሚታየለት ሂሳብ ያስረክቡ
4️⃣ የክፍያ ደረሰኝ screenshot ያስገቡ

ክፍያዎ ከተረጋገጠ በኋላ ቲኬትዎ ይቀመጣል ✅`

const helpMessage = `ትኬቱን ለመግዛት ይሄንን ቀላል መንገድ ይከተሉ! 🎟

1️⃣ «🎟 ቲኬት ይያዙ» ቁልፍ ይንኩ ወይም /book ብለው ይላኩ
2️⃣ ከተከፈተለው ገጽ ላይ ክፍት የሆነ እድል ቁጥር ይምረጡ
3️⃣ 500 ብር ወደሚታየለት ሂሳብ (Telebirr) ያስረክቡ
4️⃣ የላኩትን ደረሰኝ screenshot በዚያው ገጽ ላይ ያስገቡ
5️⃣ ክፍያዎ ከተረጋገጠ ቲኬትዎ ተመዝግቧል ✅

ማንኛውም ችግር ካለ እኛን ያነጋግሩን @afroequb`

const videoCaption = `🎥 ክፍያ እንዴት እንደሚፈጽሙ የሚያሳይ አጫጭር ቪዲዮ`

const paymentDetails = `💳 የክፍያ መረጃ

📱 Telebirr ሂሳብ: 0955885207
👤 ስም: yirgalem
💰 መጠን: 500 ብር

🔑 ትኬት ቁጥር: #%d

⚠️ እባክዎ የተመሰጠውን ትኬት ቁጥር ብቻ ያስረክቡ!`

// ---------- Helpers ----------

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

// cbChatID extracts the chat ID from a callback query's MaybeInaccessibleMessage.
func cbChatID(cb *models.CallbackQuery) int64 {
	if cb.Message.Message != nil {
		return cb.Message.Message.Chat.ID
	}
	if cb.Message.InaccessibleMessage != nil {
		return cb.Message.InaccessibleMessage.Chat.ID
	}
	return 0
}

// cbMessageID extracts the message ID from a callback query's MaybeInaccessibleMessage.
func cbMessageID(cb *models.CallbackQuery) int {
	if cb.Message.Message != nil {
		return cb.Message.Message.ID
	}
	if cb.Message.InaccessibleMessage != nil {
		return cb.Message.InaccessibleMessage.MessageID
	}
	return 0
}

// ---------- Keyboards ----------

func mainMenuKeyboard() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: "🎟 ቲኬት ይያዙ", CallbackData: CallbackBookStart}},
			{{Text: "ℹ️ እርዳታ", CallbackData: CallbackHelp}},
			{{Text: "💬 ድጋፍ", CallbackData: CallbackSupport}},
		},
	}
}

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

func supportInlineKeyboard() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: "💬 ደላላችንን ያግኙን", URL: supportURL}},
		},
	}
}

func confirmReserveKeyboard(ticketNum int) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{
				{Text: "✅ አዎ, ያስረክሩ", CallbackData: fmt.Sprintf(CallbackBookConfirm, ticketNum)},
				{Text: "❌ ሰርዝ", CallbackData: CallbackBookCancel},
			},
		},
	}
}

// ---------- Commands ----------

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

// clearState removes any in-progress booking state for a user.
func clearState(chatID int64) {
	statesMu.Lock()
	delete(userStates, chatID)
	statesMu.Unlock()
}

// ---------- /start ----------

func startHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	clearState(chatID)

	// Send welcome photo
	fileData, err := os.ReadFile("image/having.jpg")
	if err != nil {
		log.Printf("failed to read image: %v", err)
		return
	}

	photo := &models.InputFileUpload{Filename: "having.jpg", Data: bytes.NewReader(fileData)}

	if _, err := b.SendPhoto(ctx, &bot.SendPhotoParams{
		ChatID:      chatID,
		Photo:       photo,
		Caption:     welcomeMessage,
		ReplyMarkup: mainMenuKeyboard(),
	}); err != nil {
		log.Printf("failed to send welcome: %v", err)
	}

	// Persistent reply keyboard
	if _, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        "👇 ለመጀመር ከታች ያለውን ቁልፍ ይጠቀሙ",
		ReplyMarkup: bookingReplyKeyboard(),
	}); err != nil {
		log.Printf("failed to send keyboard: %v", err)
	}

	sendDepositVideo(ctx, b, chatID, videoCaption)
}

// ---------- /book ----------

func bookHandler(ctx context.Context, b *bot.Bot, chatID int64) {
	clearState(chatID)

	statesMu.Lock()
	userStates[chatID] = &BookingState{Step: StepAwaitingTicket}
	statesMu.Unlock()

	keyboard := &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: "❌ ሰርዝ", CallbackData: CallbackBookCancel}},
		},
	}

	if _, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        "🎟 እባክዎ መряይ የሚሹትን ትኬት ቁጥር ይፃፉ\n\n📝 ለምሳሌ፡ 42",
		ReplyMarkup: keyboard,
	}); err != nil {
		log.Printf("failed to send book prompt: %v", err)
	}
}

// ---------- Callback query handler ----------

func callbackQueryHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	cb := update.CallbackQuery
	chatID := cbChatID(cb)
	msgID := cbMessageID(cb)
	data := cb.Data

	// Acknowledge the callback
	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: cb.ID,
	})

	switch {
	case data == CallbackBookStart:
		bookHandler(ctx, b, chatID)

	case data == CallbackHelp:
		sendDepositVideo(ctx, b, chatID, helpMessage)

	case data == CallbackSupport:
		if _, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID:      chatID,
			MessageID:   msgID,
			Text:        "ማንኛውም ጥያቄ ወይም ችግር ካለ ደላላችንን ያግኙን, በፍጥነት እንመልሳለን! 🙏",
			ReplyMarkup: supportInlineKeyboard(),
		}); err != nil {
			log.Printf("failed to edit support message: %v", err)
		}

	case data == CallbackBookCancel:
		clearState(chatID)
		if _, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID:      chatID,
			MessageID:   msgID,
			Text:        "✅ ተ취ልቷል። ማንኛውም ጊዜ ከታች ቁልፍ በመጠቀም መያዝ ይችላሉ።",
			ReplyMarkup: mainMenuKeyboard(),
		}); err != nil {
			log.Printf("failed to edit cancel message: %v", err)
		}

	default:
		// Handle book_confirm:<ticketNum>
		if strings.HasPrefix(data, "book_confirm:") {
			ticketNumStr := strings.TrimPrefix(data, "book_confirm:")
			ticketNum, err := strconv.Atoi(ticketNumStr)
			if err != nil {
				return
			}

			statesMu.RLock()
			state, exists := userStates[chatID]
			statesMu.RUnlock()

			if !exists || state.Step != StepConfirmReserve {
				return
			}

			// Show "processing" message
			b.EditMessageText(ctx, &bot.EditMessageTextParams{
				ChatID:    chatID,
				MessageID: msgID,
				Text:      "⏳ ትኬትዎ በመረጋገጥ ላይ ነው...",
			})

			// Actually reserve the ticket
			firstName := cb.From.FirstName
			username := cb.From.Username

			paymentID, err := reserveTicket(chatID, ticketNum, firstName, username)
			if err != nil {
				b.EditMessageText(ctx, &bot.EditMessageTextParams{
					ChatID:      chatID,
					MessageID:   msgID,
					Text:        fmt.Sprintf("❌ ችግር ተፈጥሯል: %s\n\nእባክዎ እንደገና ይሞክሩ።", err.Error()),
					ReplyMarkup: mainMenuKeyboard(),
				})
				clearState(chatID)
				return
			}

			// Update state to awaiting receipt
			statesMu.Lock()
			userStates[chatID] = &BookingState{
				Step:      StepAwaitingReceipt,
				TicketNum: ticketNum,
				PaymentID: paymentID,
			}
			statesMu.Unlock()

			// Show payment details
			paymentText := fmt.Sprintf(paymentDetails, ticketNum)
			receiptKeyboard := &models.InlineKeyboardMarkup{
				InlineKeyboard: [][]models.InlineKeyboardButton{
					{{Text: "📸 ደረሰኝ አስገቡ", WebApp: webAppInfo()}},
				},
			}

			b.EditMessageText(ctx, &bot.EditMessageTextParams{
				ChatID:      chatID,
				MessageID:   msgID,
				Text:        fmt.Sprintf("✅ ትኬት #%d ተርፏል!\n\n%s\n\n📸 ከተከፈተሉ በኋላ የክፍያ ደረሰኝ screenshot ይላኩ።", ticketNum, paymentText),
				ReplyMarkup: receiptKeyboard,
			})
		}
	}
}

// ---------- Text handler ----------

func textHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	text := strings.TrimSpace(update.Message.Text)
	lower := strings.ToLower(text)
	chatID := update.Message.Chat.ID

	statesMu.RLock()
	state, exists := userStates[chatID]
	statesMu.RUnlock()

	// Handle reply keyboard buttons
	switch {
	case strings.Contains(lower, "ትኬት") || lower == "/book" || strings.Contains(lower, "book"):
		bookHandler(ctx, b, chatID)
		return
	case strings.Contains(lower, "እርዳታ") || lower == "/help" || strings.Contains(lower, "help"):
		sendDepositVideo(ctx, b, chatID, helpMessage)
		return
	case strings.Contains(lower, "ድጋፍ") || strings.Contains(lower, "support"):
		if _, err := b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:      chatID,
			Text:        "ማንኛውም ጥያቄ ወይም ችግር ካለ ደላላችንን ያግኙን, በፍጥነት እንመልሳለን! 🙏",
			ReplyMarkup: supportInlineKeyboard(),
		}); err != nil {
			log.Printf("failed to send support: %v", err)
		}
		return
	}

	// Handle conversational booking steps
	if exists && state.Step == StepAwaitingTicket {
		num, err := strconv.Atoi(text)
		if err != nil || num < 1 {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: chatID,
				Text:   "⚠️ እባክዎ ትክክለኛ ትኬት ቁጥር ይፃፉ (ለምሳሌ፡ 42)",
			})
			return
		}

		// Check availability via API
		available, ticketMsg, err := checkTicketAvailability(num)
		if err != nil {
			log.Printf("availability check failed for ticket %d: %v", num, err)
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: chatID,
				Text:   fmt.Sprintf("⚠️ ችግር ተፈጥሯል: %s\n\nእባክዎ እንደገና ይሞክሩ።", err.Error()),
			})
			return
		}
		if !available {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:      chatID,
				Text:        fmt.Sprintf("❌ ትኬት #%d አይደልም: %s", num, ticketMsg),
				ReplyMarkup: mainMenuKeyboard(),
			})
			return
		}

		// Show confirmation
		statesMu.Lock()
		userStates[chatID] = &BookingState{Step: StepConfirmReserve, TicketNum: num}
		statesMu.Unlock()

		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text: fmt.Sprintf(
				"📋 ትኬት ማረጋገጫ\n\n"+
					"🎟 ትኬት ቁጥር: #%d\n"+
					"💰 ዋጋ: 500 ብር\n"+
					"📱 ሂሳብ: 0955885207\n\n"+
					"ይህን ትኬት መያዝ ይፈልጋሉ?",
				num,
			),
			ReplyMarkup: confirmReserveKeyboard(num),
		})
		return
	}

	// Fallback
	if _, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        "To book a ticket, type /book or tap the button below 👇",
		ReplyMarkup: bookingReplyKeyboard(),
	}); err != nil {
		log.Printf("failed to send fallback message: %v", err)
	}
}

// ---------- Photo handler ----------

func photoHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID

	statesMu.RLock()
	state, exists := userStates[chatID]
	statesMu.RUnlock()

	if !exists || state.Step != StepAwaitingReceipt {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "📸 እባክዎ ትኬት ቀስጥ በመጀመር ቀደም የክፍያ ደረሰኝ ይላኩ።\n\n/book ብለው ይላኩ።",
		})
		return
	}

	photos := update.Message.Photo
	if len(photos) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "⚠️ ፎቶ አልተገኘም። እንደገና ይሞክሩ።",
		})
		return
	}
	largest := photos[len(photos)-1]

	// Get file from Telegram
	file, err := b.GetFile(ctx, &bot.GetFileParams{FileID: largest.FileID})
	if err != nil {
		log.Printf("failed to get file: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "⚠️ ፎቶውን ማስኬድ አልተቻለም። እንደገና ይሞክሩ።",
		})
		return
	}

	fileURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", botToken, file.FilePath)
	resp, err := http.Get(fileURL)
	if err != nil {
		log.Printf("failed to download photo: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "⚠️ ፎቶውን ማውረድ አልተቻለም። እንደገና ይሞክሩ።",
		})
		return
	}
	defer resp.Body.Close()

	// Upload to webapp
	err = uploadReceipt(state.PaymentID, resp.Body, "receipt.jpg")
	if err != nil {
		log.Printf("failed to upload receipt: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   fmt.Sprintf("⚠️ ደረሰኝ ማስኬድ አልተቻለም: %s\n\nእንደገና ይሞክሩ።", err.Error()),
		})
		return
	}

	clearState(chatID)

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        fmt.Sprintf("🎉 ደረሰኝ ተስርፏል! ትኬት #%d ማረጋገጫ በመጠበቅ ላይ ነው።\n\n✅ ከተረጋገጠ በኋላ ትኬትዎ ይቀመጣል።", state.TicketNum),
		ReplyMarkup: mainMenuKeyboard(),
	})
}

// ---------- API calls ----------

// checkTicketAvailability calls the Laravel API. Returns (available, message, error).
func checkTicketAvailability(ticketNum int) (bool, string, error) {
	data := map[string]interface{}{
		"ticket_number": ticketNum,
		"bot_token":     os.Getenv("BOT_API_SECRET"),
	}
	jsonData, _ := json.Marshal(data)

	url := webappAPIBase + "/ticket/availability"
	log.Printf("checking availability: POST %s payload=%s", url, string(jsonData))

	resp, err := http.Post(url, "application/json", bytes.NewReader(jsonData))
	if err != nil {
		return false, "", fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, "", fmt.Errorf("failed to read response: %w", err)
	}

	log.Printf("availability response: status=%d body=%s", resp.StatusCode, string(body))

	if resp.StatusCode != http.StatusOK {
		return false, "", fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Available bool   `json:"available"`
		Message   string `json:"message"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return false, "", fmt.Errorf("invalid JSON response: %s", string(body))
	}

	if result.Message != "" {
		return result.Available, result.Message, nil
	}
	if result.Status != "" {
		return result.Available, result.Status, nil
	}
	return result.Available, "unknown", nil
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

	url := webappAPIBase + "/ticket/reserve"
	log.Printf("reserving ticket: POST %s payload=%s", url, string(jsonData))

	resp, err := http.Post(url, "application/json", bytes.NewReader(jsonData))
	if err != nil {
		return 0, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("failed to read response: %w", err)
	}

	log.Printf("reserve response: status=%d body=%s", resp.StatusCode, string(body))

	var result struct {
		Success   bool   `json:"success"`
		Message   string `json:"message"`
		PaymentID int    `json:"payment_id"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, fmt.Errorf("invalid JSON response: %s", string(body))
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

	url := webappAPIBase + "/ticket/upload-receipt"
	log.Printf("uploading receipt: POST %s payment_id=%d", url, paymentID)

	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	log.Printf("upload receipt response: status=%d body=%s", resp.StatusCode, string(respBody))

	var result struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return fmt.Errorf("invalid JSON response: %s", string(respBody))
	}
	if !result.Success {
		return fmt.Errorf(result.Message)
	}
	return nil
}

// ---------- Main ----------

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

	// Command handlers
	b.RegisterHandler(bot.HandlerTypeMessageText, "start", bot.MatchTypeCommand, startHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "book", bot.MatchTypeCommand, func(ctx context.Context, b *bot.Bot, update *models.Update) {
		bookHandler(ctx, b, update.Message.Chat.ID)
	})
	b.RegisterHandler(bot.HandlerTypeMessageText, "help", bot.MatchTypeCommand, func(ctx context.Context, b *bot.Bot, update *models.Update) {
		sendDepositVideo(ctx, b, update.Message.Chat.ID, helpMessage)
	})

	// Callback query handler (inline keyboard buttons)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "", bot.MatchTypePrefix, callbackQueryHandler)

	// Text handler for conversational flow (must stay last)
	b.RegisterHandler(bot.HandlerTypeMessageText, "", bot.MatchTypeContains, textHandler)

	// Photo handler for receipt uploads
	b.RegisterHandlerMatchFunc(func(update *models.Update) bool {
		return update.Message != nil && len(update.Message.Photo) > 0
	}, photoHandler)

	setBotCommands(ctx, b)

	b.Start(ctx)
}
