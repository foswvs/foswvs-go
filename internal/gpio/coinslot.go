package gpio

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const gpioBase = "/sys/class/gpio/"

// Coinslot interfaces with the GPIO-based coin acceptor using active polling.
type Coinslot struct {
	mu            sync.Mutex
	busy          bool
	slotPin       int           // coin slot pulse input
	sensorPin     int           // sensor enable/disable output
	debounceDelay time.Duration // configurable debounce delay for coin detection
}

// NewCoinslot creates a new coin slot GPIO interface.
func NewCoinslot(slotPin, sensorPin int) *Coinslot {
	cs := &Coinslot{
		slotPin:       slotPin,
		sensorPin:     sensorPin,
		debounceDelay: 88 * time.Millisecond,
	}
	cs.initPins(slotPin, sensorPin)
	return cs
}

func (cs *Coinslot) initPins(slotPin, sensorPin int) {
	exportPin(slotPin)
	exportPin(sensorPin)
	time.Sleep(100 * time.Millisecond)
	setDirection(slotPin, "in")
	setEdge(slotPin, "both")
	setDirection(sensorPin, "out")
}

// PinConfig returns the currently active pin assignment.
func (cs *Coinslot) PinConfig() Config {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return Config{
		SlotPin:    cs.slotPin,
		SensorPin:  cs.sensorPin,
		DebounceMS: int(cs.debounceDelay.Milliseconds()),
	}
}

// SetDebounceDelay updates the coin detection debounce delay.
func (cs *Coinslot) SetDebounceDelay(delay time.Duration) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.debounceDelay = delay
}

// GetDebounceDelay returns the current coin detection debounce delay in milliseconds.
func (cs *Coinslot) GetDebounceDelay() int {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return int(cs.debounceDelay.Milliseconds())
}

// Reconfigure switches to a new pin assignment, exporting the new pins.
// Refuses to do so mid-topup, since a session's read loop is holding onto
// the old pin numbers for its duration.
func (cs *Coinslot) Reconfigure(slotPin, sensorPin int) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.busy {
		return errors.New("coinslot is busy")
	}
	cs.slotPin = slotPin
	cs.sensorPin = sensorPin
	cs.initPins(slotPin, sensorPin)
	return nil
}

func (cs *Coinslot) pins() (slot, sensor int) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.slotPin, cs.sensorPin
}

func exportPin(pin int) {
	path := fmt.Sprintf("%sgpio%d", gpioBase, pin)
	if _, err := os.Stat(path); err == nil {
		return // already exported
	}
	f, err := os.OpenFile(gpioBase+"export", os.O_WRONLY, 0)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(strconv.Itoa(pin))
}

func setDirection(pin int, dir string) {
	path := fmt.Sprintf("%sgpio%d/direction", gpioBase, pin)
	os.WriteFile(path, []byte(dir), 0644)
}

func setEdge(pin int, edge string) {
	path := fmt.Sprintf("%sgpio%d/edge", gpioBase, pin)
	os.WriteFile(path, []byte(edge), 0644)
}

func readPin(pin int) int {
	path := fmt.Sprintf("%sgpio%d/value", gpioBase, pin)
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	v, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return v
}

func writePin(pin int, val int) {
	path := fmt.Sprintf("%sgpio%d/value", gpioBase, pin)
	os.WriteFile(path, []byte(strconv.Itoa(val)), 0644)
}

// IsBusy returns true if a topup session is in progress.
func (cs *Coinslot) IsBusy() bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.busy
}

// SensorOn enables the coin acceptor sensor.
func (cs *Coinslot) SensorOn() {
	_, sensor := cs.pins()
	writePin(sensor, 1)
}

// SensorOff disables the coin acceptor sensor.
func (cs *Coinslot) SensorOff() {
	_, sensor := cs.pins()
	writePin(sensor, 0)
}

// SensorRead reads the sensor state (1 = active).
func (cs *Coinslot) SensorRead() int {
	_, sensor := cs.pins()
	return readPin(sensor)
}

// SlotRead reads the coin slot pulse (1 = coin detected).
func (cs *Coinslot) SlotRead() int {
	slot, _ := cs.pins()
	return readPin(slot)
}

// TopupResult is sent over the progress channel during coin insertion.
type TopupResult struct {
	MAC       string  `json:"mac"`
	Amount    int     `json:"amt"`
	MB        float64 `json:"mb"`
	Countdown int     `json:"cd"`
	Done      bool    `json:"done"`
	Cancelled bool    `json:"cancelled"`
}

// RunTopup starts a coin-counting session using active polling while sensor is ON.
// Matches the proven PHP implementation logic for accurate coin detection.
// It sends progress updates to the provided channel and blocks until timeout or cancellation.
// The cancel channel can be closed to abort the session early.
func (cs *Coinslot) RunTopup(mac string, amountToMB func(int) float64, cancelCh <-chan struct{}) <-chan TopupResult {
	ch := make(chan TopupResult, 100)

	go func() {
		defer close(ch)

		cs.mu.Lock()
		cs.busy = true
		debounce := cs.debounceDelay
		cs.mu.Unlock()
		defer func() {
			cs.mu.Lock()
			cs.busy = false
			cs.mu.Unlock()
		}()

		cs.SensorOn()
		defer cs.SensorOff()

		count := 0
		timeout := 50 * time.Second
		start := time.Now()

		// Timer for periodic status updates (100ms)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-cancelCh:
				ch <- TopupResult{MAC: mac, Amount: count, MB: amountToMB(count), Cancelled: true}
				return

			default:
				// Active polling: check slot pin and debounce
				if cs.SlotRead() == 1 {
					count++
					time.Sleep(debounce)
				}
			}

			// Check timeout and send periodic updates
			elapsed := time.Since(start)
			if elapsed >= timeout {
				mb := amountToMB(count)
				ch <- TopupResult{MAC: mac, Amount: count, MB: mb, Done: true}
				return
			}

			// Send periodic progress update on ticker
			select {
			case <-ticker.C:
				remaining := int((timeout - elapsed).Seconds())
				if remaining < 0 {
					remaining = 0
				}
				mb := amountToMB(count)
				ch <- TopupResult{
					MAC:       mac,
					Amount:    count,
					MB:        mb,
					Countdown: remaining,
				}
			case <-cancelCh:
				ch <- TopupResult{MAC: mac, Amount: count, MB: amountToMB(count), Cancelled: true}
				return
			default:
			}
		}
	}()

	return ch
}
