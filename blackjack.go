// Package main реализует консольную игру в блэкджек с сохранением/загрузкой,
// логированием и настраиваемыми правилами (число колод, карта среза,
// разрешённые суммы для удвоения и т.д.).
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// -------------------- Константы --------------------

const (
	maxScore             = 21
	blackjackPayoutMul   = 3
	blackjackPayoutDenom = 2 // 3:2
	insurancePayoutMul   = 2 // 2:1
	payoutReturnFactor   = 2 // возврат ставки + выигрыш 1:1
	surrenderRefundDiv   = 2 // деление ставки пополам при сдаче
	insuranceBetDiv      = 2 // страховка = половина ставки
	defaultSaveFileMode  = 0600
	defaultLogFile       = "blackjack.log"
	defaultSaveFile      = ".blackjack_save.json"

	defaultNumDecks      = 4
	defaultCutDivisor    = 3
	defaultInitialPlayer = 1000
	defaultInitialDealer = 5000
	defaultMaxSplits     = 3
	dealerSleepMs        = 800

	// Действия игрока.
	actionHit       = "h"
	actionStand     = "s"
	actionDouble    = "d"
	actionSplit     = "p"
	actionSurrender = "r"
)

var (
	ErrInvalidBalance = errors.New("баланс игрока нулевой или отрицательный")
)

// -------------------- Конфигурация --------------------

type config struct {
	numDecks              int
	cutDivisor            int
	initialPlayer         int
	initialDealer         int
	doubleDownScores      []int
	dealerHitsSoft17      bool
	allowDoubleAfterSplit bool
	maxSplits             int // 0 = без ограничений
}

func defaultConfig() config {
	return config{
		numDecks:              defaultNumDecks,
		cutDivisor:            defaultCutDivisor,
		initialPlayer:         defaultInitialPlayer,
		initialDealer:         defaultInitialDealer,
		doubleDownScores:      []int{9, 10, 11},
		dealerHitsSoft17:      true,
		allowDoubleAfterSplit: true,
		maxSplits:             defaultMaxSplits,
	}
}

// -------------------- Карты и руки --------------------

type card struct {
	Suit  string
	Value string
	Rank  int
}

type hand struct {
	Cards      []card
	Bet        int
	IsDone     bool
	IsSplitAce bool
	IsSplit    bool
}

// Score возвращает очки руки (тузы считаются по 11, если не превышает 21).
func (h *hand) Score() int {
	score := 0
	aces := 0

	for _, c := range h.Cards {
		score += c.Rank

		if c.Value == "A" {
			aces++
		}
	}

	for score > maxScore && aces > 0 {
		score -= 10
		aces--
	}

	return score
}

// IsSoft возвращает true, если рука мягкая (есть туз, считающийся как 11).
func (h *hand) IsSoft() bool {
	minScore := 0
	hasAce := false

	for _, c := range h.Cards {
		if c.Value == "A" {
			minScore++
			hasAce = true
		} else {
			minScore += c.Rank
		}
	}

	return hasAce && minScore+10 <= maxScore
}

// IsBlackjack возвращает true, если в руке ровно две карты и сумма очков равна 21,
// и рука не является результатом сплита тузов (сплит тузов не даёт блэкджека).
func (h *hand) IsBlackjack() bool {
	return !h.IsSplitAce && len(h.Cards) == 2 && h.Score() == maxScore
}

// CanSplit проверяет, можно ли разделить руку.
func (h *hand) CanSplit() bool {
	return len(h.Cards) == 2 && h.Cards[0].Rank == h.Cards[1].Rank && !h.IsSplitAce
}

// String возвращает строковое представление руки (например, "A♠ K♥").
func (h *hand) String() string {
	parts := make([]string, 0, len(h.Cards))
	for _, c := range h.Cards {
		parts = append(parts, c.Value+c.Suit)
	}

	return strings.Join(parts, " ")
}

// -------------------- ASCII-рендеринг карт --------------------

const resetColor = "\033[0m"

func cardColor(c card) string {
	switch c.Suit {
	case "♥", "♦":
		return "\033[31m"
	default:
		return "\033[37m"
	}
}

func backColor() string {
	return "\033[90m"
}

func getCardLines(c card) []string {
	color := cardColor(c)
	top := color + "┌───┐" + resetColor
	middle1 := fmt.Sprintf(color+"│%3s│"+resetColor, c.Value)
	middle2 := fmt.Sprintf(color+"│%3s│"+resetColor, c.Suit)
	bottom := color + "└───┘" + resetColor

	return []string{top, middle1, middle2, bottom}
}

