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

// ==================== State ====================

type Step string

const (
	Idle            Step = ""
	AwaitTicket     Step = "await_ticket"     // waiting for user to type a number
	AwaitConfirm    Step = "await_confirm"     // waiting for confirm/cancel button
	AwaitReceipt    Step = "await_receipt"     // waiting for photo
)

type State struct {
	Step      Step
	TicketNum int
	PaymentID int
}

var (
	states   = map[int64]*State{}
	statesMu sync.Mutex
)

func getState(chatID int64) *State {
	statesMu.Lock()
	defer statesMu.Unlock()
	return states[chatID]
}

func setState(chatID int64, s *State) {
	statesMu.Lock()
	defer statesMu.Unlock()
	states[chatID] = s
}

func clearState(chatID int64) {
	statesMu.Lock()
	defer statesMu.Unlock()
	delete(states, chatID)
}

// ==================== Config ====================

var (
	apiBase    = "https://afro.blessed-equb.com/api/bot"
	miniAppURL = "https://afro.blessed-equb.com"
	supportURL = "https://t.me/afroequb"
	botToken   string // Telegram bot token for downloading files
)

// ==================== Callback data ====================

const (
	cbBook       = "BOOK"
	cbConfirm    = "CONFIRM"
	cbCancel     = "CANCEL"
	cbHelp       = "HELP"
	cbSupport    = "SUPPORT"
	cbStart      = "START"
)

// ==================== Helpers ====================

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
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		os.Setenv(strings.TrimSpace(k), strings.Trim(v, `"'`))
	}
}

func pingHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"status":"ok"}`)
}

func keepAlive(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r, err := http.NewRequestWithContext(ctx, "GET", "https://affrbot.onrender.com", nil)
			if err != nil {
				continue
			}
			resp, err := http.DefaultClient.Do(r)
			if err != nil {
				log.Printf("keepAlive ping failed: %v", err)
				continue
			}
			resp.Body.Close()
		}
	}
}

func cbMsgID(cb *models.CallbackQuery) int {
	if cb.Message.Message != nil {
		return cb.Message.Message.ID
	}
	if cb.Message.InaccessibleMessage != nil {
		return cb.Message.InaccessibleMessage.MessageID
	}
	return 0
}

// ==================== Keyboards ====================

func mainMenu() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{
				{Text: "🎟 ቲኬት ይያዙ", CallbackData: cbBook},
			},
			{
				{Text: "ℹ️ እርዳታ", CallbackData: cbHelp},
				{Text: "💬 ድጋፍ", CallbackData: cbSupport},
			},
		},
	}
}

func replyKB() *models.ReplyKeyboardMarkup {
	return &models.ReplyKeyboardMarkup{
		Keyboard: [][]models.KeyboardButton{
			{{Text: "🎟 ቲኬት ይያዙ", WebApp: &models.WebAppInfo{URL: miniAppURL}}},
			{{Text: "ℹ️ እርዳታ"}, {Text: "💬 ድጋፍ"}},
		},
		IsPersistent:   true,
		ResizeKeyboard: true,
	}
}

func confirmKB() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{
				{Text: "✅ አዎ, ያስረክሩ", CallbackData: cbConfirm},
				{Text: "❌ ሰርዝ", CallbackData: cbCancel},
			},
		},
	}
}

// ==================== Bot commands ====================

func setupCommands(ctx context.Context, b *bot.Bot) {
	b.SetMyCommands(ctx, &bot.SetMyCommandsParams{
		Commands: []models.BotCommand{
			{Command: "start", Description: "🚀 ቦቱን ይጀምሩ"},
			{Command: "book", Description: "🎟 ቲኬት ይያዙ"},
			{Command: "help", Description: "ℹ️ እርዳታ"},
		},
	})
}

// ==================== /start ====================

func handleStart(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	clearState(chatID)

	// Welcome photo
	data, err := os.ReadFile("image/having.jpg")
	if err == nil {
		photo := &models.InputFileUpload{Filename: "having.jpg", Data: bytes.NewReader(data)}
		b.SendPhoto(ctx, &bot.SendPhotoParams{
			ChatID:  chatID,
			Photo:   photo,
			Caption: "🎟 ትኬት ይግዙ iPhone 17 Pro Max ይሸለሙ! 📱\n\nየ1 ቲኬት ዋጋ = 500 ብር 💰\n\n🏆 1ኛ እጣ = iPhone 17 Pro Max\n🥈 2ኛ እጣ = 20,000 ብር\n🥉 3ኛ እጣ = 10,000 ብር\n\n✅ ሁሉም በቴሌግራም ውስጥ ይከናወናል!",
			ReplyMarkup: mainMenu(),
		})
	} else {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:      chatID,
			Text:        "🎟 ትኬት ይግዙ iPhone 17 Pro Max ይሸለሙ! 📱\n\nየ1 ቲኬት ዋጋ = 500 ብር 💰\n\n🏆 1ኛ እጣ = iPhone 17 Pro Max\n🥈 2ኛ እጣ = 20,000 ብር\n🥉 3ኛ እጣ = 10,000 ብር\n\n✅ ሁሉም በቴሌግራም ውስጥ ይከናወናል!",
			ReplyMarkup: mainMenu(),
		})
	}

	// Persistent keyboard
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        "👇 ለመጀመር ከታች ያለውን ቁልፍ ይጠቀሙ",
		ReplyMarkup: replyKB(),
	})
}

// ==================== /book ====================

func handleBook(ctx context.Context, b *bot.Bot, chatID int64) {
	setState(chatID, &State{Step: AwaitTicket})

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "🎟 እባክዎ መряይ የሚሹትን ትኬት ቁጥር ይፃፉ\n\n📝 ለምሳሌ፡ 42",
		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{
				{{Text: "❌ ሰርዝ", CallbackData: cbCancel}},
			},
		},
	})
}

// ==================== /help ====================

func handleHelp(ctx context.Context, b *bot.Bot, chatID int64) {
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text: "🎟 ትኬት ለመግዛት ይሄንን ቀላል መንገድ ይከተሉ!\n\n" +
			"1️⃣ «🎟 ቲኬት ይያዙ» ቁልፍ ይንኩ\n" +
			"2️⃣ ትኬት ቁጥርዎን ይፃፉ\n" +
			"3️⃣ 500 ብር ወደ 0955885207 ያስረክቡ\n" +
			"4️⃣ ደረሰኝ screenshot ይላኩ\n\n" +
			"✅ ክፍያዎ ከተረጋገጠ ቲኬትዎ ይቀመጣል!\n\n" +
			"ለማንኛውም ጥያቄ @afroequb",
	})
}

// ==================== Callback handler ====================

func handleCallback(ctx context.Context, b *bot.Bot, update *models.Update) {
	cb := update.CallbackQuery
	chatID := cb.Message.Message.Chat.ID
	msgID := cbMsgID(cb)
	data := cb.Data

	// Always acknowledge
	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: cb.ID,
	})

	switch data {

	case cbStart:
		clearState(chatID)
		b.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID:      chatID,
			MessageID:   msgID,
			Text:        "🎟 እንደገና ይጀምሩ!",
			ReplyMarkup: mainMenu(),
		})

	case cbHelp:
		sendDepositVideo(ctx, b, chatID)

	case cbSupport:
		b.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID:    chatID,
			MessageID: msgID,
			Text:      "ማንኛውም ጥያቄ ካለ ደላላችንን ያግኙን! 🙏",
			ReplyMarkup: &models.InlineKeyboardMarkup{
				InlineKeyboard: [][]models.InlineKeyboardButton{
					{{Text: "💬 ደላላችንን ያግኙን", URL: supportURL}},
					{{Text: "🔙 ወደ ዋና ገጽ", CallbackData: cbStart}},
				},
			},
		})

	case cbBook:
		// Set state and send the ticket number prompt
		setState(chatID, &State{Step: AwaitTicket})
		b.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID:    chatID,
			MessageID: msgID,
			Text:      "🎟 እባክዎ መряይ የሚሹትን ትኬት ቁጥር ይፃፉ\n\n📝 ለምሳሌ፡ 42",
			ReplyMarkup: &models.InlineKeyboardMarkup{
				InlineKeyboard: [][]models.InlineKeyboardButton{
					{{Text: "❌ ሰርዝ", CallbackData: cbCancel}},
				},
			},
		})

	case cbCancel:
		clearState(chatID)
		b.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID:      chatID,
			MessageID:   msgID,
			Text:        "✅ ተ 취ልቷል።",
			ReplyMarkup: mainMenu(),
		})

	case cbConfirm:
		// User confirmed — reserve the ticket
		st := getState(chatID)
		if st == nil || st.Step != AwaitConfirm {
			b.EditMessageText(ctx, &bot.EditMessageTextParams{
				ChatID:      chatID,
				MessageID:   msgID,
				Text:        "⚠️ ጊዜው አልፏል። እንደገና ይጀምሩ።",
				ReplyMarkup: mainMenu(),
			})
			return
		}

		// Show "processing..."
		b.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID:    chatID,
			MessageID: msgID,
			Text:      "⏳ ትኬትዎ በመረጋገጥ ላይ ነው...",
		})

		// Reserve
		paymentID, err := apiReserve(chatID, st.TicketNum, cb.From.FirstName, cb.From.Username)
		if err != nil {
			log.Printf("reserve failed: %v", err)
			b.EditMessageText(ctx, &bot.EditMessageTextParams{
				ChatID:      chatID,
				MessageID:   msgID,
				Text:        fmt.Sprintf("❌ ችግር: %s\n\nእንደገና ይሞክሩ።", err.Error()),
				ReplyMarkup: mainMenu(),
			})
			clearState(chatID)
			return
		}

		// Update state
		setState(chatID, &State{Step: AwaitReceipt, TicketNum: st.TicketNum, PaymentID: paymentID})

		// Show payment details
		b.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID:    chatID,
			MessageID: msgID,
			Text: fmt.Sprintf(
				"✅ ትኬት #%d ተርፏል!\n\n"+
					"💳 የክፍያ መረጃ:\n"+
					"📱 Telebirr: 0955885207\n"+
					"👤 ስም: yirgalem\n"+
					"💰 መጠን: 500 ብር\n\n"+
					"📸 ከተከፈተሉ በኋላ የክፍያ ደረሰኝ screenshot ይላኩ።",
				st.TicketNum,
			),
			ReplyMarkup: &models.InlineKeyboardMarkup{
				InlineKeyboard: [][]models.InlineKeyboardButton{
					{{Text: "📸 ደረሰኝ በዚህ ላይ ይላኩ", WebApp: &models.WebAppInfo{URL: miniAppURL}}},
				},
			},
		})
	}
}

func sendDepositVideo(ctx context.Context, b *bot.Bot, chatID int64) {
	f, err := os.Open("afro equb deposit.mp4")
	if err != nil {
		// No video file — just send text
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text: "🎟 ትኬት ለመግዛት ይሄንን ቀላል መንገድ ይከተሉ!\n\n" +
				"1️⃣ «🎟 ቲኬት ይያዙ» ቁልፍ ይንኩ\n" +
				"2️⃣ ትኬት ቁጥርዎን ይፃፉ\n" +
				"3️⃣ 500 ብር ወደ 0955885207 ያስረክቡ\n" +
				"4️⃣ ደረሰኝ screenshot ይላኩ\n\n" +
				"✅ ክፍያዎ ከተረጋገጠ ቲኬትዎ ይቀመጣል!\n\n" +
				"ለማንኛውም ጥያቄ @afroequb",
			ReplyMarkup: mainMenu(),
		})
		return
	}
	defer f.Close()

	video := &models.InputFileUpload{Filename: "afro equb deposit.mp4", Data: f}
	b.SendVideo(ctx, &bot.SendVideoParams{
		ChatID:      chatID,
		Video:       video,
		Caption:     "🎥 ክፍያ እንዴት እንደሚፈጽሙ የሚያሳይ አጫጭር ቪዲዮ",
		ReplyMarkup: mainMenu(),
	})
}

// ==================== Text handler ====================

func handleText(ctx context.Context, b *bot.Bot, update *models.Update) {
	text := strings.TrimSpace(update.Message.Text)
	lower := strings.ToLower(text)
	chatID := update.Message.Chat.ID

	// Reply keyboard buttons
	switch {
	case strings.Contains(lower, "ትኬት") || lower == "/book" || lower == "book":
		handleBook(ctx, b, chatID)
		return
	case strings.Contains(lower, "እርዳታ") || lower == "/help" || lower == "help":
		handleHelp(ctx, b, chatID)
		return
	case strings.Contains(lower, "ድጋፍ") || strings.Contains(lower, "support"):
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "ማንኛውም ጥያቄ ካለ ደላላችንን ያግኙን! 🙏",
			ReplyMarkup: &models.InlineKeyboardMarkup{
				InlineKeyboard: [][]models.InlineKeyboardButton{
					{{Text: "💬 ደላላችንን ያግኙን", URL: supportURL}},
					{{Text: "🔙 ወደ ዋና ገጽ", CallbackData: cbStart}},
				},
			},
		})
		return
	}

	// Booking conversational step
	st := getState(chatID)
	if st != nil && st.Step == AwaitTicket {
		num, err := strconv.Atoi(text)
		if err != nil || num < 1 {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: chatID,
				Text:   "⚠️ እባክዎ ትክክለኛ ቁጥር ይፃፉ (ለምሳሌ፡ 42)",
			})
			return
		}

		// Check availability
		available, msg, err := apiCheckAvailability(num)
		if err != nil {
			log.Printf("availability check error: %v", err)
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: chatID,
				Text: fmt.Sprintf(
					"⚠️ ችግር ተፈጥሯል: %s\n\n"+
						"እባክዎ እንደገና ይሞክሩ ወይም @afroequb ያነጋግሩን።",
					err.Error(),
				),
				ReplyMarkup: mainMenu(),
			})
			clearState(chatID)
			return
		}

		if !available {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:      chatID,
				Text:        fmt.Sprintf("❌ ትኬት #%d አይደልም: %s\n\nሌላ ቁጥር ይሞክሩ።", num, msg),
				ReplyMarkup: mainMenu(),
			})
			clearState(chatID)
			return
		}

		// Available — show confirmation
		setState(chatID, &State{Step: AwaitConfirm, TicketNum: num})
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text: fmt.Sprintf(
				"📋 ትኬት ማረጋገጫ:\n\n"+
					"🎟 ትኬት: #%d\n"+
					"💰 ዋጋ: 500 ብር\n"+
					"📱 Telebirr: 0955885207\n\n"+
					"ይህን ትኬት መያዝ ይፈልጋሉ?",
				num,
			),
			ReplyMarkup: confirmKB(),
		})
		return
	}

	// Fallback
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        "To book a ticket, tap /book or the button below 👇",
		ReplyMarkup: replyKB(),
	})
}

// ==================== Photo handler ====================

func handlePhoto(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	st := getState(chatID)

	if st == nil || st.Step != AwaitReceipt {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "📸 እባክዎ ትኬት ቀስጥ በመጀመር ቀደም የክፍያ ደረሰኝ ይላኩ።\n\n/book ብለው ይላኩ።",
		})
		return
	}

	photos := update.Message.Photo
	if len(photos) == 0 {
		return
	}
	largest := photos[len(photos)-1]

	// Download from Telegram
	file, err := b.GetFile(ctx, &bot.GetFileParams{FileID: largest.FileID})
	if err != nil {
		log.Printf("GetFile failed: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "⚠️ ፎቶውን ማስኬድ አልተቻለም። እንደገና ይሞክሩ።",
		})
		return
	}

	dlURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", botToken, file.FilePath)
	resp, err := http.Get(dlURL)
	if err != nil {
		log.Printf("download photo failed: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "⚠️ ፎቶውን ማውረድ አልተቻለም። እንደገና ይሞክሩ።",
		})
		return
	}
	defer resp.Body.Close()

	// Upload receipt
	err = apiUploadReceipt(st.PaymentID, resp.Body, "receipt.jpg")
	if err != nil {
		log.Printf("upload receipt failed: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   fmt.Sprintf("⚠️ ደረሰኝ ማስኬድ አልተቻለም: %s\n\nእንደገና ይሞክሩ።", err.Error()),
		})
		return
	}

	clearState(chatID)
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        fmt.Sprintf("🎉 ደረሰኝ ተስርፏል! ትኬት #%d ማረጋገጫ በመጠበቅ ላይ ነው።\n\n✅ ከተረጋገጠ በኋላ ትኬትዎ ይቀመጣል።", st.TicketNum),
		ReplyMarkup: mainMenu(),
	})
}

// ==================== API calls ====================

func apiCheckAvailability(ticketNum int) (bool, string, error) {
	payload := map[string]interface{}{
		"ticket_number": ticketNum,
		"bot_token":     os.Getenv("BOT_API_SECRET"),
	}
	body, _ := json.Marshal(payload)

	url := apiBase + "/ticket/availability"
	log.Printf("API checkAvailability: POST %s body=%s", url, string(body))

	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return false, "", fmt.Errorf(" Cannot reach server. Please try again later. (%w)", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	log.Printf("API checkAvailability: status=%d body=%s", resp.StatusCode, string(respBody))

	if resp.StatusCode != 200 {
		return false, "", fmt.Errorf("Server error (HTTP %d). Please try again.", resp.StatusCode)
	}

	var result struct {
		Available bool   `json:"available"`
		Message   string `json:"message"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return false, "", fmt.Errorf("Unexpected server response. Please try again.")
	}

	msg := result.Message
	if msg == "" {
		msg = result.Status
	}
	return result.Available, msg, nil
}

