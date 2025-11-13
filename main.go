// go mod init ecobot
// go get modernc.org/sqlite
// go get github.com/go-telegram-bot-api/telegram-bot-api/v5
// go mod tidy
// go run main.go

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	OpenWeatherAPIKey = "60b31ecb5fa2a72f5c88ee986a4b2ab"
	TOKEN             = "8044735363:AAFdlteU4UoqyTez7nI6KxG7JvDLuSmgo4k"
	DB                = "ecobot.db"
	DataFile          = "user_data.json"
)

type User struct {
	ID                int64
	City              string
	LastCheck         time.Time
	WeeklyAQI         []float64
	CompletedMissions int
	Level             string
	AlertsEnabled     bool
	LastMission       string
	CurrentMission    string
}

var (
	ecoMissions = []string{
		"Відмовся від пластикової пляшки сьогодні",
		"Вимкни непотрібне світло в кімнаті",
		"Використовуй громадський транспорт",
		"Збери сміття на вулиці",
		"Посади квітку або дерево",
		"Відсортуй відходи",
		"З'їж вегетаріанський обід",
		"Принеси в магазин власну торбинку",
		"Використовуй багаторазову пляшку для води",
		"Провітри кімнату замість кондиціонера",
		"Не друкуй зайвих паперів сьогодні",
		"Піди на прогулянку до парку",
	}

	factsUkraine = []string{
		"Україна має понад 70 тисяч річок і струмків.",
		"У Карпатах росте понад 100 видів лікарських рослин.",
		"Заповідник Асканія-Нова має понад 200 видів птахів.",
		"Шацькі озера — це понад 30 озер з кришталево чистою водою.",
		"В Україні є понад 600 видів птахів.",
		"Біля Херсона росте пустеля — Олешківські піски.",
		"На півдні України можна зустріти фламінго.",
	}

	quizQuestions = []struct {
		Q       string
		A       []string
		Correct int
	}{
		{"Яка найвища гора в Україні?", []string{"Говерла", "Піп Іван", "Бребенескуль", "Свидовець"}, 0},
		{"Яке море омиває південь України?", []string{"Біле", "Чорне", "Балтійське", "Баренцове"}, 1},
		{"Яка річка найдовша в Україні?", []string{"Дніпро", "Південний Буг", "Дністер", "Сіверський Донець"}, 0},
	}

	users = make(map[int64]*User)
	db    *sql.DB
)

func main() {
	initDB()
	loadOldData()
	loadUsersFromDB()

	bot, err := tgbotapi.NewBotAPI(TOKEN)
	if err != nil {
		log.Fatal("ПОМИЛКА ТОКЕНУ")
	}

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	go runAlertChecker(bot)

	for update := range updates {
		if update.Message == nil && update.CallbackQuery == nil {
			continue
		}

		var userID int64
		var text string
		var msgID int

		if update.Message != nil {
			userID = update.Message.From.ID
			text = update.Message.Text
		} else {
			userID = update.CallbackQuery.From.ID
			text = update.CallbackQuery.Data
			msgID = update.CallbackQuery.Message.MessageID
		}

		if users[userID] == nil {
			users[userID] = &User{ID: userID, Level: "Спостерігач", AlertsEnabled: true}
			saveUser(userID)
		}

		if text == "/start" {
			if users[userID].City == "" {
				msg := tgbotapi.NewMessage(userID, "Привіт! Напиши місто латиницею (Cherkasy, Kyiv):")
				bot.Send(msg)
				continue
			} else {
				handleStart(bot, userID)
				continue
			}
		}

		if users[userID].City == "" && text != "/start" && text != "" {
			city := toLatin(strings.TrimSpace(text))
			users[userID].City = city
			saveUser(userID)
			handleCityUpdate(bot, userID, city)
			continue
		}

		if update.CallbackQuery != nil {
			handleQuizAnswer(bot, userID, text, msgID)
			continue
		}

		switch text {
		case "Еко-місія":
			handleMission(bot, userID)
		case "Факт про природу":
			handleFact(bot, userID)
		case "Еко-вікторина":
			handleQuiz(bot, userID)
		case "Мій профіль":
			handleProfile(bot, userID)
		case "/done":
			handleDone(bot, userID)
		case "/ecoalert":
			users[userID].AlertsEnabled = !users[userID].AlertsEnabled
			status := "увімкнено"
			if !users[userID].AlertsEnabled {
				status = "вимкнено"
			}
			msg := tgbotapi.NewMessage(userID, "Сповіщення "+status)
			bot.Send(msg)
		case "/report":
			handleReport(bot, userID)
		default:
			msg := tgbotapi.NewMessage(userID, "Обери з меню")
			msg.ReplyMarkup = getMainMenu()
			bot.Send(msg)
		}
	}
}