func getBackLines() []string {
	c := backColor()
	top := c + "┌───┐" + resetColor
	middle1 := c + "│░░░│" + resetColor
	middle2 := c + "│░░░│" + resetColor
	bottom := c + "└───┘" + resetColor

	return []string{top, middle1, middle2, bottom}
}

// Print выводит руку в консоль. Если hideSecond == true, вторая и последующие карты скрыты.
//
//nolint:forbidigo // прямые вызовы fmt для рендеринга, не зависят от игрового ввода-вывода
func (h *hand) Print(hideSecond bool) {
	if len(h.Cards) == 0 {
		fmt.Println("[Пусто]")

		return
	}

	var allLines [4]string

	for cardIdx, c := range h.Cards {
		var lines []string
		if hideSecond && cardIdx >= 1 {
			lines = getBackLines()
		} else {
			lines = getCardLines(c)
		}

		for j := range 4 {
			if cardIdx > 0 {
				allLines[j] += " "
			}

			allLines[j] += lines[j]
		}
	}

	for _, line := range allLines {
		fmt.Println(line)
	}

	if hideSecond && len(h.Cards) > 1 {
		fmt.Print("(очки: ?)")
	} else {
		fmt.Printf("(очки: %d)", h.Score())
	}

	fmt.Println()
}

// -------------------- Колода (Shoe) --------------------

type shoe struct {
	Cards []card
	total int // исходное количество карт для расчёта карты среза
}

// newShoe создаёт новую колоду (обувь) из numDecks колод и перемешивает.
func newShoe(numDecks int) *shoe {
	suits := []string{"♠", "♥", "♦", "♣"}
	values := []struct {
		val  string
		rank int
	}{
		{"2", 2}, {"3", 3}, {"4", 4}, {"5", 5}, {"6", 6},
		{"7", 7}, {"8", 8}, {"9", 9}, {"10", 10},
		{"J", 10}, {"Q", 10}, {"K", 10}, {"A", 11},
	}

	cards := make([]card, 0, numDecks*len(suits)*len(values))

	for range numDecks {
		for _, suit := range suits {
			for _, v := range values {
				cards = append(cards, card{Suit: suit, Value: v.val, Rank: v.rank})
			}
		}
	}
	// Используем math/rand/v2, криптостойкость не требуется для игры.
	//nolint:gosec // G404: игровой генератор, не для криптографии
	rand.Shuffle(len(cards), func(i, j int) { cards[i], cards[j] = cards[j], cards[i] })

	return &shoe{Cards: cards, total: len(cards)}
}

// NeedsCut проверяет, нужно ли перетасовать колоду по карте среза.
func (s *shoe) NeedsCut(cutDivisor int) bool {
	if cutDivisor <= 0 {
		return false
	}

	return s.total > 0 && len(s.Cards) <= s.total/cutDivisor
}

// Draw извлекает верхнюю карту; если колода пуста, создаёт новую.
func (s *shoe) Draw(numDecks int) card {
	if len(s.Cards) == 0 {
		*s = *newShoe(numDecks)
	}

	c := s.Cards[0]
	s.Cards = s.Cards[1:]

	return c
}

// -------------------- Логгер (интерфейс и реализации) --------------------

type Logger interface {
	Printf(format string, v ...any)
}

// fileLogger пишет логи в файл.
type fileLogger struct {
	file *os.File
	log  *log.Logger
}

// newFileLogger создаёт логгер, пишущий в указанный файл.
// Если файл не удаётся создать, возвращает nil, false.
//
//nolint:ireturn // возврат интерфейса допустим для фабрики
func newFileLogger(filename string) (Logger, error) {
	// Имя файла фиксировано (const), поэтому безопасно.
	//nolint:gosec // G304: путь не из пользовательского ввода
	file, err := os.Create(filename)
	if err != nil {
		return nil, fmt.Errorf("создание лог-файла: %w", err)
	}

	return &fileLogger{
		file: file,
		log:  log.New(file, "", log.LstdFlags),
	}, nil
}

func (l *fileLogger) Printf(format string, v ...any) {
	if l != nil && l.log != nil {
		l.log.Printf(format, v...)
	}
}

// Close закрывает файл лога.
func (l *fileLogger) Close() error {
	if l != nil && l.file != nil {
		return l.file.Close() //nolint:wrapcheck // обёртывание не требуется
	}

	return nil
}

// noopLogger ничего не делает.
type noopLogger struct{}

