package gpio

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config holds the GPIO pin assignments for the coin acceptor. Persisted so
// an admin can remap pins from the dashboard if the factory default wiring
// doesn't match a given board/build, without touching the binary.
type Config struct {
	SlotPin   int `json:"slot_pin"`
	SensorPin int `json:"sensor_pin"`
}

// DefaultConfig returns the factory-default pin assignments.
func DefaultConfig() Config {
	return Config{SlotPin: 2, SensorPin: 17}
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
