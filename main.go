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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// ==================== Data ====================

type Booking struct {
	ID          int       `json:"id"`
	UserID      int64     `json:"user_id"`
	UserName    string    `json:"user_name"`
	UserPhone   string    `json:"user_phone"`
	TicketNum   int       `json:"ticket_number"`
	Status      string    `json:"status"`
	PaymentID   int       `json:"payment_id,omitempty"`
	ReceiptFile string    `json:"receipt_file,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type bookingsFile struct {
	NextID   int       `json:"next_id"`
	Bookings []Booking `json:"bookings"`
}

// ==================== State ====================

type Step string

const (
	Idle         Step = ""
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
func clearState(id int64) { statesMu.Lock(); defer statesMu.Unlock(); delete(states, id) }

// ==================== Config ====================

var (
	adminIDs    []int64
	paymentAcct = "0955885207"
	paymentName = "yirgalem"
	price       = 500
	dataDir     = "data"
	miniAppURL  = "https://afro.blessed-equb.com"
	supportURL  = "https://t.me/afroequb"
	apiBase     = "https://afro.blessed-equb.com/api/bot"
	botTokenStr string
)

// ==================== Callback data ====================

const (
	cbBook    = "BOOK"
	cbConfirm = "CONFIRM"
	cbCancel  = "CANCEL"
	cbHelp    = "HELP"
	cbSupport = "SUPPORT"
	cbStart   = "START"
	cbMyBook  = "MYBOOK"
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

// ==================== Data persistence ====================

func ensureDataDir() {
	os.MkdirAll(filepath.Join(dataDir, "receipts"), 0o755)
	os.MkdirAll(filepath.Join(dataDir, "bookings"), 0o755)
}

func loadBookings() bookingsFile {
	path := filepath.Join(dataDir, "bookings", "bookings.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return bookingsFile{NextID: 1}
	}
	var bf bookingsFile
	json.Unmarshal(b, &bf)
	if bf.NextID == 0 {
		bf.NextID = 1
	}
	return bf
}

func saveBookings(bf bookingsFile) {
	path := filepath.Join(dataDir, "bookings", "bookings.json")
	b, _ := json.MarshalIndent(bf, "", "  ")
	os.WriteFile(path, b, 0o644)
}

func addBooking(b Booking) Booking {
	bf := loadBookings()
	b.ID = bf.NextID
	bf.NextID++
	b.CreatedAt = time.Now()
	bf.Bookings = append(bf.Bookings, b)
	saveBookings(bf)
	return b
}

func updateBookingPayment(id int, paymentID int) {
	bf := loadBookings()
	for i := range bf.Bookings {
		if bf.Bookings[i].ID == id {
			bf.Bookings[i].PaymentID = paymentID
			bf.Bookings[i].Status = "PENDING"
			break
		}
	}
	saveBookings(bf)
}

func updateBookingReceipt(id int, status string, receipt string) {
	bf := loadBookings()
	for i := range bf.Bookings {
		if bf.Bookings[i].ID == id {
			bf.Bookings[i].Status = status
			if receipt != "" {
				bf.Bookings[i].ReceiptFile = receipt
			}
			break
		}
	}
	saveBookings(bf)
}

// ==================== API calls ====================

func apiCheckTicket(ticketNum int) (bool, string) {
	payload := map[string]interface{}{
		"ticket_number": ticketNum,
		"bot_token":     os.Getenv("BOT_API_SECRET"),
	}
	body, _ := json.Marshal(payload)

	resp, err := http.Post(apiBase+"/ticket/availability", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("API checkTicket error: %v", err)
		return true, "API unavailable, proceeding"
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	log.Printf("API checkTicket: status=%d body=%s", resp.StatusCode, string(respBody))

	if resp.StatusCode != 200 {
		return true, "API unavailable, proceeding"
	}

	var result struct {
		Available bool   `json:"available"`
		Message   string `json:"message"`
	}
	json.Unmarshal(respBody, &result)
	return result.Available, result.Message
}

func apiReserve(chatID int64, ticketNum int, firstName, phone string) (int, error) {
	payload := map[string]interface{}{
		"telegram_id":   chatID,
		"ticket_number": ticketNum,
		"bot_token":     os.Getenv("BOT_API_SECRET"),
		"first_name":    firstName,
		"phone":         phone,
	}
	body, _ := json.Marshal(payload)

	resp, err := http.Post(apiBase+"/ticket/reserve", "application/json", bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("API unavailable: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	log.Printf("API reserve: status=%d body=%s", resp.StatusCode, string(respBody))

	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("API returned %d", resp.StatusCode)
	}

	var result struct {
		Success   bool   `json:"success"`
		Message   string `json:"message"`
		PaymentID int    `json:"payment_id"`
	}
	json.Unmarshal(respBody, &result)
	if !result.Success {
		return 0, fmt.Errorf(result.Message)
	}
	return result.PaymentID, nil
}

func apiUploadReceipt(paymentID int, filePath string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := w.CreateFormFile("receipt", "receipt.jpg")
	io.Copy(part, f)
	w.WriteField("payment_id", strconv.Itoa(paymentID))
	w.WriteField("bot_token", os.Getenv("BOT_API_SECRET"))
	w.Close()

	req, _ := http.NewRequest("POST", apiBase+"/ticket/upload-receipt", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("API unavailable: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	log.Printf("API uploadReceipt: status=%d body=%s", resp.StatusCode, string(respBody))

	return nil
}

// ==================== Keyboards ====================

func mainMenu() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: "🎟 ቲኬት ይያዙ", CallbackData: cbBook}},
			{{Text: "📋 የእኔ ቲኬቶች", CallbackData: cbMyBook}},
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

func contactRequestKB() *models.ReplyKeyboardMarkup {
	return &models.ReplyKeyboardMarkup{
		Keyboard: [][]models.KeyboardButton{
			{
				{
					Text:            "📱 ስልክ ቁጥር ላክ",
					RequestContact:  true,
				},
			},
			{
				{Text: "❌ ሰርዝ"},
			},
		},
		ResizeKeyboard:        true,
		OneTimeKeyboard:       true,
		InputFieldPlaceholder: "ስልክ ቁጥር ይፃፉ ወይም ከታች ቁልፍ ይንኩ",
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

// ==================== Handlers ====================

func pingHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"status":"ok"}`)
}

func startHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	clearState(chatID)

	fileData, err := os.ReadFile("image/having.jpg")
	if err == nil {
		b.SendPhoto(ctx, &bot.SendPhotoParams{
			ChatID:  chatID,
			Photo:   &models.InputFileUpload{Filename: "having.jpg", Data: bytes.NewReader(fileData)},
			Caption: "🎟 ትኬት ይግዙ iPhone 17 Pro Max ይሸለሙ! 📱\n\nየ1 ቲኬት ዋጋ = 500 ብር 💰\n\n🏆 1ኛ እጣ = iPhone 17 Pro Max\n🥈 2ኛ እጣ = 20,000 ብር\n🥉 3ኛ እጣ = 10,000 ብር\n\n✅ ሁሉም በቴሌግራም ውስጥ ይከናወናል!",
			ReplyMarkup: mainMenu(),
		})
	} else {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:      chatID,
			Text:        "🎟 ትኬት ይግዙ iPhone 17 Pro Max ይሸለሙ! 📱\n\nየ1 ቲኬት ዋጋ = 500 ብር 💰\n\n🏆 1ኛ እጣ = iPhone 17 Pro Max\n🥈 2ኛ እጣ = 20,000 ብር\n🥉 3ኛ እጣ = 10,000 ብር",
			ReplyMarkup: mainMenu(),
		})
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        "👇 ለመጀመር ከታች ያለውን ቁልፍ ይጠቀሙ",
		ReplyMarkup: replyKB(),
	})

	// Send deposit video
	f, err := os.Open("afro equb deposit.mp4")
	if err == nil {
		defer f.Close()
		b.SendVideo(ctx, &bot.SendVideoParams{
			ChatID:  chatID,
			Video:   &models.InputFileUpload{Filename: "deposit.mp4", Data: f},
			Caption: "🎥 ክፍያ እንዴት እንደሚፈጽሙ የሚያሳይ አጫጭር ቪዲዮ",
		})
	}
}

func helpHandler(ctx context.Context, b *bot.Bot, chatID int64) {
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text: "🎟 ትኬት ለመግዛት ይሄንን ቀላል መንገድ ይከተሉ!\n\n" +
			"1️⃣ «🎟 ቲኬት ይያዙ» ቁልፍ ይንኩ\n" +
			"2️⃣ ትኬት ቁጥርዎን ይፃፉ\n" +
			"3️⃣ ስልክ ቁጥርዎን ያጋሩ\n" +
			"4️⃣ 500 ብር ወደ " + paymentAcct + " ያስረክቡ\n" +
			"5️⃣ ደረሰኝ screenshot ይላኩ\n\n" +
			"✅ ክፍያዎ ከተረጋገጠ ቲኬትዎ ይቀመጣል!",
		ReplyMarkup: mainMenu(),
	})
}

