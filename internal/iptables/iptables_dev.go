package iptables

import (
	"log"
	"sync"
)

// DevIPT is a mock iptables for development without kernel access.
// Tracks connected IPs in memory and simulates byte counters.
type DevIPT struct {
	mu       sync.Mutex
	clients  map[string]bool
	counters map[string]int64 // simulated byte counters
}

func NewDev() *DevIPT {
	log.Println("iptables: using DEV stub (in-memory)")
	return &DevIPT{
		clients:  make(map[string]bool),
		counters: make(map[string]int64),
	}
}

func (d *DevIPT) AddClient(ip string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.clients[ip] = true
	log.Printf("iptables-dev: ADD %s", ip)
	return nil
}

func (d *DevIPT) RemoveClient(ip string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.clients, ip)
	delete(d.counters, ip)
	log.Printf("iptables-dev: REMOVE %s", ip)
	return nil
}

func (d *DevIPT) IsConnected(ip string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.clients[ip]
}

// GetForwardByteCounters returns simulated byte counters.
// In dev mode, each connected client "uses" ~50KB per poll cycle
// to simulate realistic data consumption.
func (d *DevIPT) GetForwardByteCounters() (map[string]int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	result := make(map[string]int64)
	for ip := range d.clients {
		// Simulate ~50KB per 3s poll = ~1MB/min
		bytes := int64(50_000)
		d.counters[ip] += bytes
		result[ip] = bytes
	}
	return result, nil
}
