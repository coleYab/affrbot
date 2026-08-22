package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
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
	Status      string    `json:"status"` // PENDING, PAID, CONFIRMED
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
	Idle       Step = ""
	AwaitNum   Step = "await_num"
	AwaitPhone Step = "await_phone"
	AwaitConfirm Step = "await_confirm"
	AwaitReceipt Step = "await_receipt"
)

type UserState struct {
	Step    Step
	Ticket  int
	Phone   string
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
	botTokenStr string
)

// ==================== Callback data ====================

const (
	cbBook     = "BOOK"
	cbConfirm  = "CONFIRM"
	cbCancel   = "CANCEL"
	cbHelp     = "HELP"
	cbSupport  = "SUPPORT"
	cbStart    = "START"
	cbMyBook   = "MYBOOK"
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

func updateBookingStatus(id int, status string, receipt string) {
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

// ==================== Keyboards ====================

func mainMenu() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: "🎟 ቲኬት ይያዙ", CallbackData: cbBook}},
			{
				{Text: "📋 የእኔ ቲኬቶች", CallbackData: cbMyBook},
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

func confirmKB(ticket int) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{
				{Text: "✅ አዎ, ያስረክሩ", CallbackData: cbConfirm},
				{Text: "❌ ሰርዝ", CallbackData: cbCancel},
			},
		},
	}
}

// ==================== Admin notification ====================

func notifyAdmin(ctx context.Context, b *bot.Bot, msg string) {
	for _, adminID := range adminIDs {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: adminID,
			Text:   msg,
		})
	}
}