func apiReserve(chatID int64, ticketNum int, firstName, username string) (int, error) {
	payload := map[string]interface{}{
		"telegram_id":   chatID,
		"ticket_number": ticketNum,
		"bot_token":     os.Getenv("BOT_API_SECRET"),
		"first_name":    firstName,
		"username":      username,
	}
	body, _ := json.Marshal(payload)

	url := apiBase + "/ticket/reserve"
	log.Printf("API reserve: POST %s body=%s", url, string(body))

	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("Cannot reach server. Please try again later.")
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	log.Printf("API reserve: status=%d body=%s", resp.StatusCode, string(respBody))

	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("Server error (HTTP %d). Please try again.", resp.StatusCode)
	}

	var result struct {
		Success   bool   `json:"success"`
		Message   string `json:"message"`
		PaymentID int    `json:"payment_id"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return 0, fmt.Errorf("Unexpected server response.")
	}
	if !result.Success {
		return 0, fmt.Errorf(result.Message)
	}
	return result.PaymentID, nil
}

func apiUploadReceipt(paymentID int, reader io.Reader, filename string) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	part, err := w.CreateFormFile("receipt", filename)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, reader); err != nil {
		return err
	}
	w.WriteField("payment_id", strconv.Itoa(paymentID))
	w.WriteField("bot_token", os.Getenv("BOT_API_SECRET"))
	w.Close()

	url := apiBase + "/ticket/upload-receipt"
	log.Printf("API uploadReceipt: POST %s payment_id=%d", url, paymentID)

	req, err := http.NewRequest("POST", url, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("Cannot reach server. Please try again later.")
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	log.Printf("API uploadReceipt: status=%d body=%s", resp.StatusCode, string(respBody))

	if resp.StatusCode != 200 {
		return fmt.Errorf("Server error (HTTP %d). Please try again.", resp.StatusCode)
	}

	var result struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return fmt.Errorf("Unexpected server response.")
	}
	if !result.Success {
		return fmt.Errorf(result.Message)
	}
	return nil
}

// ==================== Health check ====================

func healthCheck() {
	url := apiBase + "/ticket/availability"
	payload := map[string]interface{}{"ticket_number": 1, "bot_token": "health_check"}
	body, _ := json.Marshal(payload)

	log.Printf("Running API health check: POST %s", url)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("⚠️  HEALTH CHECK FAILED: Cannot reach API at %s: %v", apiBase, err)
		log.Printf("⚠️  Make sure WEBAPP_API_BASE env var is set correctly in .env")
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	log.Printf("API health check: status=%d body=%s", resp.StatusCode, string(respBody))

	if resp.StatusCode == 200 || resp.StatusCode == 403 {
		log.Printf("✅ API is reachable at %s", apiBase)
	} else {
		log.Printf("⚠️  API returned unexpected status %d", resp.StatusCode)
	}
}

// ==================== Main ====================

func main() {
	loadEnv(".env")

	botToken = os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		log.Fatal("❌ TELEGRAM_BOT_TOKEN is required in .env")
	}

	if v := os.Getenv("MINIAPP_URL"); v != "" {
		miniAppURL = v
	}
	if v := os.Getenv("WEBAPP_API_BASE"); v != "" {
		apiBase = v
	}

	log.Printf("Config: API=%s MiniApp=%s", apiBase, miniAppURL)

	// Health check the API before starting
	healthCheck()

	ctx := context.Background()
	go keepAlive(ctx)

	// HTTP server for keep-alive ping
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

	// Create bot
	b, err := bot.New(botToken)
	if err != nil {
		log.Fatalf("Failed to create bot: %v", err)
	}

	me, err := b.GetMe(ctx)
	if err != nil {
		log.Fatalf("Failed to getMe: %v", err)
	}
	log.Printf("✅ Bot authorized: @%s", me.Username)

	// Register handlers
	// 1. Commands first
	b.RegisterHandler(bot.HandlerTypeMessageText, "start", bot.MatchTypeCommand, handleStart)
	b.RegisterHandler(bot.HandlerTypeMessageText, "book", bot.MatchTypeCommand, func(ctx context.Context, b *bot.Bot, update *models.Update) {
		handleBook(ctx, b, update.Message.Chat.ID)
	})
	b.RegisterHandler(bot.HandlerTypeMessageText, "help", bot.MatchTypeCommand, func(ctx context.Context, b *bot.Bot, update *models.Update) {
		handleHelp(ctx, b, update.Message.Chat.ID)
	})

	// 2. Callback queries (inline keyboard buttons)
	b.RegisterHandlerMatchFunc(func(update *models.Update) bool {
		return update.CallbackQuery != nil
	}, handleCallback)

	// 3. Photos for receipt upload
	b.RegisterHandlerMatchFunc(func(update *models.Update) bool {
		return update.Message != nil && len(update.Message.Photo) > 0
	}, handlePhoto)

	// 4. Text messages (catch-all, must be last)
	b.RegisterHandler(bot.HandlerTypeMessageText, "", bot.MatchTypeContains, handleText)

	setupCommands(ctx, b)
	log.Printf("🚀 Bot is running! Send /start to @%s to test.", me.Username)

	b.Start(ctx)
}
