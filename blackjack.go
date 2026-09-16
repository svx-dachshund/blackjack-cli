// Package main реализует консольную игру в блэкджек.
//
// Возможности:
//   - настраиваемое число колод, карта среза, правила удвоения/сплита;
//   - сохранение и загрузка прогресса в файл;
//   - опциональное логирование партий в файл;
//   - ASCII-рендеринг карт в терминале.
//
// Запуск: go run blackjack.go [-reset] [-log] [-player N] [-dealer N] [-decks N] [-speed MS]
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
	insurancePayoutMul   = 2 // 2:1 прибыль
	insuranceBetDiv      = 2 // страховка = 1/2 ставки
	payoutReturnFactor   = 2 // возврат ставки + выигрыш 1:1
	surrenderRefundDiv   = 2 // сдача = возврат 1/2 ставки

	defaultNumDecks      = 4
	defaultCutDivisor    = 3
	defaultInitialPlayer = 1000
	defaultInitialDealer = 5000
	defaultMaxSplits     = 3
	defaultDealerSleepMs = 800

	defaultSaveFile = "blackjack_save.json"
	defaultLogFile  = "blackjack.log"

	fileMode = 0o600

	actionHit       = "h"
	actionStand     = "s"
	actionDouble    = "d"
	actionSplit     = "p"
	actionSurrender = "r"
)

// errInvalidBalance — статическая ошибка для err113.
var errInvalidBalance = errors.New("некорректные балансы в сохранении")

// -------------------- Вывод (обход forbidigo) --------------------

func emit(args ...any)                 { _, _ = fmt.Fprint(os.Stdout, args...) }
func emitf(format string, args ...any) { _, _ = fmt.Fprintf(os.Stdout, format, args...) }
func emitln(args ...any)               { _, _ = fmt.Fprintln(os.Stdout, args...) }

// -------------------- Конфигурация --------------------

type config struct {
	numDecks              int
	cutDivisor            int
	initialPlayer         int
	initialDealer         int
	doubleDownScores      []int
	dealerHitsSoft17      bool
	allowDoubleAfterSplit bool
	maxSplits             int
	dealerSleepMs         int
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
		dealerSleepMs:         defaultDealerSleepMs,
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
	IsInitial  bool
}

// newInitialHand создаёт стартовую руку со всеми полями (exhaustruct).
func newInitialHand(cards []card, bet int) hand {
	return hand{
		Cards:      cards,
		Bet:        bet,
		IsDone:     false,
		IsSplitAce: false,
		IsSplit:    false,
		IsInitial:  true,
	}
}

func (h *hand) Score() int {
	score := 0
	aces := 0

	for _, cardValue := range h.Cards {
		score += cardValue.Rank

		if cardValue.Value == "A" {
			aces++
		}
	}

	for score > maxScore && aces > 0 {
		score -= 10
		aces--
	}

	return score
}

func (h *hand) IsSoft() bool {
	minScore := 0
	hasAce := false

	for _, cardValue := range h.Cards {
		if cardValue.Value == "A" {
			minScore++
			hasAce = true
		} else {
			minScore += cardValue.Rank
		}
	}

	return hasAce && minScore+10 <= maxScore
}

func (h *hand) IsBlackjack() bool {
	return h.IsInitial && !h.IsSplit && !h.IsSplitAce &&
		len(h.Cards) == 2 && h.Score() == maxScore
}

func (h *hand) CanSplit() bool {
	return len(h.Cards) == 2 &&
		h.Cards[0].Rank == h.Cards[1].Rank &&
		!h.IsSplitAce
}

func (h *hand) String() string {
	parts := make([]string, 0, len(h.Cards))
	for _, cardValue := range h.Cards {
		parts = append(parts, cardValue.Value+cardValue.Suit)
	}

	return strings.Join(parts, " ")
}

// -------------------- ASCII-рендеринг --------------------

const (
	resetColor   = "\033[0m"
	redColor     = "\033[31m"
	whiteColor   = "\033[97m"
	grayColor    = "\033[90m"
	cardHeight   = 4
	hiddenPoints = "(очки: ?)"
)

func cardColor(cardValue card) string {
	if cardValue.Suit == "♥" || cardValue.Suit == "♦" {
		return redColor
	}

	return whiteColor
}