// Printf игнорирует все аргументы (заглушка).
func (noopLogger) Printf(_ string, _ ...any) {}

// -------------------- Сохранение/загрузка --------------------

type saveData struct {
	PlayerBalance int `json:"playerBalance"`
	DealerBalance int `json:"dealerBalance"`
}

func savePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}

	return filepath.Join(home, defaultSaveFile)
}

func saveGame(player, dealer int) error {
	data := saveData{PlayerBalance: player, DealerBalance: dealer}

	bytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("маршалинг сохранения: %w", err)
	}

	err = os.WriteFile(savePath(), bytes, defaultSaveFileMode)
	if err != nil {
		return fmt.Errorf("запись файла сохранения: %w", err)
	}

	return nil
}

func loadGame() (int, int, error) {
	bytes, err := os.ReadFile(savePath())
	if err != nil {
		return 0, 0, fmt.Errorf("чтение файла сохранения: %w", err)
	}

	var data saveData

	err = json.Unmarshal(bytes, &data)
	if err != nil {
		return 0, 0, fmt.Errorf("разбор сохранения: %w", err)
	}

	if data.PlayerBalance <= 0 || data.DealerBalance <= 0 {
		return 0, 0, ErrInvalidBalance
	}

	return data.PlayerBalance, data.DealerBalance, nil
}

func resetSave() error {
	path := savePath()

	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("удаление файла сохранения: %w", err)
	}

	return nil
}

// -------------------- Интерфейс ввода/вывода --------------------

type ioInterface interface {
	ReadString(prompt string) (string, error)
	Printf(format string, args ...any)
	Println(args ...any)
	Print(args ...any)
}

type consoleIO struct {
	reader *bufio.Reader
}

func newConsoleIO() *consoleIO {
	return &consoleIO{reader: bufio.NewReader(os.Stdin)}
}

func (c *consoleIO) ReadString(prompt string) (string, error) {
	fmt.Print(prompt) //nolint:forbidigo // прямой вывод приглашения

	line, err := c.reader.ReadString('\n')
	if err != nil {
		return "", err //nolint:wrapcheck // ошибка не обёрнута, достаточно
	}

	return strings.TrimSpace(line), nil
}

func (c *consoleIO) Printf(format string, args ...any) {
	fmt.Printf(format, args...) //nolint:forbidigo
}

func (c *consoleIO) Println(args ...any) {
	fmt.Println(args...) //nolint:forbidigo
}

func (c *consoleIO) Print(args ...any) {
	fmt.Print(args...) //nolint:forbidigo
}

// -------------------- Основная игра --------------------

type game struct {
	cfg     config
	player  int
	dealer  int
	shoe    *shoe
	console ioInterface
	logger  Logger
}

func newGame(cfg config, logger Logger, console ioInterface) *game {
	return &game{
		cfg:     cfg,
		player:  cfg.initialPlayer,
		dealer:  cfg.initialDealer,
		shoe:    newShoe(cfg.numDecks),
		console: console,
		logger:  logger,
	}
}

func loadOrInitGame(cfg config, logger Logger, console ioInterface) *game {
	player, dealer, err := loadGame()

	gameInstance := newGame(cfg, logger, console)

	if err != nil {
		console.Printf("🔄 Начинаем новую игру.\n")

		if cfg.initialPlayer != defaultInitialPlayer || cfg.initialDealer != defaultInitialDealer {
			console.Printf("   Баланс игрока: $%d\n", cfg.initialPlayer)
			console.Printf("   Баланс казино: $%d\n", cfg.initialDealer)
		}

		gameInstance.player = cfg.initialPlayer
		gameInstance.dealer = cfg.initialDealer
		logger.Printf("=== НОВАЯ ИГРА === Начальный баланс: игрок $%d, казино $%d", gameInstance.player, gameInstance.dealer)
	} else {
		// Если загруженный баланс некорректен (<=0) – сбрасываем и начинаем новую
		if player <= 0 || dealer <= 0 {
			_ = resetSave()

			console.Printf("🔄 Сохранение повреждено (баланс <=0), начинаем новую игру.\n")

			gameInstance.player = cfg.initialPlayer
			gameInstance.dealer = cfg.initialDealer
			logger.Printf(
				"=== НОВАЯ ИГРА (сброс повреждённого сохранения) === игрок $%d, казино $%d",
				gameInstance.player, gameInstance.dealer,
			)
		} else {
			console.Printf("📂 Загружено сохранение:\n   Ваш баланс: $%d\n   Баланс казино: $%d\n", player, dealer)
			gameInstance.player = player
			gameInstance.dealer = dealer
			logger.Printf("=== ЗАГРУЖЕНА ИГРА === игрок $%d, казино $%d", gameInstance.player, gameInstance.dealer)
		}
	}

	return gameInstance
}

