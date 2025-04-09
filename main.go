package main

import (
	"encoding/json"
	"os"
	"strconv"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/oldtyt/frigate-telegram/internal/config"
	"github.com/oldtyt/frigate-telegram/internal/frigate"
	"github.com/oldtyt/frigate-telegram/internal/log"
)

// --- Persistent State Management ---

type ChatState struct {
	Enabled bool `json:"enabled"`
	Silent  bool `json:"silent"`
}

type StateManager struct {
	ChatStates map[int64]*ChatState `json:"chat_states"`
	mu         sync.Mutex
}

var stateFilePath = "bot_state.json"
var botState *StateManager

func LoadState() *StateManager {
	file, err := os.Open(stateFilePath)
	if err != nil {
		return &StateManager{ChatStates: make(map[int64]*ChatState)}
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	var sm StateManager
	if err := decoder.Decode(&sm); err != nil {
		return &StateManager{ChatStates: make(map[int64]*ChatState)}
	}
	return &sm
}

func (sm *StateManager) Save() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	file, err := os.Create(stateFilePath)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(sm)
}

func (sm *StateManager) Get(chatID int64) *ChatState {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	state, exists := sm.ChatStates[chatID]
	if !exists {
		state = &ChatState{Enabled: true, Silent: false}
		sm.ChatStates[chatID] = state
	}
	return state
}

// --- Telegram Bot Logic ---

func PongBot(bot *tgbotapi.BotAPI) {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message != nil && update.Message.IsCommand() {
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, "")
			state := botState.Get(update.Message.Chat.ID)

			switch update.Message.Command() {
			case "help":
				msg.Text = "Available commands:\n/start, /stop, /mute, /unmute, /ping, /status, /menu"
			case "start":
				state.Enabled = true
				msg.Text = "Notifications enabled."
				botState.Save()
			case "stop":
				state.Enabled = false
				msg.Text = "Notifications disabled."
				botState.Save()
			case "mute":
				state.Silent = true
				msg.Text = "Notifications are now silent."
				botState.Save()
			case "unmute":
				state.Silent = false
				msg.Text = "Silent mode disabled."
				botState.Save()
			case "ping":
				msg.Text = "pong"
			case "pong":
				msg.Text = "ping"
			case "status":
				msg.Text = "I'm ok."
			case "menu":
				msg.Text = "Select a command:"
				msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
					tgbotapi.NewInlineKeyboardRow(
						tgbotapi.NewInlineKeyboardButtonData("Ping", "cmd_ping"),
						tgbotapi.NewInlineKeyboardButtonData("Status", "cmd_status"),
					),
					tgbotapi.NewInlineKeyboardRow(
						tgbotapi.NewInlineKeyboardButtonData("Help", "cmd_help"),
					),
				)
			default:
				msg.Text = "I don't know that command."
			}

			if _, err := bot.Send(msg); err != nil {
				log.Error.Fatalln("Error sending message: " + err.Error())
			}
		}

		if update.CallbackQuery != nil {
			var reply string
			switch update.CallbackQuery.Data {
			case "cmd_ping":
				reply = "pong"
			case "cmd_status":
				reply = "I'm ok."
			case "cmd_help":
				reply = "Available commands: /ping, /status, /pong, /menu, /start, /stop, /mute, /unmute"
			default:
				reply = "Unknown command"
			}

			bot.Request(tgbotapi.NewCallback(update.CallbackQuery.ID, ""))
			msg := tgbotapi.NewMessage(update.CallbackQuery.Message.Chat.ID, reply)
			bot.Send(msg)
		}
	}
}

// --- Main Application Entry ---

func main() {
	log.LogFunc()
	conf := config.New()

	startupMsg := "Starting frigate-telegram.\n"
	startupMsg += "Frigate URL:  " + conf.FrigateURL + "\n"
	log.Info.Println(startupMsg)

	bot, err := tgbotapi.NewBotAPI(conf.TelegramBotToken)
	if err != nil {
		log.Error.Fatalln("Error initializing telegram bot: " + err.Error())
	}
	bot.Debug = conf.Debug
	log.Info.Println("Authorized on account " + bot.Self.UserName)

	botState = LoadState()

	_, errmsg := bot.Send(tgbotapi.NewMessage(conf.TelegramChatID, startupMsg))
	if errmsg != nil {
		log.Error.Println(errmsg.Error())
	}

	go PongBot(bot)

	FrigateEventsURL := conf.FrigateURL + "/api/events"

	if conf.SendTextEvent {
		go frigate.NotifyEvents(bot, FrigateEventsURL)
	}

	for {
		FrigateEvents := frigate.GetEvents(FrigateEventsURL, bot, true)

		// Only send if user enabled notifications
		if state := botState.Get(conf.TelegramChatID); state.Enabled {
			for _, event := range FrigateEvents {
				msg := tgbotapi.NewMessage(conf.TelegramChatID, event.ToMessage()) // Example
				msg.DisableNotification = state.Silent
				bot.Send(msg)
			}
		}

		time.Sleep(time.Duration(conf.SleepTime) * time.Second)
		log.Debug.Println("Sleeping for " + strconv.Itoa(conf.SleepTime) + " seconds.")
	}
}