func getCardLines(cardValue card) []string {
	color := cardColor(cardValue)

	return []string{
		color + "┌───┐" + resetColor,
		fmt.Sprintf(color+"│%3s│"+resetColor, cardValue.Value),
		fmt.Sprintf(color+"│%3s│"+resetColor, cardValue.Suit),
		color + "└───┘" + resetColor,
	}
}

func getBackLines() []string {
	return []string{
		grayColor + "┌───┐" + resetColor,
		grayColor + "│░░░│" + resetColor,
		grayColor + "│░░░│" + resetColor,
		grayColor + "└───┘" + resetColor,
	}
}

// Print выводит руку. hideSecond скрывает все карты, начиная со второй.
func (h *hand) Print(hideSecond bool) {
	if len(h.Cards) == 0 {
		emitln("[Пусто]")

		return
	}

	lines := make([]string, cardHeight)

	for index, cardValue := range h.Cards {
		var cardLines []string
		if hideSecond && index >= 1 {
			cardLines = getBackLines()
		} else {
			cardLines = getCardLines(cardValue)
		}

		for lineIndex := range cardHeight {
			if index > 0 {
				lines[lineIndex] += " "
			}

			lines[lineIndex] += cardLines[lineIndex]
		}
	}

	for _, line := range lines {
		emitln(line)
	}

	if hideSecond && len(h.Cards) > 1 {
		emit(hiddenPoints)
	} else {
		emitf("(очки: %d)", h.Score())
	}

	emitln()
}

// -------------------- Обувь (shoe) --------------------

type shoe struct {
	Cards []card
	Total int
}

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
			for _, value := range values {
				cards = append(cards, card{Suit: suit, Value: value.val, Rank: value.rank})
			}
		}
	}

	//nolint:gosec // игровой PRNG, криптостойкость не требуется
	rand.Shuffle(len(cards), func(i, j int) { cards[i], cards[j] = cards[j], cards[i] })

	return &shoe{Cards: cards, Total: len(cards)}
}

func (s *shoe) NeedsCut(cutDivisor int) bool {
	if cutDivisor <= 0 {
		return false
	}

	return s.Total > 0 && len(s.Cards) <= s.Total/cutDivisor
}

func (s *shoe) Draw(numDecks int) card {
	if len(s.Cards) == 0 {
		*s = *newShoe(numDecks)
	}

	c := s.Cards[0]
	s.Cards = s.Cards[1:]

	return c
}

// -------------------- Сохранение / загрузка --------------------

type saveData struct {
	PlayerBalance int `json:"playerBalance"`
	DealerBalance int `json:"dealerBalance"`
}

func savePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}

	return filepath.Join(dir, defaultSaveFile)
}

func saveGame(player, dealer int) error {
	data := saveData{PlayerBalance: player, DealerBalance: dealer}

	buf, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("маршалинг: %w", err)
	}

	err = os.WriteFile(savePath(), buf, fileMode)
	if err != nil {
		return fmt.Errorf("запись: %w", err)
	}

	return nil
}

func loadGame() (int, int, error) {
	buf, err := os.ReadFile(savePath())
	if err != nil {
		return 0, 0, fmt.Errorf("чтение файла сохранения: %w", err)
	}

	var data saveData

	err = json.Unmarshal(buf, &data)
	if err != nil {
		return 0, 0, fmt.Errorf("разбор: %w", err)
	}

	if data.PlayerBalance <= 0 || data.DealerBalance <= 0 {
		return 0, 0, errInvalidBalance
	}

	return data.PlayerBalance, data.DealerBalance, nil
}

func resetSave() error {
	err := os.Remove(savePath())
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("удаление файла сохранения: %w", err)
	}

	return nil
}

// -------------------- Игра --------------------

type game struct {
	cfg    config
	player int
	dealer int
	shoe   *shoe
	logger *log.Logger // nil = без логирования
	reader *bufio.Reader
}

func newGame(cfg config, logger *log.Logger) *game {
	return &game{
		cfg:    cfg,
		player: cfg.initialPlayer,
		dealer: cfg.initialDealer,
		shoe:   newShoe(cfg.numDecks),
		logger: logger,
		reader: bufio.NewReader(os.Stdin),
	}
}