func (g *game) run() {
	g.console.Println("=== ДОБРО ПОЖАЛОВАТЬ В CASINO BLACKJACK ===")

	for g.player > 0 && g.dealer > 0 {
		g.console.Printf("\n💰 Баланс Казино: $%d | 💵 Ваш Баланс: $%d\n", g.dealer, g.player)

		if !g.playRound() {
			break
		}

		if g.player > 0 {
			err := saveGame(g.player, g.dealer)
			if err != nil {
				g.console.Printf("⚠️ Не удалось сохранить прогресс: %v\n", err)
				g.logger.Printf("Ошибка сохранения: %v", err)
			}
		}
	}

	if g.player <= 0 {
		_ = resetSave()
	}

	g.console.Println("\n=== ИГРА ОКОНЧЕНА ===")

	switch {
	case g.player <= 0:
		g.console.Println("🛑 Вы обанкротились! Казино забирает всё.")
	case g.dealer <= 0:
		g.console.Println("🏆 Поздравляем! Вы разорили казино!")
	default:
		g.console.Printf("Вы вышли из игры. Итоговый капитал: $%d\n", g.player)
	}

	g.logger.Printf("=== ИГРА ЗАВЕРШЕНА === Итог: игрок $%d, казино $%d", g.player, g.dealer)
}

// playRound выполняет один игровой раунд:
//  1. Чтение ставки.
//  2. Раздача карт игроку и дилеру.
//  3. Предложение even money (если у игрока блэкджек и у дилера туз).
//  4. Предложение страховки (если у дилера туз).
//  5. Проверка на блэкджек у обоих.
//  6. Ход игрока (включая сплиты).
//  7. Ход дилера.
//  8. Подведение итогов по всем рукам.
//
// Возвращает false, если игрок завершил игру (ввод 'q').
//
//nolint:cyclop,funlen // сложность и длина оправданы, разбивать дальше нецелесообразно
func (g *game) playRound() bool {
	// Проверка, что казино ещё не разорено
	if g.dealer <= 0 {
		g.console.Println("🏆 Казино разорено! Игра окончена.")

		return false
	}

	bet, ok := g.readBet()
	if !ok {
		return false
	}

	g.player -= bet
	g.dealer += bet
	g.logger.Printf("=== НОВЫЙ РАУНД === Ставка: $%d", bet)

	g.reshuffleIfNeeded()

	playerHands := []hand{{
		Bet:        bet,
		Cards:      []card{g.shoe.Draw(g.cfg.numDecks), g.shoe.Draw(g.cfg.numDecks)},
		IsDone:     false,
		IsSplitAce: false,
		IsSplit:    false,
	}}
	dealerHand := hand{
		Cards:      []card{g.shoe.Draw(g.cfg.numDecks), g.shoe.Draw(g.cfg.numDecks)},
		Bet:        0,
		IsDone:     false,
		IsSplitAce: false,
		IsSplit:    false,
	}

	g.logger.Printf("Игрок: %s", playerHands[0].String())
	g.logger.Printf("Дилер: %s + [скрыто]", dealerHand.Cards[0].Value+dealerHand.Cards[0].Suit)

	// 1. Even money (если у игрока блэкджек и у дилера туз)
	if playerHands[0].IsBlackjack() && dealerHand.Cards[0].Value == "A" {
		if g.handleEvenMoney(bet) {
			return true
		}
	}

	// 2. Страховка (если у дилера туз)
	insuranceTaken, insuranceBet := false, 0
	if dealerHand.Cards[0].Value == "A" {
		insuranceTaken, insuranceBet = g.offerInsurance(bet)
	}

	// 3. Проверка блэкджека
	if g.resolveBlackjacks(&playerHands[0], &dealerHand, insuranceTaken, insuranceBet) {
		// Если был блэкджек, раунд завершён
		return true
	}

	// Если страховка была, но блэкджека не было – сообщаем
	if insuranceTaken {
		g.console.Println("❌ Страховка не сыграла.")
	}

	// 4. Ход игрока
	surrendered := g.processPlayerHands(&playerHands, &dealerHand)
	if surrendered {
		refund := bet / surrenderRefundDiv
		g.player += refund
		g.dealer -= refund
		g.logger.Printf("Игрок сдался, возвращено $%d", refund)
		g.console.Printf("\n🏳️ Вы сдались. Возвращено $%d, потеряно $%d\n", refund, bet-refund)

		return true
	}

	// 5. Ход дилера
	if !g.allBusted(playerHands) {
		g.dealerTurn(&dealerHand)
	}

	// 6. Подведение итогов
	g.resolveAllHands(playerHands, dealerHand)

	return true
}

