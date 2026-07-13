package gpio

import (
	"errors"
	"log"
	"os"
	"sync"
	"time"
)

// DevCoinslot is a mock coinslot for development without GPIO hardware.
// Simulates coin insertion via a timer when RunTopup is called.
type DevCoinslot struct {
	mu              sync.Mutex
	busy            bool
	slotPin         int
	sensorPin       int
	debounceDelay   time.Duration
}

func NewDevCoinslot(slotPin, sensorPin int) *DevCoinslot {
	log.Println("gpio: using DEV stub (no hardware)")
	return &DevCoinslot{slotPin: slotPin, sensorPin: sensorPin, debounceDelay: 88 * time.Millisecond}
}

func (cs *DevCoinslot) IsBusy() bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.busy
}

func (cs *DevCoinslot) SensorOn()       {}
func (cs *DevCoinslot) SensorOff()      {}
func (cs *DevCoinslot) SensorRead() int { return 0 }
func (cs *DevCoinslot) SlotRead() int   { return 0 }

// PinConfig returns the currently configured pins. Not wired to any real
// hardware in dev mode — just tracked so the admin UI has something
// consistent to display and edit before a device is deployed for real.
func (cs *DevCoinslot) PinConfig() Config {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return Config{SlotPin: cs.slotPin, SensorPin: cs.sensorPin, DebounceMS: int(cs.debounceDelay.Milliseconds())}
}

// Reconfigure updates the tracked pins. No real GPIO to touch in dev mode.
func (cs *DevCoinslot) Reconfigure(slotPin, sensorPin int) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.busy {
		return errors.New("coinslot is busy")
	}
	cs.slotPin = slotPin
	cs.sensorPin = sensorPin
	return nil
}

// SetDebounceDelay updates the coin detection debounce delay.
func (cs *DevCoinslot) SetDebounceDelay(delay time.Duration) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.debounceDelay = delay
}

// GetDebounceDelay returns the current coin detection debounce delay in milliseconds.
func (cs *DevCoinslot) GetDebounceDelay() int {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return int(cs.debounceDelay.Milliseconds())
}

// RunTopup simulates a coin insertion session.
// In dev mode it auto-inserts 3 coins over 6 seconds.
func (cs *DevCoinslot) RunTopup(mac string, amountToMB func(int) float64, cancelCh <-chan struct{}) <-chan TopupResult {
	ch := make(chan TopupResult, 100)

	go func() {
		defer close(ch)

		cs.mu.Lock()
		cs.busy = true
		cs.mu.Unlock()
		defer func() {
			cs.mu.Lock()
			cs.busy = false
			cs.mu.Unlock()
		}()

		count := 0
		timeout := 15 * time.Second
		start := time.Now()
		nextCoin := time.After(2 * time.Second)

		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-cancelCh:
				ch <- TopupResult{MAC: mac, Amount: count, MB: amountToMB(count), Cancelled: true}
				return

			case <-nextCoin:
				if count < 3 {
					count++
					log.Printf("gpio-dev: simulated coin #%d", count)
					nextCoin = time.After(2 * time.Second)
				}

			case <-ticker.C:
				elapsed := time.Since(start)
				remaining := int(timeout.Seconds() - elapsed.Seconds())
				if remaining < 0 {
					remaining = 0
				}

				ch <- TopupResult{
					MAC:       mac,
					Amount:    count,
					MB:        amountToMB(count),
					Countdown: remaining,
				}

				if elapsed >= timeout {
					ch <- TopupResult{MAC: mac, Amount: count, MB: amountToMB(count), Done: true}
					return
				}
			}
		}
	}()

	return ch
}

// IsDevMode checks if FOSWVS_DEV environment variable is set.
func IsDevMode() bool {
	return os.Getenv("FOSWVS_DEV") == "1"
}