func (g *game) logf(format string, args ...any) {
	if g.logger != nil {
		g.logger.Printf(format, args...)
	}
}

func (g *game) readLine(prompt string) (string, error) {
	emit(prompt)

	line, err := g.reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("чтение строки: %w", err)
	}

	return strings.TrimSpace(line), nil
}

func (g *game) askYesNo(prompt string) bool {
	answer, err := g.readLine(prompt)
	if err != nil {
		g.logf("ошибка ввода: %v", err)

		return false
	}

	switch strings.ToLower(answer) {
	case "y", "yes", "д", "да":
		return true
	}

	return false
}

// loadOrInit возвращает игру, загружая сохранение если оно валидно.
func loadOrInit(cfg config, logger *log.Logger) *game {
	player, dealer, err := loadGame()
	gameInstance := newGame(cfg, logger)

	if err != nil {
		emitln("🔄 Начинаем новую игру.")
		gameInstance.logf("=== НОВАЯ ИГРА === игрок $%d, казино $%d",
			gameInstance.player, gameInstance.dealer)

		return gameInstance
	}

	gameInstance.player = player
	gameInstance.dealer = dealer
	emitf("📂 Загружено сохранение:\n   Ваш баланс: $%d\n   Баланс казино: $%d\n", player, dealer)
	gameInstance.logf("=== ЗАГРУЖЕНА ИГРА === игрок $%d, казино $%d",
		gameInstance.player, gameInstance.dealer)

	return gameInstance
}

// -------------------- Цикл игры --------------------

func (g *game) run() {
	g.welcome()

	for g.player > 0 && g.dealer > 0 {
		emitf("\n💰 Баланс Казино: $%d | 💵 Ваш Баланс: $%d\n", g.dealer, g.player)

		if !g.playRound() {
			break
		}

		g.persistProgress()
	}

	if g.player <= 0 {
		_ = resetSave()
	}

	g.printFinalMessage()
}

func (g *game) welcome() {
	emitln("=== ДОБРО ПОЖАЛОВАТЬ В CASINO BLACKJACK ===")
}

func (g *game) persistProgress() {
	if g.player <= 0 || g.dealer <= 0 {
		return
	}

	err := saveGame(g.player, g.dealer)
	if err != nil {
		emitf("⚠️ Не удалось сохранить: %v\n", err)
	}
}

func (g *game) printFinalMessage() {
	emitln("\n=== ИГРА ОКОНЧЕНА ===")

	switch {
	case g.player <= 0:
		emitln("🛑 Вы обанкротились! Казино забирает всё.")
	case g.dealer <= 0:
		emitln("🏆 Поздравляем! Вы разорили казино!")
	default:
		emitf("Вы вышли из игры. Итоговый капитал: $%d\n", g.player)
	}

	g.logf("=== ИГРА ЗАВЕРШЕНА === игрок $%d, казино $%d", g.player, g.dealer)
}

// -------------------- Раунд --------------------

// playRound возвращает false, если игрок решил выйти (q).
func (g *game) playRound() bool {
	bet, ok := g.readBet()
	if !ok {
		return false
	}

	g.player -= bet
	g.dealer += bet
	g.logf("=== НОВЫЙ РАУНД === Ставка: $%d", bet)

	g.reshuffleIfNeeded()

	playerHand := newInitialHand(
		[]card{g.shoe.Draw(g.cfg.numDecks), g.shoe.Draw(g.cfg.numDecks)}, bet)
	dealerHand := newInitialHand(
		[]card{g.shoe.Draw(g.cfg.numDecks), g.shoe.Draw(g.cfg.numDecks)}, 0)

	g.logf("Игрок: %s", playerHand.String())
	g.logf("Дилер: %s + [скрыто]", dealerHand.Cards[0].Value+dealerHand.Cards[0].Suit)

	if g.tryEvenMoney(playerHand, dealerHand, bet) {
		return true
	}

	insuranceTaken, insuranceBet := g.tryInsurance(dealerHand, bet)

	if playerHand.IsBlackjack() || dealerHand.IsBlackjack() {
		g.resolveBlackjacks(playerHand, dealerHand, insuranceTaken, insuranceBet)

		return true
	}

	if insuranceTaken {
		emitln("❌ Страховка не сыграла.")
	}

	return g.playPlayerHands(playerHand, dealerHand, bet)
}