// вспомогательные методы, разбивающие логику playRound

func (g *game) reshuffleIfNeeded() {
	if g.shoe.NeedsCut(g.cfg.cutDivisor) {
		g.console.Println("🃏 Карта среза: перетасовка новой обуви.")
		g.shoe = newShoe(g.cfg.numDecks)
		g.logger.Printf("Перетасовка по карте среза (<= 1/%d)", g.cfg.cutDivisor)
	}
}

// handleEvenMoney предлагает even money и возвращает true, если игрок согласился.
// В этом случае раунд завершается.
func (g *game) handleEvenMoney(bet int) bool {
	g.console.Println("У вас Блэкджек! У дилера открыт ТУЗ.")

	ans, err := g.console.ReadString("Хотите получить выигрыш 1:1 (even money) сейчас? (y/n): ")
	if err != nil {
		g.logger.Printf("Ошибка ввода even money: %v", err)

		return false
	}

	if strings.ToLower(ans) == "y" {
		win := bet
		g.player += bet + win
		g.dealer -= bet + win
		g.logger.Printf("Even money принято, выигрыш $%d", win)
		g.console.Printf("✅ Вы получили $%d (1:1). Раунд завершён.\n", win)

		return true
	}

	return false
}

// offerInsurance предлагает страховку, возвращает (принята ли, размер ставки).
func (g *game) offerInsurance(bet int) (bool, int) {
	insuranceBet := bet / insuranceBetDiv
	if insuranceBet == 0 || insuranceBet > g.player {
		return false, 0
	}

	ans, err := g.console.ReadString("🛡️ У дилера открыт ТУЗ. Хотите застраховаться? (y/n): ")
	if err != nil {
		g.logger.Printf("Ошибка ввода страховки: %v", err)

		return false, 0
	}

	if strings.ToLower(ans) == "y" {
		g.player -= insuranceBet
		g.dealer += insuranceBet
		g.logger.Printf("Страховка принята: игрок заплатил $%d", insuranceBet)
		g.console.Printf("✅ Страховка принята ($%d).\n", insuranceBet)

		return true, insuranceBet
	}

	return false, 0
}

// resolveBlackjacks обрабатывает ситуации с блэкджеком у игрока и/или дилера.
// Возвращает true, если блэкджек был у кого-то (раунд завершён).
func (g *game) resolveBlackjacks(playerHand *hand, dealerHand *hand, insuranceTaken bool, insuranceBet int) bool {
	if !playerHand.IsBlackjack() && !dealerHand.IsBlackjack() {
		return false
	}

	g.showInitialHands(*playerHand, *dealerHand)

	if dealerHand.IsBlackjack() && playerHand.IsBlackjack() {
		// Ничья: ставка возвращается
		g.player += playerHand.Bet
		g.dealer -= playerHand.Bet

		if insuranceTaken {
			win := insuranceBet * insurancePayoutMul
			g.player += win
			g.dealer -= win
			g.logger.Printf("Оба блэкджек, страховка выиграла +$%d", win)
			g.console.Printf("🛡️ Страховка выиграла! +$%d\n", win)
		}

		g.logger.Printf("Ничья (оба блэкджек), ставка возвращена")
		g.console.Println("🤝 У обоих Блэкджек – ничья (ставка возвращена).")

		return true
	}

	if dealerHand.IsBlackjack() {
		if insuranceTaken {
			win := insuranceBet * insurancePayoutMul
			g.player += win
			g.dealer -= win
			g.logger.Printf("Дилер блэкджек, страховка выиграла +$%d", win)
			g.console.Printf("🛡️ Страховка выиграла! +$%d (основная ставка проиграна).\n", win)
		} else {
			g.logger.Printf("Дилер блэкджек, игрок проиграл ставку")
			g.console.Println("💀 У дилера Блэкджек. Вы проиграли.")
		}

		return true
	}

	if playerHand.IsBlackjack() {
		win := playerHand.Bet * blackjackPayoutMul / blackjackPayoutDenom // 3:2
		g.player += playerHand.Bet + win
		g.dealer -= playerHand.Bet + win
		g.logger.Printf("Игрок блэкджек, выигрыш $%d (3:2)", win)
		g.console.Printf("🎉 Натуральный Блэкджек! Выигрыш 3:2: +$%d\n", win)

		return true
	}

	return false
}

