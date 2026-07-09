package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DeviceTokenTTL is how long a device token stays valid without being
// refreshed. Reissued on top-up and whenever a device's MAC address is
// found to have changed (see App.reconcileDeviceToken).
const DeviceTokenTTL = 30 * 24 * time.Hour

// deviceClaims is the payload carried inside a device token.
type deviceClaims struct {
	Sub string `json:"sub"` // device ID, as a string
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

// jweHeader is the fixed JWE protected header for device tokens: direct
// symmetric encryption (no key-wrapping segment) with AES-256-GCM.
var jweHeader = mustEncodeHeader()

func mustEncodeHeader() string {
	h, err := json.Marshal(map[string]string{"alg": "dir", "enc": "A256GCM"})
	if err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(h)
}

// NewDeviceToken issues an encrypted, expiring token binding a device
// identity. A browser holds this in localStorage to reclaim its data
// balance after its MAC address rotates (iOS/Android randomize MAC per
// network by default, so the server would otherwise see a brand new
// device). It's encrypted (JWE compact serialization, A256GCM) rather than
// merely signed, so the device id isn't plainly readable to anyone who
// captures it off the air or lifts it from browser storage.
func NewDeviceToken(secret []byte, deviceID int64, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := deviceClaims{
		Sub: strconv.FormatInt(deviceID, 10),
		Iat: now.Unix(),
		Exp: now.Add(ttl).Unix(),
	}
	plaintext, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}

	gcm, err := newAESGCM(secret)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}

	aad := []byte(jweHeader)
	sealed := gcm.Seal(nil, nonce, plaintext, aad)
	tagLen := gcm.Overhead()
	ciphertext, tag := sealed[:len(sealed)-tagLen], sealed[len(sealed)-tagLen:]

	return strings.Join([]string{
		jweHeader,
		"", // no wrapped key in "dir" mode
		base64.RawURLEncoding.EncodeToString(nonce),
		base64.RawURLEncoding.EncodeToString(ciphertext),
		base64.RawURLEncoding.EncodeToString(tag),
	}, "."), nil
}

// ParseDeviceToken decrypts and validates a token minted by NewDeviceToken,
// returning the device ID it identifies.
func ParseDeviceToken(secret []byte, token string) (int64, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 5 {
		return 0, errors.New("malformed device token")
	}
	header, encKey, nonceB64, ctB64, tagB64 := parts[0], parts[1], parts[2], parts[3], parts[4]
	if header != jweHeader || encKey != "" {
		return 0, errors.New("unsupported device token header")
	}

	nonce, err := base64.RawURLEncoding.DecodeString(nonceB64)
	if err != nil {
		return 0, fmt.Errorf("decode nonce: %w", err)
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(ctB64)
	if err != nil {
		return 0, fmt.Errorf("decode ciphertext: %w", err)
	}
	tag, err := base64.RawURLEncoding.DecodeString(tagB64)
	if err != nil {
		return 0, fmt.Errorf("decode tag: %w", err)
	}

	gcm, err := newAESGCM(secret)
	if err != nil {
		return 0, err
	}

	plaintext, err := gcm.Open(nil, nonce, append(ciphertext, tag...), []byte(header))
	if err != nil {
		return 0, fmt.Errorf("invalid or tampered device token: %w", err)
	}

	var claims deviceClaims
	if err := json.Unmarshal(plaintext, &claims); err != nil {
		return 0, err
	}
	if time.Now().Unix() > claims.Exp {
		return 0, errors.New("device token expired")
	}

	devID, err := strconv.ParseInt(claims.Sub, 10, 64)
	if err != nil || devID <= 0 {
		return 0, errors.New("invalid device token subject")
	}
	return devID, nil
}

func newAESGCM(secret []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(secret)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	return cipher.NewGCM(block)
}

// ReadOrCreateDeviceTokenSecret loads the AES-256 key used to encrypt
// device tokens, generating and persisting one on first run. Losing this
// file invalidates every outstanding device token — clients silently fall
// back to being treated as new devices, which is safe, just loses the
// stored-balance convenience.
func ReadOrCreateDeviceTokenSecret(dataDir string) ([]byte, error) {
	path := filepath.Join(dataDir, "device_token.key")

	if data, err := os.ReadFile(path); err == nil && len(data) == 32 {
		return data, nil
	}

	secret := make([]byte, 32) // AES-256
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, secret, 0600); err != nil {
		return nil, err
	}
	return secret, nil
}