func (g *game) reshuffleIfNeeded() {
	if g.shoe.NeedsCut(g.cfg.cutDivisor) {
		emitln("🃏 Карта среза: перетасовка.")
		g.logf("Перетасовка: осталось %d/%d", len(g.shoe.Cards), g.shoe.Total)
		g.shoe = newShoe(g.cfg.numDecks)
	}
}

func (g *game) tryEvenMoney(playerHand, dealerHand hand, bet int) bool {
	if !playerHand.IsBlackjack() || dealerHand.Cards[0].Value != "A" {
		return false
	}

	if !g.askYesNo("У вас Блэкджек! У дилера открыт ТУЗ. Взять 1:1 (even money)? (y/n): ") {
		return false
	}

	g.player += bet + bet
	g.dealer -= bet + bet
	g.logf("Even money принято, прибыль $%d", bet)
	emitf("✅ Even money: прибыль $%d. Раунд завершён.\n", bet)

	return true
}

func (g *game) tryInsurance(dealerHand hand, bet int) (bool, int) {
	if dealerHand.Cards[0].Value != "A" {
		return false, 0
	}

	insuranceBet := bet / insuranceBetDiv
	if insuranceBet <= 0 || insuranceBet > g.player {
		return false, 0
	}

	prompt := fmt.Sprintf("🛡️ У дилера открыт ТУЗ. Страховка за $%d? (y/n): ", insuranceBet)
	if !g.askYesNo(prompt) {
		return false, 0
	}

	g.player -= insuranceBet
	g.dealer += insuranceBet
	g.logf("Страховка принята: $%d", insuranceBet)
	emitf("✅ Страховка принята ($%d).\n", insuranceBet)

	return true, insuranceBet
}

func (g *game) playPlayerHands(playerHand, dealerHand hand, bet int) bool {
	hands := []hand{playerHand}
	if g.processPlayerHands(&hands, &dealerHand) {
		refund := bet / surrenderRefundDiv
		g.player += refund
		g.dealer -= refund
		g.logf("Сдача: возврат $%d", refund)
		emitf("\n🏳️ Вы сдались. Возврат $%d, потеряно $%d\n", refund, bet-refund)

		return true
	}

	if !allBusted(hands) {
		g.dealerTurn(&dealerHand)
	}

	g.resolveAllHands(hands, dealerHand)

	return true
}

// resolveBlackjacks обрабатывает ситуации BJ у игрока и/или дилера.
func (g *game) resolveBlackjacks(playerHand, dealerHand hand, insuranceTaken bool, insuranceBet int) {
	emitln("\n--- Вскрытие стартовых карт ---")
	emitln("Ваша рука: ")
	playerHand.Print(false)
	emitln("Рука дилера: ")
	dealerHand.Print(false)

	playerBJ := playerHand.IsBlackjack()
	dealerBJ := dealerHand.IsBlackjack()

	if dealerBJ && insuranceTaken {
		profit := insuranceBet * insurancePayoutMul
		g.player += insuranceBet + profit
		g.dealer -= insuranceBet + profit
		g.logf("Страховка выиграла: возврат $%d", insuranceBet+profit)
		emitf("🛡️ Страховка выиграла! Прибыль $%d\n", profit)
	}

	switch {
	case playerBJ && dealerBJ:
		g.player += playerHand.Bet
		g.dealer -= playerHand.Bet
		g.logf("Ничья (оба BJ), ставка возвращена")
		emitln("🤝 У обоих Блэкджек — ничья (ставка возвращена).")

	case dealerBJ:
		if insuranceTaken {
			emitln("💀 У дилера Блэкджек. Ставка проиграна (страховка компенсировала).")
		} else {
			emitln("💀 У дилера Блэкджек. Вы проиграли.")
		}

		g.logf("Дилер BJ, игрок проиграл ставку")

	case playerBJ:
		win := playerHand.Bet * blackjackPayoutMul / blackjackPayoutDenom
		g.player += playerHand.Bet + win
		g.dealer -= playerHand.Bet + win
		g.logf("Игрок BJ, прибыль $%d (3:2)", win)
		emitf("🎉 Натуральный Блэкджек! Выигрыш 3:2: +$%d\n", win)
	}
}