func handleCityUpdate(bot *tgbotapi.BotAPI, userID int64, city string) {
	weather, _ := getWeather(city)
	aqi, _ := getAQI(city)

	msgText := fmt.Sprintf("Температура: %.1f°C %s\nAQI: %d\nЕко-порада: полий рослину", weather.Temp, weather.Condition, aqi.AQI)
	msg := tgbotapi.NewMessage(userID, msgText)
	msg.ReplyMarkup = getMainMenu()
	bot.Send(msg)

	users[userID].WeeklyAQI = append(users[userID].WeeklyAQI, float64(aqi.AQI))
	users[userID].LastCheck = time.Now()
	saveUser(userID)
}

func getWeather(city string) (struct{ Temp float64; Condition string }, error) {
	url := fmt.Sprintf("https://api.openweathermap.org/data/2.5/weather?q=%s,UA&appid=%s&units=metric&lang=uk", city, OpenWeatherAPIKey)
	resp, err := http.Get(url)
	if err != nil {
		return struct{ Temp float64; Condition string }{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var data struct {
		Main struct {
			Temp float64 `json:"temp"`
		} `json:"main"`
		Weather []struct {
			Description string `json:"description"`
		} `json:"weather"`
	}
	json.Unmarshal(body, &data)
	cond := "невідомо"
	if len(data.Weather) > 0 {
		cond = data.Weather[0].Description
	}
	return struct{ Temp float64; Condition string }{data.Main.Temp, cond}, nil
}

func getAQI(city string) (struct{ AQI int }, error) {
	return struct{ AQI int }{50}, nil
}

func handleReport(bot *tgbotapi.BotAPI, userID int64) {
	user := users[userID]
	avg := 0.0
	if len(user.WeeklyAQI) > 0 {
		sum := 0.0
		for _, v := range user.WeeklyAQI {
			sum += v
		}
		avg = sum / float64(len(user.WeeklyAQI))
	}
	msg := tgbotapi.NewMessage(userID, fmt.Sprintf("Середній AQI: %.1f\nМісій: %d\nРівень: %s", avg, user.CompletedMissions, user.Level))
	msg.ReplyMarkup = getMainMenu()
	bot.Send(msg)
}

func runAlertChecker(bot *tgbotapi.BotAPI) {
	ticker := time.NewTicker(3 * time.Hour)
	for range ticker.C {
		for id, u := range users {
			if !u.AlertsEnabled || u.City == "" || time.Since(u.LastCheck) < 2*time.Hour {
				continue
			}
			aqi, _ := getAQI(u.City)
			if aqi.AQI > 100 {
				msg := tgbotapi.NewMessage(id, "Попередження: погана якість повітря!")
				bot.Send(msg)
			}
		}
	}
}

func handleStart(bot *tgbotapi.BotAPI, userID int64) {
	level := getLevel(users[userID].CompletedMissions)
	msg := tgbotapi.NewMessage(userID, fmt.Sprintf("Вітаю!\nРівень: %s\nМісій: %d", level, users[userID].CompletedMissions))
	msg.ReplyMarkup = getMainMenu()
	bot.Send(msg)
}

func handleMission(bot *tgbotapi.BotAPI, userID int64) {
	user := users[userID]
	today := time.Now().Format("2006-01-02")
	if user.LastMission == today {
		msg := tgbotapi.NewMessage(userID, "Ти вже виконав місію сьогодні")
		msg.ReplyMarkup = getMainMenu()
		bot.Send(msg)
		return
	}
	mission := ecoMissions[rand.Intn(len(ecoMissions))]
	user.CurrentMission = mission
	msg := tgbotapi.NewMessage(userID, "Місія: "+mission+"\nПиши /done")
	msg.ReplyMarkup = getMainMenu()
	bot.Send(msg)
	saveUser(userID)
}

func handleFact(bot *tgbotapi.BotAPI, userID int64) {
	fact := factsUkraine[rand.Intn(len(factsUkraine))]
	msg := tgbotapi.NewMessage(userID, "Факт: "+fact)
	msg.ReplyMarkup = getMainMenu()
	bot.Send(msg)
}

func handleQuiz(bot *tgbotapi.BotAPI, userID int64) {
	q := quizQuestions[rand.Intn(len(quizQuestions))]
	keyboard := tgbotapi.NewInlineKeyboardMarkup()
	for i, a := range q.A {
		keyboard.InlineKeyboard = append(keyboard.InlineKeyboard, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(a, strconv.Itoa(i)+":"+strconv.Itoa(q.Correct)),
		))
	}
	msg := tgbotapi.NewMessage(userID, q.Q)
	msg.ReplyMarkup = keyboard
	bot.Send(msg)
}

func handleQuizAnswer(bot *tgbotapi.BotAPI, userID int64, data string, msgID int) {
	parts := strings.Split(data, ":")
	if len(parts) != 2 {
		return
	}
	answer, _ := strconv.Atoi(parts[0])
	correct, _ := strconv.Atoi(parts[1])
	text := "Неправильно"
	if answer == correct {
		text = "Правильно!"
		users[userID].CompletedMissions++
		updateLevel(userID)
		saveUser(userID)
	}
	edit := tgbotapi.NewEditMessageText(userID, msgID, text)
	bot.Send(edit)
}

func handleProfile(bot *tgbotapi.BotAPI, userID int64) {
	user := users[userID]
	msg := tgbotapi.NewMessage(userID, fmt.Sprintf("Місій: %d\nРівень: %s", user.CompletedMissions, user.Level))
	msg.ReplyMarkup = getMainMenu()
	bot.Send(msg)
}

func handleDone(bot *tgbotapi.BotAPI, userID int64) {
	user := users[userID]
	if user.CurrentMission == "" {
		msg := tgbotapi.NewMessage(userID, "Спочатку отримай місію")
		msg.ReplyMarkup = getMainMenu()
		bot.Send(msg)
		return
	}
	user.CompletedMissions++
	user.LastMission = time.Now().Format("2006-01-02")
	user.CurrentMission = ""
	updateLevel(userID)
	saveUser(userID)
	msg := tgbotapi.NewMessage(userID, "Місія виконана")
	msg.ReplyMarkup = getMainMenu()
	bot.Send(msg)
}

func getMainMenu() tgbotapi.ReplyKeyboardMarkup {
	return tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("Еко-місія"),
			tgbotapi.NewKeyboardButton("Факт про природу"),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("Еко-вікторина"),
			tgbotapi.NewKeyboardButton("Мій профіль"),
		),
	)
}

