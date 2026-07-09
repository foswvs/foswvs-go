package handlers

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Maintenance mode lets an admin work around a broken coin acceptor
// without taking the whole box out of service: lock the portal down
// entirely, or hand out a fixed amount of free data to anyone who asks.
const (
	MaintenanceOff      = "off"
	MaintenanceLockdown = "lockdown"
	MaintenanceFree     = "free"
)

// MaintenanceConfig is the persisted maintenance mode setting.
type MaintenanceConfig struct {
	Mode      string  `json:"mode"`
	FreeMB    float64 `json:"free_mb"`
	EnabledAt int64   `json:"enabled_at"` // unix seconds; when Mode last moved away from "off"
}

func maintenanceConfigPath(dataDir string) string {
	return filepath.Join(dataDir, "maintenance.json")
}

func loadMaintenanceConfig(dataDir string) MaintenanceConfig {
	data, err := os.ReadFile(maintenanceConfigPath(dataDir))
	if err != nil {
		return MaintenanceConfig{Mode: MaintenanceOff}
	}
	var cfg MaintenanceConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return MaintenanceConfig{Mode: MaintenanceOff}
	}
	if cfg.Mode != MaintenanceLockdown && cfg.Mode != MaintenanceFree {
		cfg.Mode = MaintenanceOff
	}
	return cfg
}

func saveMaintenanceConfig(dataDir string, cfg MaintenanceConfig) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(maintenanceConfigPath(dataDir), data, 0644)
}

// MaintenanceState is the live, mutex-protected maintenance config. Read on
// every /api/connect and /api/topup request, so reads are cheap (RLock over
// an in-memory struct, not a file read).
type MaintenanceState struct {
	mu      sync.RWMutex
	cfg     MaintenanceConfig
	dataDir string
}

func NewMaintenanceState(dataDir string) *MaintenanceState {
	return &MaintenanceState{cfg: loadMaintenanceConfig(dataDir), dataDir: dataDir}
}

func (m *MaintenanceState) Get() MaintenanceConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

// Set updates and persists the maintenance config. Moving into (or staying
// within) lockdown/free mode refreshes EnabledAt, which resets the
// once-per-window cap on free-mode grants (see Store.HasFreeSessionSince) —
// so bumping the free MB allowance intentionally makes everyone eligible
// for a fresh grant rather than being stuck with the old amount.
func (m *MaintenanceState) Set(mode string, freeMB float64) (MaintenanceConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg := MaintenanceConfig{Mode: mode, FreeMB: freeMB}
	if mode != MaintenanceOff {
		cfg.EnabledAt = time.Now().Unix()
	}

	if err := saveMaintenanceConfig(m.dataDir, cfg); err != nil {
		return m.cfg, err
	}
	m.cfg = cfg
	return cfg, nil
}

// maybeGrantMaintenanceFreeMB grants the configured free-mode data
// allowance to a device, if free mode is active and the device hasn't
// already received a grant since this maintenance window began. Returns
// true if a grant was made.
func (a *App) maybeGrantMaintenanceFreeMB(devID int64) bool {
	mc := a.Maintenance.Get()
	if mc.Mode != MaintenanceFree || mc.FreeMB <= 0 {
		return false
	}
	already, err := a.Store.HasFreeSessionSince(devID, mc.EnabledAt)
	if err != nil || already {
		return false
	}
	if _, err := a.Store.AddSession(devID, 0, mc.FreeMB); err != nil {
		log.Printf("maintenance free grant device=%d: %v", devID, err)
		return false
	}
	return true
}