func supportHandler(ctx context.Context, b *bot.Bot, chatID int64) {
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
}

func bookHandler(ctx context.Context, b *bot.Bot, chatID int64) {
	setState(chatID, &UserState{Step: AwaitNum})
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "🎟 እባክዎ መряይ የሚሹትን ትኬት ቁጥር ይፃፉ\n\n📝 ለምሳሌ፡ 42\n\n(1 እስከ 5000)",
		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{
				{{Text: "❌ ሰርዝ", CallbackData: cbCancel}},
			},
		},
	})
}

func myBookingsHandler(ctx context.Context, b *bot.Bot, chatID int64) {
	bf := loadBookings()
	var my []Booking
	for _, bk := range bf.Bookings {
		if bk.UserID == chatID {
			my = append(my, bk)
		}
	}
	if len(my) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:      chatID,
			Text:        "📋 ምንም ቲኬት የለዎትም!\n\n🎟 ቲኬት ለመያዝ ከታች ቁልፍ ይንኩ!",
			ReplyMarkup: mainMenu(),
		})
		return
	}
	var sb strings.Builder
	sb.WriteString("📋 የእኔ ቲኬቶች:\n\n")
	for _, bk := range my {
		status := "⏳ ማረጋገጫ በመጠበቅ ላይ"
		switch bk.Status {
		case "CONFIRMED", "SOLD":
			status = "✅ ተረጋግጧል"
		case "PAID":
			status = "💰 ክፍያ ተገብቧል"
		case "CANCELLED":
			status = "❌ ተሰርቧል"
		}
		sb.WriteString(fmt.Sprintf("🎟 #%d | 📱 %s | %s\n", bk.TicketNum, bk.UserPhone, status))
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        sb.String(),
		ReplyMarkup: mainMenu(),
	})
}

func callbackHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	cb := update.CallbackQuery
	chatID := cb.Message.Message.Chat.ID
	msgID := cb.Message.Message.ID

	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: cb.ID})

	switch cb.Data {
	case cbStart:
		clearState(chatID)
		b.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID: chatID, MessageID: msgID, Text: "🎟 እንደገና ይጀምሩ!", ReplyMarkup: mainMenu(),
		})
	case cbHelp:
		helpHandler(ctx, b, chatID)
	case cbSupport:
		supportHandler(ctx, b, chatID)
	case cbBook:
		clearState(chatID)
		setState(chatID, &UserState{Step: AwaitNum})
		b.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID: chatID, MessageID: msgID,
			Text: "🎟 እባክዎ መряይ የሚሹትን ትኬት ቁጥር ይፃፉ\n\n📝 ለምሳሌ፡ 42\n\n(1 እስከ 5000)",
			ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
				{{Text: "❌ ሰርዝ", CallbackData: cbCancel}},
			}},
		})
	case cbCancel:
		clearState(chatID)
		b.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID: chatID, MessageID: msgID, Text: "✅ ተ 취ልቷል።", ReplyMarkup: mainMenu(),
		})
	case cbMyBook:
		myBookingsHandler(ctx, b, chatID)
	case cbConfirm:
		st := getState(chatID)
		if st == nil || st.Step != AwaitConfirm {
			b.EditMessageText(ctx, &bot.EditMessageTextParams{
				ChatID: chatID, MessageID: msgID, Text: "⚠️ ጊዜው አልፏል። እንደገና ይጀምሩ።",
				ReplyMarkup: mainMenu(),
			})
			return
		}

		b.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID: chatID, MessageID: msgID, Text: "⏳ ትኬትዎ በመመዝገብ ላይ ነው...",
		})

		user := cb.From
		name := user.FirstName
		if user.LastName != "" {
			name += " " + user.LastName
		}

		// Try API first, fallback to local
		paymentID, err := apiReserve(chatID, st.Ticket, name, st.Phone)
		if err != nil {
			log.Printf("API reserve failed, using local: %v", err)
			// Store locally
			bk := addBooking(Booking{
				UserID:    chatID,
				UserName:  name,
				UserPhone: st.Phone,
				TicketNum: st.Ticket,
				Status:    "PENDING",
			})
			log.Printf("Local booking created: ID=%d", bk.ID)
		} else {
			log.Printf("API booking created: paymentID=%d", paymentID)
			addBooking(Booking{
				UserID:    chatID,
				UserName:  name,
				UserPhone: st.Phone,
				TicketNum: st.Ticket,
				Status:    "PENDING",
				PaymentID: paymentID,
			})
		}

		setState(chatID, &UserState{Step: AwaitReceipt, Ticket: st.Ticket, Phone: st.Phone, Name: name})

		paymentText := fmt.Sprintf(
			"✅ ትኬት #%d ተመዝግቧል!\n\n"+
				"💳 የክፍያ መረጃ:\n"+
				"📱 Telebirr: %s\n"+
				"👤 ስም: %s\n"+
				"💰 መጠን: %d ብር\n\n"+
				"📸 ከተከፈተሉ በኋላ የክፍያ ደረሰኝ screenshot ይላኩ።\n\n"+
				"⚠️ እባክዎ ትኬት ቁጥር #%d ብቻ ያስረክቡ!",
			st.Ticket, paymentAcct, paymentName, price, st.Ticket,
		)
		b.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID: chatID, MessageID: msgID, Text: paymentText,
		})

		// Notify admin
		notifyAdmin(ctx, b, fmt.Sprintf(
			"🆕 አዲስ ትኬት ቀይቧል!\n\n"+
				"🎟 ትኬት: #%d\n"+
				"👤 ስም: %s\n"+
				"📱 ስልክ: %s\n"+
				"🆔 ቴሌግራም: %d",
			st.Ticket, name, st.Phone, chatID,
		))
	}
}

func textHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	text := strings.TrimSpace(update.Message.Text)
	lower := strings.ToLower(text)
	chatID := update.Message.Chat.ID

	// Reply keyboard buttons
	switch {
	case strings.Contains(lower, "ትኬት") || lower == "/book":
		bookHandler(ctx, b, chatID)
		return
	case strings.Contains(lower, "እርዳታ") || lower == "/help":
		helpHandler(ctx, b, chatID)
		return
	case strings.Contains(lower, "ድጋፍ") || strings.Contains(lower, "support"):
		supportHandler(ctx, b, chatID)
		return
	case lower == "❌ ሰርዝ":
		clearState(chatID)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID, Text: "✅ ተ 취ልቷል።", ReplyMarkup: mainMenu(),
		})
		return
	}

	st := getState(chatID)
	if st == nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID, Text: "To book a ticket, type /book or tap the button 👇",
			ReplyMarkup: replyKB(),
		})
		return
	}

	switch st.Step {
	case AwaitNum:
		num, err := strconv.Atoi(text)
		if err != nil || num < 1 || num > 5000 {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: chatID, Text: "⚠️ እባክዎ ትክክለኛ ቁጥር ይፃፉ (1-5000)\n\n📝 ለምሳሌ፡ 42",
			})
			return
		}

		// Check if already taken (local)
		bf := loadBookings()
		for _, bk := range bf.Bookings {
			if bk.TicketNum == num && bk.Status != "CANCELLED" {
				b.SendMessage(ctx, &bot.SendMessageParams{
					ChatID: chatID, Text: fmt.Sprintf("❌ ትኬት #%d አይደልም። ሌላ ቁጥር ይሞክሩ።", num),
					ReplyMarkup: mainMenu(),
				})
				clearState(chatID)
				return
			}
		}

		// Check API availability (best-effort)
		available, _ := apiCheckTicket(num)
		if !available {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: chatID, Text: fmt.Sprintf("❌ ትኬት #%d አይደልም። ሌላ ቁጥር ይሞክሩ።", num),
				ReplyMarkup: mainMenu(),
			})
			clearState(chatID)
			return
		}

		// Ask for contact
		setState(chatID, &UserState{Step: AwaitContact, Ticket: num})
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text: fmt.Sprintf(
				"📱 ትኬት #%d ተመርጧል!\n\n"+
					"እባክዎ ስልክ ቁጥርዎን ያጋሩ ወይም ከታች «📱 ስልክ ቁጥር ላክ» ቁልፍ ይንኩ።",
				num,
			),
			ReplyMarkup: contactRequestKB(),
		})

	case AwaitContact:
		// Fallback: accept text phone number too
		cleaned := strings.ReplaceAll(strings.ReplaceAll(text, " ", ""), "-", "")
		if len(cleaned) >= 9 && len(cleaned) <= 12 {
			st.Phone = text
			st.Name = update.Message.From.FirstName
			if update.Message.From.LastName != "" {
				st.Name += " " + update.Message.From.LastName
			}
			st.Step = AwaitConfirm
			setState(chatID, st)

			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: chatID,
				Text: fmt.Sprintf(
					"📋 ትኬት ማረጋገጫ:\n\n"+
						"🎟 ትኬት: #%d\n"+
						"📱 ስልክ: %s\n"+
						"👤 ስም: %s\n"+
						"💰 ዋጋ: %d ብር\n\n"+
						"ይህን ትኬት መያዝ ይፈልጋሉ?",
					st.Ticket, st.Phone, st.Name, price,
				),
				ReplyMarkup: confirmKB(),
			})
			return
		}
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "⚠️ እባክዎ ስልክ ቁጥር ያጋሩ ወይም ትክክለኛ ስልክ ቁጥር ይፃፉ\n\n📱 ከታች «📱 ስልክ ቁጥር ላክ» ቁልፍ ይንኩ",
			ReplyMarkup: contactRequestKB(),
		})

	case AwaitReceipt:
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "📸 እባክዎ የክፍያ ደረሰኝ screenshot ይላኩ።",
		})
	}
}