func getLevel(m int) string {
	if m < 5 {
		return "Спостерігач"
	} else if m < 15 {
		return "Захисник"
	}
	return "Амбасадор"
}

func updateLevel(id int64) {
	users[id].Level = getLevel(users[id].CompletedMissions)
}

func initDB() {
	var err error
	db, err = sql.Open("sqlite", DB)
	if err != nil {
		log.Fatal(err)
	}
	db.Exec(`CREATE TABLE IF NOT EXISTS users (
		user_id INTEGER PRIMARY KEY,
		city TEXT, last_check TEXT, weekly_aqi TEXT,
		completed_missions INTEGER, level TEXT, alerts_enabled INTEGER,
		last_mission TEXT, current_mission TEXT
	)`)
}

func loadOldData() {
	if data, err := os.ReadFile(DataFile); err == nil {
		var oldUsers map[int64]*User
		json.Unmarshal(data, &oldUsers)
		for id, u := range oldUsers {
			users[id] = u
		}
	}
}

func loadUsersFromDB() {
	rows, err := db.Query("SELECT user_id, city, last_check, weekly_aqi, completed_missions, level, alerts_enabled, last_mission, current_mission FROM users")
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var u User
		var aqiStr, lastCheck string
		var alerts int
		rows.Scan(&u.ID, &u.City, &lastCheck, &aqiStr, &u.CompletedMissions, &u.Level, &alerts, &u.LastMission, &u.CurrentMission)
		u.AlertsEnabled = alerts == 1
		if aqiStr != "" {
			json.Unmarshal([]byte(aqiStr), &u.WeeklyAQI)
		}
		if lastCheck != "" {
			u.LastCheck, _ = time.Parse("2006-01-02T15:04:05Z", lastCheck)
		}
		users[u.ID] = &u
	}
}

func saveUser(id int64) {
	u := users[id]
	aqiJSON, _ := json.Marshal(u.WeeklyAQI)
	alerts := 0
	if u.AlertsEnabled {
		alerts = 1
	}
	db.Exec(`INSERT OR REPLACE INTO users VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, u.City, u.LastCheck.Format("2006-01-02T15:04:05Z"), string(aqiJSON),
		u.CompletedMissions, u.Level, alerts, u.LastMission, u.CurrentMission)
}

func toLatin(s string) string {
	replace := map[rune]string{
		'а': "a", 'б': "b", 'в': "v", 'г': "h", 'ґ': "g", 'д': "d", 'е': "e", 'є': "ye",
		'ж': "zh", 'з': "z", 'и': "y", 'і': "i", 'ї': "yi", 'й': "y", 'к': "k", 'л': "l",
		'м': "m", 'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
		'ф': "f", 'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "shch", 'ь': "", 'ю': "yu", 'я': "ya",
	}
	res := ""
	for _, r := range strings.ToLower(s) {
		if lat, ok := replace[r]; ok {
			res += lat
		} else {
			res += string(r)
		}
	}
	return strings.ReplaceAll(res, "  ", " ")
}