// -------------------- Руки игрока --------------------

// processPlayerHands проигрывает все руки игрока. Возвращает true при сдаче.
func (g *game) processPlayerHands(hands *[]hand, dealerHand *hand) bool {
	for idx := 0; idx < len(*hands); {
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

		action := g.askAction(*currentHand, len(*hands))
		prevLen := len(*hands)

		if g.executeAction(hands, idx, action) {
			return true
		}

		if len(*hands) > prevLen {
			continue
		}

		if (*hands)[idx].IsDone {
			idx++
		}
	}

	return false
}

func (g *game) showGameState(hands []hand, dealerHand hand, idx int) {
	emitln("\n--- Рука дилера: ")
	dealerHand.Print(true)

	if len(hands) > 1 {
		emitf("--- Ваша рука #%d: \n", idx+1)
	} else {
		emitln("--- Ваша рука: ")
	}

	hands[idx].Print(false)
}

func (g *game) askAction(currentHand hand, totalHands int) string {
	options := "[h] Взять, [s] Остановиться"
	if g.canDouble(currentHand) {
		options += ", [d] Удвоить"
	}

	if g.canSplit(currentHand, totalHands) {
		options += ", [p] Разделить"
	}

	if len(currentHand.Cards) == 2 && totalHands == 1 {
		options += ", [r] Сдаться"
	}

	for {
		answer, err := g.readLine(fmt.Sprintf("Выберите действие %s: ", options))
		if err != nil {
			emitln("⚠️ Ошибка ввода, попробуйте снова.")

			continue
		}

		return answer
	}
}

func (g *game) splitLimitReached(totalHands int) bool {
	return g.cfg.maxSplits > 0 && totalHands > g.cfg.maxSplits
}

func (g *game) canDouble(currentHand hand) bool {
	if len(currentHand.Cards) != 2 || g.player < currentHand.Bet || g.dealer < currentHand.Bet {
		return false
	}

	if currentHand.IsSplitAce {
		return false
	}

	if currentHand.IsSplit && !g.cfg.allowDoubleAfterSplit {
		return false
	}

	return slices.Contains(g.cfg.doubleDownScores, currentHand.Score())
}

func (g *game) canSplit(currentHand hand, totalHands int) bool {
	if !currentHand.CanSplit() || g.player < currentHand.Bet || g.dealer < currentHand.Bet {
		return false
	}

	return !g.splitLimitReached(totalHands)
}

// executeAction выполняет действие. Возвращает true только при сдаче.
func (g *game) executeAction(hands *[]hand, idx int, action string) bool {
	switch strings.ToLower(action) {
	case actionHit, "hit", "взять":
		g.doHit(&(*hands)[idx])
	case actionStand, "stand", "остановиться":
		(*hands)[idx].IsDone = true

		g.logf("Игрок остановился")
	case actionDouble, "double", "удвоить":
		g.doDouble(&(*hands)[idx])
	case actionSplit, "split", "разделить":
		g.doSplit(hands, idx)
	case actionSurrender, "surrender", "сдаться":
		if len((*hands)[idx].Cards) == 2 && len(*hands) == 1 {
			return true
		}

		emitln("❌ Сдаться можно только с двумя картами до сплитов.")
	default:
		emitln("❌ Неизвестная команда.")
	}

	return false
}

func (g *game) doHit(currentHand *hand) {
	newCard := g.shoe.Draw(g.cfg.numDecks)
	currentHand.Cards = append(currentHand.Cards, newCard)
	currentHand.IsInitial = false

	g.logf("Игрок взял: %s%s", newCard.Value, newCard.Suit)

	if currentHand.Score() >= maxScore {
		currentHand.IsDone = true
	}
}