// handleContact processes shared contact phone numbers
func contactHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	contact := update.Message.Contact
	if contact == nil {
		return
	}

	st := getState(chatID)
	if st == nil || st.Step != AwaitContact {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "📱 ስልክ ተቀብሏል፣ ነገር ግን ትኬት መያዝ አልተጀመረም።\n\n/book ብለው ይላኩ።",
		})
		return
	}

	// Use the shared contact's phone number
	phone := contact.PhoneNumber
	// Add + prefix if not present
	if !strings.HasPrefix(phone, "+") {
		phone = "+" + phone
	}

	// Use contact's name if available
	name := contact.FirstName
	if contact.LastName != "" {
		name += " " + contact.LastName
	}
	if name == "" {
		name = update.Message.From.FirstName
		if update.Message.From.LastName != "" {
			name += " " + update.Message.From.LastName
		}
	}

	st.Phone = phone
	st.Name = name
	st.Step = AwaitConfirm
	setState(chatID, st)

	// Show confirmation
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text: fmt.Sprintf(
			"📋 ትኬት ማረጋገጫ:\n\n"+
				"🎟 ትኬት: #%d\n"+
				"📱 ስልክ: %s\n"+
				"👤 ስም: %s\n"+
				"💰 ዋጋ: %d ብር\n\n"+
				"ይህን ትኬት መያዝ ይፈልጋሉ?",
			st.Ticket, st.Phone, st.Name, price,
		),
		ReplyMarkup: confirmKB(),
	})
}

func photoHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
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
		b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "⚠️ ፎቶውን ማስኬድ አልተቻለም። እንደገና ይሞክሩ።"})
		return
	}

	dlURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", botTokenStr, file.FilePath)
	resp, err := http.Get(dlURL)
	if err != nil {
		log.Printf("download photo failed: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "⚠️ ፎቶውን ማውረድ አልተቻለም። እንደገና ይሞክሩ።"})
		return
	}
	defer resp.Body.Close()

	// Save receipt locally
	receiptPath := filepath.Join(dataDir, "receipts", fmt.Sprintf("receipt_%d_%d.jpg", chatID, st.Ticket))
	outFile, err := os.Create(receiptPath)
	if err != nil {
		log.Printf("create receipt file failed: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "⚠️ ደረሰኝ ማስኬድ አልተቻለም።"})
		return
	}
	defer outFile.Close()
	io.Copy(outFile, resp.Body)

	// Try API upload
	bf := loadBookings()
	for i := range bf.Bookings {
		if bf.Bookings[i].UserID == chatID && bf.Bookings[i].TicketNum == st.Ticket && bf.Bookings[i].Status == "PENDING" {
			if bf.Bookings[i].PaymentID > 0 {
				if err := apiUploadReceipt(bf.Bookings[i].PaymentID, receiptPath); err != nil {
					log.Printf("API upload receipt failed: %v", err)
				}
			}
			bf.Bookings[i].Status = "PAID"
			bf.Bookings[i].ReceiptFile = receiptPath
			break
		}
	}
	saveBookings(bf)

	clearState(chatID)

	// Send receipt to admin
	f, err := os.Open(receiptPath)
	if err == nil {
		defer f.Close()
		for _, adminID := range adminIDs {
			b.SendPhoto(ctx, &bot.SendPhotoParams{
				ChatID:  adminID,
				Photo:   &models.InputFileUpload{Filename: "receipt.jpg", Data: f},
				Caption: fmt.Sprintf(
					"📸 የክፍያ ደረሰኝ!\n\n"+
						"🎟 ትኬት: #%d\n"+
						"📱 ስልክ: %s\n"+
						"👤 ስም: %s\n"+
						"🆔 ቴሌግራም: %d\n\n"+
						"✅ ክፍያ ተረጋግጧል!",
					st.Ticket, st.Phone, st.Name, chatID,
				),
			})
		}
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text: fmt.Sprintf(
			"🎉 ደረሰኝ ተስርፏል! ትኬት #%d ማረጋገጫ በመጠበቅ ላይ ነው።\n\n"+
				"✅ ከተረጋገጠ በኋላ ትኬትዎ ይቀመጣል።\n\n"+
				"ለማንኛውም ጥያቄ @afroequb",
			st.Ticket,
		),
		ReplyMarkup: mainMenu(),
	})
}