func (g *game) showInitialHands(playerHand, dealerHand hand) {
	g.console.Println("\n--- Вскрытие стартовых карт ---")
	g.console.Println("Ваша рука: ")
	playerHand.Print(false)
	g.console.Println("Рука дилера: ")
	dealerHand.Print(false)
}

// -------- остальные методы (с улучшениями) --------

func (g *game) readBet() (int, bool) {
	for {
		input, err := g.console.ReadString("Введите ставку (или 'q' для выхода): ")
		if err != nil {
			g.console.Println("⚠️ Ошибка чтения ввода. Попробуйте ещё раз.")
			g.logger.Printf("Ошибка чтения ставки: %v", err)

			continue
		}

		if strings.ToLower(input) == "q" {
			return 0, false
		}

		bet, err := strconv.Atoi(input)
		if err == nil && bet > 0 && bet <= g.player && bet <= g.dealer {
			return bet, true
		}

		g.console.Println("❌ Некорректная ставка или недостаточно средств!")
	}
}

func (g *game) processPlayerHands(hands *[]hand, dealerHand *hand) bool {
	surrendered := false
	idx := 0

	for idx < len(*hands) {
		currentHand := &(*hands)[idx]
		if currentHand.IsDone {
			idx++

			continue
		}

		g.showGameState(*hands, *dealerHand, idx)

		if currentHand.Score() >= maxScore || currentHand.IsSplitAce {
			currentHand.IsDone = true
			idx++

			continue
		}

		action := g.getPlayerAction(*currentHand, len(*hands))

		oldLen := len(*hands)

		if g.executeAction(currentHand, hands, action) {
			surrendered = true

			break
		}

		if len(*hands) > oldLen {
			continue
		}

		if currentHand.IsDone {
			idx++
		}
	}

	return surrendered
}

func (g *game) showGameState(hands []hand, dealerHand hand, idx int) {
	g.console.Println("\n--- Рука дилера: ")
	dealerHand.Print(true)

	if len(hands) > 1 {
		g.console.Printf("--- Ваша рука #%d: \n", idx+1)
	} else {
		g.console.Println("--- Ваша рука: ")
	}

	hands[idx].Print(false)
}

// getPlayerAction формирует строку опций и запрашивает ввод.
func (g *game) getPlayerAction(handVal hand, totalHands int) string {
	options := g.buildActionOptions(handVal, totalHands)

	for {
		input, err := g.console.ReadString(fmt.Sprintf("Выберите действие %s: ", options))
		if err != nil {
			g.console.Println("⚠️ Ошибка ввода, попробуйте снова.")
			g.logger.Printf("Ошибка чтения действия: %v", err)

			continue
		}

		return input
	}
}

// buildActionOptions выделена для снижения цикломатической сложности.
func (g *game) buildActionOptions(handVal hand, totalHands int) string {
	options := "[h] Взять, [s] Остановиться"

	if g.canDouble(handVal) {
		options += ", [d] Удвоить"
	}

	if g.canSplit(handVal, totalHands) {
		options += ", [p] Разделить"
	}

	if len(handVal.Cards) == 2 && totalHands == 1 {
		options += ", [r] Сдаться"
	}

	return options
}

// canDouble проверяет, можно ли удвоить ставку.
// Учитывает настройку allowDoubleAfterSplit.
func (g *game) canDouble(handVal hand) bool {
	if len(handVal.Cards) != 2 || g.player < handVal.Bet || g.dealer < handVal.Bet {
		return false
	}

	if handVal.IsSplit && !g.cfg.allowDoubleAfterSplit {
		return false
	}

	score := handVal.Score()

	return slices.Contains(g.cfg.doubleDownScores, score)
}

func (g *game) canSplit(handVal hand, totalHands int) bool {
	if !handVal.CanSplit() || g.player < handVal.Bet || g.dealer < handVal.Bet {
		return false
	}

	if g.cfg.maxSplits > 0 && totalHands >= g.cfg.maxSplits {
		return false
	}

	return true
}