func (g *game) doDouble(currentHand *hand) {
	if !g.canDouble(*currentHand) {
		emitln("❌ Удвоить сейчас нельзя.")

		return
	}

	oldBet := currentHand.Bet
	g.player -= oldBet
	g.dealer += oldBet
	currentHand.Bet *= 2

	newCard := g.shoe.Draw(g.cfg.numDecks)
	currentHand.Cards = append(currentHand.Cards, newCard)
	currentHand.IsInitial = false
	currentHand.IsDone = true

	g.logf("Удвоение: доплата $%d, карта %s%s", oldBet, newCard.Value, newCard.Suit)

	emitln("Карта после удвоения: ")
	currentHand.Print(false)
}

func (g *game) doSplit(hands *[]hand, idx int) {
	currentHand := &(*hands)[idx]
	if !g.canSplit(*currentHand, len(*hands)) {
		emitln("❌ Сплит сейчас невозможен.")

		return
	}

	g.player -= currentHand.Bet
	g.dealer += currentHand.Bet

	newHand := hand{
		Cards:      []card{currentHand.Cards[1]},
		Bet:        currentHand.Bet,
		IsDone:     false,
		IsSplitAce: false,
		IsSplit:    true,
		IsInitial:  false,
	}

	currentHand.IsSplit = true
	currentHand.IsInitial = false
	currentHand.Cards = currentHand.Cards[:1]

	firstCard := g.shoe.Draw(g.cfg.numDecks)
	secondCard := g.shoe.Draw(g.cfg.numDecks)

	currentHand.Cards = append(currentHand.Cards, firstCard)
	newHand.Cards = append(newHand.Cards, secondCard)

	if currentHand.Cards[0].Value == "A" {
		currentHand.IsSplitAce = true
		newHand.IsSplitAce = true

		g.logf("Сплит тузов: по одной карте, повторный сплит запрещён")
	}

	*hands = append(*hands, newHand)
	g.logf("Сплит: руки %s и %s", currentHand.String(), newHand.String())
	emitln("🃏 Рука разделена на две!")
}

func allBusted(hands []hand) bool {
	for _, currentHand := range hands {
		if currentHand.Score() <= maxScore {
			return false
		}
	}

	return true
}

// -------------------- Ход дилера --------------------

func (g *game) dealerTurn(dealerHand *hand) {
	emitln("\n=== ХОД ДИЛЕРА ===")

	first := true

	for dealerHand.Score() < 17 ||
		(g.cfg.dealerHitsSoft17 && dealerHand.Score() == 17 && dealerHand.IsSoft()) {
		emitln("Рука дилера: ")
		dealerHand.Print(false)

		if first {
			emitln("Дилер берёт карту...")

			first = false
		}

		if g.cfg.dealerSleepMs > 0 {
			time.Sleep(time.Duration(g.cfg.dealerSleepMs) * time.Millisecond)
		}

		newCard := g.shoe.Draw(g.cfg.numDecks)
		dealerHand.Cards = append(dealerHand.Cards, newCard)
		g.logf("Дилер взял: %s%s", newCard.Value, newCard.Suit)
	}
}

// -------------------- Итоги --------------------

func (g *game) resolveAllHands(hands []hand, dealerHand hand) {
	emitln("\n=== ИТОГИ РАУНДА ===")
	emitln("🤖 Рука дилера: ")
	dealerHand.Print(false)

	dealerScore := dealerHand.Score()
	g.logf("Итог дилера: %s (очки %d)", dealerHand.String(), dealerScore)

	for idx, currentHand := range hands {
		if len(hands) > 1 {
			emitf("Рука #%d (ставка $%d):\n", idx+1, currentHand.Bet)
		} else {
			emitln("Ваш результат: ")
		}

		currentHand.Print(false)
		g.resolveHand(currentHand, dealerScore)
	}
}