func notifyAdmin(ctx context.Context, b *bot.Bot, msg string) {
	for _, adminID := range adminIDs {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: adminID,
			Text:   msg,
		})
	}
}

// ==================== Main ====================

func main() {
	loadEnv(".env")

	botTokenStr = os.Getenv("TELEGRAM_BOT_TOKEN")
	if botTokenStr == "" {
		log.Fatal("❌ TELEGRAM_BOT_TOKEN is required")
	}

	if v := os.Getenv("MINIAPP_URL"); v != "" {
		miniAppURL = v
	}
	if v := os.Getenv("WEBAPP_API_BASE"); v != "" {
		apiBase = v
	}
	if v := os.Getenv("TELEGRAM_ADMIN_IDS"); v != "" {
		for _, s := range strings.Split(v, ",") {
			s = strings.TrimSpace(s)
			if id, err := strconv.ParseInt(s, 10, 64); err == nil {
				adminIDs = append(adminIDs, id)
			}
		}
	}

	ensureDataDir()
	log.Printf("Admin IDs: %v | API: %s", adminIDs, apiBase)

	// HTTP server
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	http.HandleFunc("/", pingHandler)
	go func() {
		log.Printf("HTTP on :%s", port)
		http.ListenAndServe(":"+port, nil)
	}()

	ctx := context.Background()

	b, err := bot.New(botTokenStr)
	if err != nil {
		log.Fatalf("Failed to create bot: %v", err)
	}

	me, err := b.GetMe(ctx)
	if err != nil {
		log.Fatalf("Failed to getMe: %v", err)
	}
	log.Printf("✅ Bot: @%s", me.Username)

	// Commands
	b.RegisterHandler(bot.HandlerTypeMessageText, "start", bot.MatchTypeCommand, startHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "book", bot.MatchTypeCommand, func(ctx context.Context, b *bot.Bot, update *models.Update) {
		bookHandler(ctx, b, update.Message.Chat.ID)
	})
	b.RegisterHandler(bot.HandlerTypeMessageText, "help", bot.MatchTypeCommand, func(ctx context.Context, b *bot.Bot, update *models.Update) {
		helpHandler(ctx, b, update.Message.Chat.ID)
	})

	// Callbacks
	b.RegisterHandlerMatchFunc(func(update *models.Update) bool {
		return update.CallbackQuery != nil
	}, callbackHandler)

	// Contact sharing
	b.RegisterHandlerMatchFunc(func(update *models.Update) bool {
		return update.Message != nil && update.Message.Contact != nil
	}, contactHandler)

	// Photos
	b.RegisterHandlerMatchFunc(func(update *models.Update) bool {
		return update.Message != nil && len(update.Message.Photo) > 0
	}, photoHandler)

	// Text (catch-all, last)
	b.RegisterHandler(bot.HandlerTypeMessageText, "", bot.MatchTypeContains, textHandler)

	b.SetMyCommands(ctx, &bot.SetMyCommandsParams{
		Commands: []models.BotCommand{
			{Command: "start", Description: "🚀 ቦቱን ይጀምሩ"},
			{Command: "book", Description: "🎟 ቲኬት ይያዙ"},
			{Command: "help", Description: "ℹ️ እርዳታ"},
		},
	})

	log.Printf("🚀 Bot running! Send /start to @%s", me.Username)
	b.Start(ctx)
}