// executeAction обрабатывает действия игрока.
func (g *game) executeAction(handPtr *hand, hands *[]hand, action string) bool {
	action = strings.ToLower(action)

	switch action {
	case actionHit, "hit", "взять":
		g.doHit(handPtr)
	case actionStand, "stand", "остановиться":
		g.doStand(handPtr)
	case actionDouble, "double", "удвоить":
		g.doDouble(handPtr)
	case actionSplit, "split", "разделить":
		g.doSplit(handPtr, hands)
	case actionSurrender, "surrender", "сдаться":
		if len(handPtr.Cards) == 2 && len(*hands) == 1 {
			return true
		}

		g.console.Println("❌ Сдаться сейчас нельзя.")
	default:
		g.console.Println("❌ Неизвестная команда.")
	}

	return false
}

func (g *game) doHit(handPtr *hand) {
	c := g.shoe.Draw(g.cfg.numDecks)
	handPtr.Cards = append(handPtr.Cards, c)
	g.logger.Printf("Игрок взял: %s%s", c.Value, c.Suit)

	if handPtr.Score() >= maxScore {
		handPtr.IsDone = true
	}
}

func (g *game) doStand(handPtr *hand) {
	g.logger.Printf("Игрок остановился")

	handPtr.IsDone = true
}

func (g *game) doDouble(handPtr *hand) {
	if len(handPtr.Cards) != 2 || g.player < handPtr.Bet || g.dealer < handPtr.Bet {
		g.console.Println("❌ Удвоить сейчас нельзя.")

		return
	}

	score := handPtr.Score()
	if !slices.Contains(g.cfg.doubleDownScores, score) {
		g.console.Println("❌ Удвоение разрешено только при суммах 9, 10 или 11 (или заданных в конфиге).")

		return
	}

	oldBet := handPtr.Bet
	g.player -= oldBet
	g.dealer += oldBet
	handPtr.Bet *= 2
	c := g.shoe.Draw(g.cfg.numDecks)
	handPtr.Cards = append(handPtr.Cards, c)
	g.logger.Printf("Удвоение, доплата $%d, карта %s%s", oldBet, c.Value, c.Suit)

	handPtr.IsDone = true

	g.console.Println("Карта после удвоения: ")
	handPtr.Print(false)
}

func (g *game) doSplit(handPtr *hand, hands *[]hand) {
	if !handPtr.CanSplit() || g.player < handPtr.Bet || g.dealer < handPtr.Bet {
		g.console.Println("❌ Сплит сейчас невозможен.")

		return
	}

	if g.cfg.maxSplits > 0 && len(*hands) >= g.cfg.maxSplits {
		g.console.Println("❌ Достигнут лимит сплитов.")

		return
	}

	g.player -= handPtr.Bet
	g.dealer += handPtr.Bet

	newHand := hand{
		Cards:      []card{handPtr.Cards[1]},
		Bet:        handPtr.Bet,
		IsSplit:    true,
		IsDone:     false,
		IsSplitAce: false,
	}
	handPtr.IsSplit = true
	handPtr.Cards = handPtr.Cards[:1]
	c1 := g.shoe.Draw(g.cfg.numDecks)
	c2 := g.shoe.Draw(g.cfg.numDecks)

	handPtr.Cards = append(handPtr.Cards, c1)
	newHand.Cards = append(newHand.Cards, c2)
	g.logger.Printf("Сплит, доплата $%d, руки: %s и %s", handPtr.Bet, handPtr.String(), newHand.String())

	if handPtr.Cards[0].Value == "A" {
		handPtr.IsSplitAce = true
		newHand.IsSplitAce = true

		g.logger.Printf("Сплит тузов – по одной карте, повторный сплит запрещён")
	}

	*hands = append(*hands, newHand)

	g.console.Println("🃏 Рука разделена на две!")
}

func (g *game) allBusted(hands []hand) bool {
	for _, handVal := range hands {
		if handVal.Score() <= maxScore {
			return false
		}
	}

	return true
}

func (g *game) dealerTurn(dealerHand *hand) {
	g.console.Println("\n=== ХОД ДИЛЕРА ===")

	for dealerHand.Score() < 17 || (g.cfg.dealerHitsSoft17 && dealerHand.Score() == 17 && dealerHand.IsSoft()) {
		g.console.Println("Рука дилера: ")
		dealerHand.Print(false)
		g.console.Println("Дилер берёт карту...")
		time.Sleep(dealerSleepMs * time.Millisecond)

		c := g.shoe.Draw(g.cfg.numDecks)
		dealerHand.Cards = append(dealerHand.Cards, c)
		g.logger.Printf("Дилер взял: %s%s", c.Value, c.Suit)
	}
}