func notifyAdminPhoto(ctx context.Context, b *bot.Bot, photo *models.InputFileUpload, caption string) {
	for _, adminID := range adminIDs {
		b.SendPhoto(ctx, &bot.SendPhotoParams{
			ChatID:  adminID,
			Photo:   photo,
			Caption: caption,
		})
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
			"3️⃣ ስልክ ቁጥርዎን ይፃፉ\n" +
			"4️⃣ 500 ብር ወደ " + paymentAcct + " ያስረክቡ\n" +
			"5️⃣ ደረሰኝ screenshot ይላኩ\n\n" +
			"✅ ክፍያዎ ከተረጋገጠ ቲኬትዎ ይቀመጣል!\n\n" +
			"ለማንኛውም ጥያቄ @" + strings.TrimPrefix(supportURL, "https://t.me/"),
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
		Text:   "🎟 እባክዎ መряይ የሚሹትን ትኬት ቁጥር ይፃፉ\n\n📝 ለምሳሌ፡ 42",
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
			Text:        "📋 ምንም ቲኬት የለዎትም።\n\n🎟 ቲኬት ለመያዝ ከታች ቁልፍ ይንኩ!",
			ReplyMarkup: mainMenu(),
		})
		return
	}
	var sb strings.Builder
	sb.WriteString("📋 የእኔ ቲኬቶች:\n\n")
	for _, bk := range my {
		status := "⏳ ማረጋገጫ በመጠበቅ ላይ"
		if bk.Status == "CONFIRMED" {
			status = "✅ ተረጋግጧል"
		}
		sb.WriteString(fmt.Sprintf("🎟 #%d | %s | %s\n", bk.TicketNum, bk.UserPhone, status))
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
			ChatID: chatID, MessageID: msgID,
			Text: "🎟 እንደገና ይጀምሩ!", ReplyMarkup: mainMenu(),
		})
	case cbHelp:
		helpHandler(ctx, b, chatID)
	case cbSupport:
		supportHandler(ctx, b, chatID)
	case cbBook:
		clearState(chatID)
		setState(chatID, &UserState{Step: AwaitNum})
		b.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID: chatID, MessageID: msgID, Text: "🎟 እባክዎ መряይ የሚሹትን ትኬት ቁጥር ይፃፉ\n\n📝 ለምሳሌ፡ 42",
			ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
				{{Text: "❌ ሰርዝ", CallbackData: cbCancel}},
			}},
		})
	case cbCancel:
		clearState(chatID)
		b.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID: chatID, MessageID: msgID, Text: "✅ ተ취ልቷል።", ReplyMarkup: mainMenu(),
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

		// Create booking
		user := cb.From
		name := user.FirstName
		if user.LastName != "" {
			name += " " + user.LastName
		}
		bk := addBooking(Booking{
			UserID:    chatID,
			UserName:  name,
			UserPhone: st.Phone,
			TicketNum: st.Ticket,
			Status:    "PENDING",
		})

		// Update state to await receipt
		setState(chatID, &UserState{Step: AwaitReceipt, Ticket: st.Ticket, Phone: st.Phone})

		// Notify admin
		notifyAdmin(ctx, b, fmt.Sprintf(
			"🆕 አዲስ ትኬት ቀይቧል!\n\n"+
				"🎟 ትኬት: #%d\n"+
				"👤 ስም: %s\n"+
				"📱 ስልክ: %s\n"+
				"🆔 ቴሌግራም: %d\n"+
				"📋 ቦታ: %d\n\n"+
				"⏳ የክፍያ ደረሰኝ በመጠበቅ ላይ...",
			st.Ticket, name, st.Phone, chatID, bk.ID,
		))

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

		// Check if already taken
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

		// Ask for phone
		setState(chatID, &UserState{Step: AwaitPhone, Ticket: num})
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   fmt.Sprintf("📱 ትኬት #%d ተመርጧል!\n\nእባክዎ ስልክ ቁጥርዎን ይፃፉ\n\n📝 ለምሳሌ፡ 0911223344", num),
			ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
				{{Text: "❌ ሰርዝ", CallbackData: cbCancel}},
			}},
		})

	case AwaitPhone:
		// Basic phone validation
		cleaned := strings.ReplaceAll(strings.ReplaceAll(text, " ", ""), "-", "")
		if len(cleaned) < 9 || len(cleaned) > 12 {
			b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: chatID, Text: "⚠️ እባክዎ ትክክለኛ ስልክ ቁጥር ይፃፉ\n\n📝 ለምሳሌ፡ 0911223344",
			})
			return
		}

		st.Phone = text
		st.Step = AwaitConfirm
		setState(chatID, st)

		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text: fmt.Sprintf(
				"📋 ትኬት ማረጋገጫ:\n\n"+
					"🎟 ትኬት: #%d\n"+
					"📱 ስልክ: %s\n"+
					"💰 ዋጋ: %d ብር\n\n"+
					"ይህን ትኬት መያዝ ይፈልጋሉ?",
				st.Ticket, st.Phone, price,
			),
			ReplyMarkup: confirmKB(st.Ticket),
		})

	case AwaitReceipt:
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "📸 እባክዎ የክፍያ ደረሰኝ screenshot ይላኩ።",
		})
	}
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

	// Download photo from Telegram
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

	// Find and update booking
	bf := loadBookings()
	for i := range bf.Bookings {
		if bf.Bookings[i].UserID == chatID && bf.Bookings[i].TicketNum == st.Ticket && bf.Bookings[i].Status == "PENDING" {
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
					st.Ticket, st.Phone,
					func() string {
						u := update.Message.From
						n := u.FirstName
						if u.LastName != "" {
							n += " " + u.LastName
						}
						return n
					}(),
					chatID,
				),
			})
		}
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        fmt.Sprintf("🎉 ደረሰኝ ተስርፏል! ትኬት #%d ማረጋገጫ በመጠበቅ ላይ ነው።\n\n✅ ከተረጋገጠ በኋላ ትኬትዎ ይቀመጣል።\n\nለማንኛውም ጥያቄ @afroequb", st.Ticket),
		ReplyMarkup: mainMenu(),
	})
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
	if v := os.Getenv("TELEGRAM_ADMIN_IDS"); v != "" {
		for _, s := range strings.Split(v, ",") {
			s = strings.TrimSpace(s)
			if id, err := strconv.ParseInt(s, 10, 64); err == nil {
				adminIDs = append(adminIDs, id)
			}
		}
	}

	ensureDataDir()
	log.Printf("Admin IDs: %v", adminIDs)

	// HTTP keep-alive server
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
