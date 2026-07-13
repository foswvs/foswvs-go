package gpio

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config holds the GPIO pin assignments and coin detection settings for the coin acceptor.
// Persisted so an admin can remap pins and tune detection from the dashboard without
// touching the binary.
type Config struct {
	SlotPin    int `json:"slot_pin"`
	SensorPin  int `json:"sensor_pin"`
	DebounceMS int `json:"debounce_ms"`
}

// DefaultConfig returns the factory-default pin assignments and settings.
func DefaultConfig() Config {
	return Config{SlotPin: 2, SensorPin: 17, DebounceMS: 88}
}

func configPath(dataDir string) string {
	return filepath.Join(dataDir, "gpio.json")
}

// LoadConfig reads the GPIO pin config, falling back to defaults if no
// config file exists yet or it can't be parsed.
func LoadConfig(dataDir string) Config {
	data, err := os.ReadFile(configPath(dataDir))
	if err != nil {
		return DefaultConfig()
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil || cfg.SlotPin <= 0 || cfg.SensorPin <= 0 {
		return DefaultConfig()
	}
	if cfg.DebounceMS <= 0 || cfg.DebounceMS > 1000 {
		cfg.DebounceMS = 88
	}
	return cfg
}

// SaveConfig persists the GPIO pin config for future restarts.
func SaveConfig(dataDir string, cfg Config) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(dataDir), data, 0644)
}