func (g *game) resolveAllHands(hands []hand, dealerHand hand) {
	g.console.Println("\n=== ИТОГИ РАУНДА ===")
	g.console.Println("🤖 Рука дилера: ")
	dealerHand.Print(false)
	dScore := dealerHand.Score()
	g.logger.Printf("Итог дилера: %s (очки %d)", dealerHand.String(), dScore)

	for handIdx, handVal := range hands {
		if len(hands) > 1 {
			g.console.Printf("Рука #%d (ставка $%d):\n", handIdx+1, handVal.Bet)
		} else {
			g.console.Println("Ваш результат: ")
		}

		handVal.Print(false)
		g.resolveHand(handVal, dScore)
	}
}

func (g *game) resolveHand(handVal hand, dScore int) {
	pScore := handVal.Score()

	switch {
	case pScore > maxScore:
		g.console.Println("Перебор! Вы проиграли.")
		g.logger.Printf("Рука %s (очки %d) – перебор, проигрыш", handVal.String(), pScore)
	case dScore > maxScore:
		g.console.Printf("У дилера перебор! Вы выиграли +$%d\n", handVal.Bet)
		winAmount := handVal.Bet * payoutReturnFactor
		g.player += winAmount
		g.dealer -= winAmount
		g.logger.Printf("Рука %s – победа (дилер перебор), +$%d", handVal.String(), handVal.Bet)
	case pScore > dScore:
		g.console.Printf("Победа! +$%d\n", handVal.Bet)
		winAmount := handVal.Bet * payoutReturnFactor
		g.player += winAmount
		g.dealer -= winAmount
		g.logger.Printf("Рука %s – победа, +$%d", handVal.String(), handVal.Bet)
	case dScore > pScore:
		g.console.Println("Дилер победил. Ставка проиграна.")
		g.logger.Printf("Рука %s – проигрыш", handVal.String())
	default:
		g.console.Println("Ничья (Пуш). Ставка возвращена.")
		g.player += handVal.Bet
		g.dealer -= handVal.Bet
		g.logger.Printf("Рука %s – ничья, ставка возвращена", handVal.String())
	}
}

// -------------------- Разбор аргументов командной строки (с использованием flag) --------------------

type cliArgs struct {
	reset      bool
	log        bool
	playerInit int
	dealerInit int
}

// parseArgs разбирает аргументы командной строки с помощью пакета flag.
func parseArgs() cliArgs {
	var args cliArgs

	flag.BoolVar(&args.reset, "reset", false, "Сбросить сохранённую игру")
	flag.BoolVar(&args.log, "log", false, "Включить логирование в файл "+defaultLogFile)
	flag.IntVar(&args.playerInit, "player", -1, "Начальный баланс игрока (если не указан, берётся из конфига)")
	flag.IntVar(&args.dealerInit, "dealer", -1, "Начальный баланс казино (если не указан, берётся из конфига)")
	flag.Parse()

	return args
}

// -------------------- Точка входа --------------------

func main() {
	cfg := defaultConfig()
	cli := parseArgs()

	if cli.reset {
		err := resetSave()
		if err != nil {
			fmt.Println("⚠️ Не удалось сбросить сохранение:", err) //nolint:forbidigo
		} else {
			fmt.Println("🗑️ Прогресс сброшен.") //nolint:forbidigo
		}
	}

	if cli.playerInit > 0 {
		cfg.initialPlayer = cli.playerInit
	}

	if cli.dealerInit > 0 {
		cfg.initialDealer = cli.dealerInit
	}

	var logger Logger

	//nolint:nestif // сложность блока оправдана настройкой логгера
	if cli.log {
		flogger, err := newFileLogger(defaultLogFile)
		if err != nil {
			fmt.Printf("⚠️ Не удалось создать лог-файл: %v. Логирование отключено.\n", err) //nolint:forbidigo

			logger = noopLogger{}
		} else {
			fmt.Printf("📝 Логирование включено (файл %s)\n", defaultLogFile) //nolint:forbidigo

			logger = flogger

			defer func() {
				if fl, ok := logger.(*fileLogger); ok && fl != nil {
					err := fl.Close()
					if err != nil {
						fmt.Printf("⚠️ Ошибка закрытия лог-файла: %v\n", err) //nolint:forbidigo
					}
				}
			}()
		}
	} else {
		logger = noopLogger{}
	}

	console := newConsoleIO()
	gameInstance := loadOrInitGame(cfg, logger, console)
	gameInstance.run()
}
