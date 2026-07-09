package handlers

import "github.com/foswvs/foswvs-go/internal/gpio"

// Firewall abstracts iptables operations for real and dev mode.
type Firewall interface {
	AddClient(ip string) error
	RemoveClient(ip string) error
	IsConnected(ip string) bool
	GetForwardByteCounters() (map[string]int64, error)
}

// CoinAcceptor abstracts the GPIO coin slot for real and dev mode.
type CoinAcceptor interface {
	IsBusy() bool
	SensorRead() int
	RunTopup(mac string, amountToMB func(int) float64, cancelCh <-chan struct{}) <-chan gpio.TopupResult
	PinConfig() gpio.Config
	Reconfigure(slotPin, sensorPin int) error
}