func (g *game) resolveHand(currentHand hand, dealerScore int) {
	playerScore := currentHand.Score()

	switch {
	case playerScore > maxScore:
		emitln("Перебор! Вы проиграли.")
		g.logf("Рука %s (очки %d) — перебор, проигрыш", currentHand.String(), playerScore)

	case dealerScore > maxScore:
		win := currentHand.Bet * payoutReturnFactor
		g.player += win
		g.dealer -= win
		emitf("У дилера перебор! Возврат $%d, прибыль $%d\n", win, currentHand.Bet)
		g.logf("Рука %s — победа (дилер перебор), возврат $%d", currentHand.String(), win)

	case playerScore > dealerScore:
		win := currentHand.Bet * payoutReturnFactor
		g.player += win
		g.dealer -= win
		emitf("Победа! Возврат $%d, прибыль $%d\n", win, currentHand.Bet)
		g.logf("Рука %s — победа, возврат $%d", currentHand.String(), win)

	case dealerScore > playerScore:
		emitln("Дилер победил. Ставка проиграна.")
		g.logf("Рука %s — проигрыш", currentHand.String())

	default:
		g.player += currentHand.Bet
		g.dealer -= currentHand.Bet

		emitln("Ничья (Пуш). Ставка возвращена.")
		g.logf("Рука %s — ничья", currentHand.String())
	}
}

// -------------------- Ставка --------------------

func (g *game) readBet() (int, bool) {
	for {
		input, err := g.readLine("Введите ставку (или 'q' для выхода): ")
		if err != nil {
			emitln("⚠️ Ошибка чтения. Попробуйте ещё раз.")

			continue
		}

		if strings.ToLower(input) == "q" {
			return 0, false
		}

		bet, err := strconv.Atoi(input)

		switch {
		case err != nil:
			emitln("❌ Введите целое число.")
		case bet <= 0:
			emitln("❌ Ставка должна быть положительной.")
		case bet > g.player:
			emitf("❌ Недостаточно средств. Баланс: $%d\n", g.player)
		case bet > g.dealer:
			emitf("❌ У казино недостаточно средств ($%d)\n", g.dealer)
		default:
			return bet, true
		}
	}
}

// -------------------- CLI --------------------

type cliArgs struct {
	reset      bool
	log        bool
	playerInit int
	dealerInit int
	numDecks   int
	speedMs    int
}

func parseArgs() cliArgs {
	var parsed cliArgs

	flag.BoolVar(&parsed.reset, "reset", false, "Сбросить сохранённую игру")
	flag.BoolVar(&parsed.log, "log", false, "Включить логирование в "+defaultLogFile)
	flag.IntVar(&parsed.playerInit, "player", -1, "Начальный баланс игрока")
	flag.IntVar(&parsed.dealerInit, "dealer", -1, "Начальный баланс казино")
	flag.IntVar(&parsed.numDecks, "decks", -1, "Число колод в обуви")
	flag.IntVar(&parsed.speedMs, "speed", -1, "Задержка дилера в мс (0 — без задержки)")
	flag.Parse()

	return parsed
}

func (a cliArgs) apply(cfg *config) {
	if a.playerInit > 0 {
		cfg.initialPlayer = a.playerInit
	}

	if a.dealerInit > 0 {
		cfg.initialDealer = a.dealerInit
	}

	if a.numDecks > 0 {
		cfg.numDecks = a.numDecks
	}

	if a.speedMs >= 0 {
		cfg.dealerSleepMs = a.speedMs
	}
}

// -------------------- Точка входа --------------------

func main() {
	cli := parseArgs()
	cfg := defaultConfig()
	cli.apply(&cfg)

	if cli.reset {
		err := resetSave()
		if err != nil {
			emitln("⚠️ Не удалось сбросить сохранение:", err)
		} else {
			emitln("🗑️ Прогресс сброшен.")
		}
	}

	logger, closeLogger := setupLogger(cli.log)
	defer closeLogger()

	loadOrInit(cfg, logger).run()
}

// setupLogger возвращает логгер и функцию закрытия.
// Если логирование отключено или не удалось — возвращает (nil, noop).
func setupLogger(enable bool) (*log.Logger, func()) {
	noop := func() {}

	if !enable {
		return nil, noop
	}

	file, err := os.OpenFile(defaultLogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, fileMode)
	if err != nil {
		emitf("⚠️ Не удалось открыть %s: %v. Логирование отключено.\n", defaultLogFile, err)

		return nil, noop
	}

	emitf("📝 Логирование включено (файл %s)\n", defaultLogFile)

	return log.New(file, "", log.LstdFlags), func() {
		cerr := file.Close()
		if cerr != nil {
			emitf("⚠️ Ошибка закрытия лог-файла: %v\n", cerr)
		}
	}
}
