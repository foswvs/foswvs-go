package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps the SQLite database. Concurrency is handled natively by SQLite's WAL mode.
type Store struct {
	db *sql.DB

	// Prepared statements for hot paths (avoid SQL re-parsing on each call)
	stmtGetDeviceIDByIP     *sql.Stmt
	stmtGetDeviceIDByMAC    *sql.Stmt
	stmtGetDeviceMAC        *sql.Stmt
	stmtGetDataUsage        *sql.Stmt
	stmtUpdateMBUsed        *sql.Stmt
	stmtGetTopupCount       *sql.Stmt
	stmtUpsertDevice        *sql.Stmt
	stmtAddSession          *sql.Stmt
	stmtIncrTopupCount      *sql.Stmt
	stmtAddShareTx          *sql.Stmt
}

// Device represents a connected WiFi client.
type Device struct {
	ID        int64  `json:"id"`
	MAC       string `json:"mac"`
	IP        string `json:"ip"`
	Hostname  string `json:"host"`
	UpdatedAt int64  `json:"updated_at"` // unix ms
}

// Session represents a topup/data allocation session.
type Session struct {
	ID        int64   `json:"id"`
	DeviceID  int64   `json:"device_id,omitempty"`
	Amount    float64 `json:"amt"`
	MBLimit   float64 `json:"mb_limit"`
	MBUsed    float64 `json:"mb_used"`
	CreatedAt int64   `json:"ts"`  // unix ms
	UpdatedAt int64   `json:"te"`  // unix ms
}

// Transaction is a session joined with device info for the txn list.
type Transaction struct {
	Amount   float64 `json:"amt"`
	MBLimit  float64 `json:"mb"`
	TS       int64   `json:"ts"`
	MAC      string  `json:"mac"`
	IP       string  `json:"ip"`
	Hostname string  `json:"host"`
}

// DataUsage holds limit and used MB for a device.
type DataUsage struct {
	MBLimit float64 `json:"mb_limit"`
	MBUsed  float64 `json:"mb_used"`
}

// DeviceSession combines device info with session/connection state.
type DeviceSession struct {
	MAC       string  `json:"mac"`
	IP        string  `json:"ip"`
	Hostname  string  `json:"host"`
	MBLimit   float64 `json:"mb_limit"`
	MBUsed    float64 `json:"mb_used"`
	ActiveAt  int64   `json:"active_at"`
	Connected bool    `json:"connected"`
}

// EarningsSummary holds aggregated piso counts.
type EarningsSummary struct {
	Day       float64 `json:"day"`
	Week      float64 `json:"week"`
	Month     float64 `json:"month"`
	Year      float64 `json:"year"`
	LastDay   float64 `json:"last_day"`
	LastWeek  float64 `json:"last_week"`
	LastMonth float64 `json:"last_month"`
	LastYear  float64 `json:"last_year"`
}

// BandwidthSummary holds aggregated data usage in MB.
type BandwidthSummary struct {
	Day   float64 `json:"day"`
	Week  float64 `json:"week"`
	Month float64 `json:"month"`
	Year  float64 `json:"year"`
}

func Open(dataDir string) (*Store, error) {
	dbPath := filepath.Join(dataDir, "foswvs.db")
	os.MkdirAll(dataDir, 0755)

	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	db.SetMaxOpenConns(1) // SQLite single-writer
	db.SetConnMaxLifetime(0)

	s := &Store{db: db}
	if err := s.createTables(); err != nil {
		return nil, err
	}
	if err := s.prepareStatements(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) prepareStatements() error {
	var err error
	s.stmtGetDeviceIDByIP, err = s.db.Prepare("SELECT id FROM devices WHERE ip_addr = ? ORDER BY updated_at DESC LIMIT 1")
	if err != nil {
		return err
	}
	s.stmtGetDeviceIDByMAC, err = s.db.Prepare("SELECT id FROM devices WHERE mac_addr = ?")
	if err != nil {
		return err
	}
	s.stmtGetDeviceMAC, err = s.db.Prepare("SELECT mac_addr FROM devices WHERE id = ?")
	if err != nil {
		return err
	}
	s.stmtGetDataUsage, err = s.db.Prepare("SELECT SUM(mb_limit), SUM(mb_used) FROM session WHERE device_id = ?")
	if err != nil {
		return err
	}
	s.stmtUpdateMBUsed, err = s.db.Prepare(`UPDATE session SET mb_used = mb_used + ?, updated_at = CURRENT_TIMESTAMP
	 WHERE id = (SELECT id FROM session WHERE device_id = ? AND mb_limit > mb_used ORDER BY id LIMIT 1)`)
	if err != nil {
		return err
	}
	s.stmtGetTopupCount, err = s.db.Prepare("SELECT topup_count FROM devices WHERE id = ?")
	if err != nil {
		return err
	}
	s.stmtUpsertDevice, err = s.db.Prepare(`INSERT INTO devices(mac_addr, ip_addr, hostname, topup_count, updated_at)
		VALUES(?, ?, ?, 0, CURRENT_TIMESTAMP)
		ON CONFLICT(mac_addr) DO UPDATE SET
		ip_addr=excluded.ip_addr, hostname=excluded.hostname, topup_count=0, updated_at=CURRENT_TIMESTAMP`)
	if err != nil {
		return err
	}
	s.stmtAddSession, err = s.db.Prepare("INSERT INTO session(device_id, piso_count, mb_limit) VALUES(?, ?, ?)")
	if err != nil {
		return err
	}
	s.stmtIncrTopupCount, err = s.db.Prepare("UPDATE devices SET topup_count = topup_count + 1, topup_at = CURRENT_TIMESTAMP WHERE id = ?")
	if err != nil {
		return err
	}
	s.stmtAddShareTx, err = s.db.Prepare("INSERT INTO sharetx(device_id, token) VALUES(?, ?)")
	if err != nil {
		return err
	}
	return nil
}

func (s *Store) Close() error {
	// Close prepared statements
	if s.stmtGetDeviceIDByIP != nil {
		s.stmtGetDeviceIDByIP.Close()
	}
	if s.stmtGetDeviceIDByMAC != nil {
		s.stmtGetDeviceIDByMAC.Close()
	}
	if s.stmtGetDeviceMAC != nil {
		s.stmtGetDeviceMAC.Close()
	}
	if s.stmtGetDataUsage != nil {
		s.stmtGetDataUsage.Close()
	}
	if s.stmtUpdateMBUsed != nil {
		s.stmtUpdateMBUsed.Close()
	}
	if s.stmtGetTopupCount != nil {
		s.stmtGetTopupCount.Close()
	}
	if s.stmtUpsertDevice != nil {
		s.stmtUpsertDevice.Close()
	}
	if s.stmtAddSession != nil {
		s.stmtAddSession.Close()
	}
	if s.stmtIncrTopupCount != nil {
		s.stmtIncrTopupCount.Close()
	}
	if s.stmtAddShareTx != nil {
		s.stmtAddShareTx.Close()
	}
	return s.db.Close()
}

func (s *Store) createTables() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS devices (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			mac_addr TEXT NOT NULL UNIQUE,
			ip_addr TEXT DEFAULT '127.0.0.1',
			hostname TEXT DEFAULT '-NA-',
			topup_count INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			topup_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS session (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			device_id INTEGER NOT NULL,
			piso_count REAL DEFAULT 0,
			mb_limit REAL DEFAULT 0,
			mb_used REAL DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (device_id) REFERENCES devices(id)
		)`,
		`CREATE TABLE IF NOT EXISTS sharetx (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			device_id INTEGER NOT NULL,
			token TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_session_device ON session(device_id)`,
		`CREATE INDEX IF NOT EXISTS idx_session_device_status ON session(device_id, mb_limit, mb_used)`,
		`CREATE INDEX IF NOT EXISTS idx_sharetx_token ON sharetx(token)`,
	}

	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("create table: %w", err)
		}
	}
	return nil
}

// --- Device operations ---

func (s *Store) UpsertDevice(mac, ip, hostname string) (int64, error) {
	res, err := s.stmtUpsertDevice.Exec(mac, ip, hostname)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) GetDeviceIDByMAC(mac string) (int64, error) {
	var id int64
	err := s.stmtGetDeviceIDByMAC.QueryRow(mac).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}

func (s *Store) GetDeviceIDByIP(ip string) (int64, error) {
	var id int64
	err := s.stmtGetDeviceIDByIP.QueryRow(ip).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}

func (s *Store) GetDeviceMAC(deviceID int64) (string, error) {
	var mac string
	err := s.stmtGetDeviceMAC.QueryRow(deviceID).Scan(&mac)
	return mac, err
}

func (s *Store) GetDeviceIP(deviceID int64) (string, error) {
	var ip string
	err := s.db.QueryRow("SELECT ip_addr FROM devices WHERE id = ?", deviceID).Scan(&ip)
	return ip, err
}

func (s *Store) GetDeviceInfo(deviceID int64) (*Device, error) {
	d := &Device{}
	err := s.db.QueryRow(
		"SELECT mac_addr, IFNULL(ip_addr,'-NA-'), IFNULL(hostname,'-NA-') FROM devices WHERE id = ?",
		deviceID,
	).Scan(&d.MAC, &d.IP, &d.Hostname)
	if err != nil {
		return nil, err
	}
	d.ID = deviceID
	return d, nil
}

// --- Data usage ---

func (s *Store) GetDataUsage(deviceID int64) (DataUsage, error) {
	var du DataUsage
	var limit, used sql.NullFloat64
	err := s.stmtGetDataUsage.QueryRow(deviceID).Scan(&limit, &used)
	if err != nil {
		return du, err
	}
	du.MBLimit = limit.Float64
	du.MBUsed = used.Float64
	return du, nil
}

func (s *Store) UpdateMBUsed(deviceID int64, mb float64) error {
	_, err := s.stmtUpdateMBUsed.Exec(mb, deviceID)
	return err
}

// --- Session (topup) operations ---

func (s *Store) AddSession(deviceID int64, amount, mbLimit float64) (int64, error) {
	res, err := s.stmtAddSession.Exec(deviceID, amount, mbLimit)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// HasFreeSessionSince reports whether a device already has a ₱0 session
// (admin grant or maintenance-mode free data) created at or after the
// given unix-seconds timestamp. Used to cap maintenance mode's free-data
// grant to once per activation window rather than handing it out on every
// reconnect.
func (s *Store) HasFreeSessionSince(deviceID int64, sinceUnixSec int64) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM session
		 WHERE device_id = ? AND piso_count = 0 AND created_at >= datetime(?, 'unixepoch')`,
		deviceID, sinceUnixSec,
	).Scan(&count)
	return count > 0, err
}

func (s *Store) RemoveSession(sessionID int64) error {
	_, err := s.db.Exec("DELETE FROM session WHERE id = ?", sessionID)
	return err
}

func (s *Store) ClearSessions(deviceID int64) error {
	_, err := s.db.Exec("DELETE FROM session WHERE device_id = ?", deviceID)
	return err
}

func (s *Store) GetDeviceSessions(deviceID int64) ([]Session, error) {
	rows, err := s.db.Query(
		`SELECT id, piso_count, mb_limit, mb_used,
		        CAST(strftime('%s', created_at) AS INTEGER) * 1000,
		        CAST(strftime('%s', updated_at) AS INTEGER) * 1000
		 FROM session WHERE device_id = ? ORDER BY id DESC`, deviceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		var sess Session
		if err := rows.Scan(&sess.ID, &sess.Amount, &sess.MBLimit, &sess.MBUsed, &sess.CreatedAt, &sess.UpdatedAt); err != nil {
			return nil, err
		}
		sessions = append(sessions, sess)
	}
	return sessions, nil
}

// --- Topup count (rate limit) ---

func (s *Store) GetTopupCount(deviceID int64) (int, error) {
	var count int
	err := s.stmtGetTopupCount.QueryRow(deviceID).Scan(&count)
	return count, err
}

func (s *Store) IncrTopupCount(deviceID int64) error {
	_, err := s.stmtIncrTopupCount.Exec(deviceID)
	return err
}

func (s *Store) ResetTopupCount(deviceID int64) error {
	_, err := s.db.Exec("UPDATE devices SET topup_count = 0 WHERE id = ?", deviceID)
	return err
}

// --- Device listings ---

func (s *Store) GetAllDevices() ([]Device, error) {
	rows, err := s.db.Query(
		`SELECT id, mac_addr, IFNULL(ip_addr,'-NA-'), IFNULL(hostname,'-NA-'),
		        CAST(strftime('%s', updated_at) AS INTEGER) * 1000
		 FROM devices WHERE mac_addr != '' ORDER BY updated_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var devs []Device
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.ID, &d.MAC, &d.IP, &d.Hostname, &d.UpdatedAt); err != nil {
			return nil, err
		}
		devs = append(devs, d)
	}
	return devs, nil
}

func (s *Store) GetActiveDevices() ([]Device, error) {
	rows, err := s.db.Query(
		`SELECT d.id, d.mac_addr, d.ip_addr, IFNULL(d.hostname,'-NA-'),
		        CAST(strftime('%s', MAX(s.updated_at)) AS INTEGER) * 1000
		 FROM session s JOIN devices d ON d.id = s.device_id
		 WHERE s.updated_at > DATETIME(CURRENT_TIMESTAMP, '-1 minute')
		 GROUP BY d.id
		 ORDER BY MAX(s.updated_at) DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var devs []Device
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.ID, &d.MAC, &d.IP, &d.Hostname, &d.UpdatedAt); err != nil {
			return nil, err
		}
		devs = append(devs, d)
	}
	return devs, nil
}

func (s *Store) GetRecentDevices() ([]Device, error) {
	rows, err := s.db.Query(
		`SELECT id, mac_addr, IFNULL(ip_addr,'-NA-'), IFNULL(hostname,'-NA-'),
		        CAST(strftime('%s', updated_at) AS INTEGER) * 1000
		 FROM devices WHERE updated_at > DATETIME(CURRENT_TIMESTAMP, '-4 hours')
		 ORDER BY updated_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var devs []Device
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.ID, &d.MAC, &d.IP, &d.Hostname, &d.UpdatedAt); err != nil {
			return nil, err
		}
		devs = append(devs, d)
	}
	return devs, nil
}

func (s *Store) GetRestrictedDevices() ([]Device, error) {
	rows, err := s.db.Query(
		`SELECT d.id, d.mac_addr, d.ip_addr, IFNULL(d.hostname,'-NA-'),
		        CAST(strftime('%s', s.updated_at) AS INTEGER) * 1000
		 FROM devices d
		 JOIN (SELECT device_id, MAX(updated_at) AS updated_at,
		              SUM(mb_limit) - SUM(mb_used) AS mb_free
		       FROM session GROUP BY device_id HAVING mb_free <= 0) s
		 ON s.device_id = d.id
		 ORDER BY s.updated_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var devs []Device
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.ID, &d.MAC, &d.IP, &d.Hostname, &d.UpdatedAt); err != nil {
			return nil, err
		}
		devs = append(devs, d)
	}
	return devs, nil
}

// --- Transactions ---

func (s *Store) GetAllTransactions(offset, limit int) ([]Transaction, error) {
	rows, err := s.db.Query(
		`SELECT s.piso_count, s.mb_limit,
		        CAST(strftime('%s', s.created_at) AS INTEGER) * 1000,
		        IFNULL(d.mac_addr,'-NA-'), IFNULL(d.ip_addr,'-NA-'), IFNULL(d.hostname,'-NA-')
		 FROM session s LEFT JOIN devices d ON s.device_id = d.id
		 ORDER BY s.id DESC LIMIT ? OFFSET ?`, int64(limit), int64(offset),
	)
	if err != nil {
		return nil, fmt.Errorf("query error (offset=%d, limit=%d): %w", offset, limit, err)
	}
	defer rows.Close()

	var txns []Transaction
	for rows.Next() {
		var t Transaction
		if err := rows.Scan(&t.Amount, &t.MBLimit, &t.TS, &t.MAC, &t.IP, &t.Hostname); err != nil {
			return nil, fmt.Errorf("scan error: %w", err)
		}
		txns = append(txns, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration error: %w", err)
	}
	return txns, nil
}

// --- Share transaction ---

func (s *Store) AddShareTx(deviceID int64, token string) error {
	_, err := s.stmtAddShareTx.Exec(deviceID, token)
	return err
}

func (s *Store) GetShareTxDeviceID(token string) (int64, error) {
	var did int64
	err := s.db.QueryRow(
		"SELECT device_id FROM sharetx WHERE token = ? ORDER BY id DESC LIMIT 1", token,
	).Scan(&did)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return did, err
}

func (s *Store) CleanExpiredShareTx() error {
	_, err := s.db.Exec("DELETE FROM sharetx WHERE created_at < DATETIME(CURRENT_TIMESTAMP, '-1 minute')")
	return err
}

// --- Device identity merge (survives MAC rotation) ---

// MergeDeviceSessions moves session and share history from oldDeviceID to
// newDeviceID. Used when a device token (see internal/auth) reveals that
// the current device (identified by its new, OS-randomized MAC) is
// actually the same physical device as an earlier one — reuniting its
// paid data balance.
func (s *Store) MergeDeviceSessions(oldDeviceID, newDeviceID int64) error {
	if oldDeviceID == 0 || newDeviceID == 0 || oldDeviceID == newDeviceID {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("UPDATE session SET device_id = ? WHERE device_id = ?", newDeviceID, oldDeviceID); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE sharetx SET device_id = ? WHERE device_id = ?", newDeviceID, oldDeviceID); err != nil {
		return err
	}
	return tx.Commit()
}

// --- Active at ---

func (s *Store) GetActiveAt(deviceID int64) (int64, error) {
	var ts sql.NullInt64
	err := s.db.QueryRow(
		`SELECT CAST(strftime('%s', updated_at) AS INTEGER) * 1000
		 FROM session WHERE device_id = ? ORDER BY updated_at DESC LIMIT 1`, deviceID,
	).Scan(&ts)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return ts.Int64, err
}

// --- Earnings summary ---

func (s *Store) GetEarningsSummary() (*EarningsSummary, error) {
	es := &EarningsSummary{}
	queries := []struct {
		sql  string
		dest *float64
	}{
		{"SELECT IFNULL(SUM(piso_count),0) FROM session WHERE DATE(created_at,'+8 hours') = DATE('now','+8 hours')", &es.Day},
		{"SELECT IFNULL(SUM(piso_count),0) FROM session WHERE DATE(created_at,'+8 hours') >= DATE('now','+8 hours','weekday 0','-7 days')", &es.Week},
		{"SELECT IFNULL(SUM(piso_count),0) FROM session WHERE DATE(created_at,'+8 hours') >= DATE('now','+8 hours','start of month')", &es.Month},
		{"SELECT IFNULL(SUM(piso_count),0) FROM session WHERE DATE(created_at,'+8 hours') >= DATE('now','+8 hours','start of year')", &es.Year},
		{"SELECT IFNULL(SUM(piso_count),0) FROM session WHERE DATE(created_at,'+8 hours') = DATE('now','+8 hours','-1 day')", &es.LastDay},
		{"SELECT IFNULL(SUM(piso_count),0) FROM session WHERE DATE(created_at,'+8 hours') BETWEEN DATE('now','+8 hours','weekday 0','-14 days') AND DATE('now','weekday 0','-8 days')", &es.LastWeek},
		{"SELECT IFNULL(SUM(piso_count),0) FROM session WHERE DATE(created_at,'+8 hours') BETWEEN DATE('now','+8 hours','start of month','-1 month') AND DATE('now','start of month','-1 day')", &es.LastMonth},
		{"SELECT IFNULL(SUM(piso_count),0) FROM session WHERE DATE(created_at,'+8 hours') BETWEEN DATE('now','+8 hours','start of year','-1 year') AND DATE('now','start of year','-1 day')", &es.LastYear},
	}

	for _, q := range queries {
		if err := s.db.QueryRow(q.sql).Scan(q.dest); err != nil {
			return nil, err
		}
	}
	return es, nil
}

// GetBandwidthSummary aggregates data usage (mb_used) by the date a session
// was last updated, mirroring GetEarningsSummary's bucketing. This is an
// approximation: unlike piso_count (fixed once at payment), mb_used grows
// over a session's lifetime, and there's no per-day usage log — so a
// session's *entire* current usage is attributed to whichever day it was
// most recently active, not spread across the days it actually accrued.
func (s *Store) GetBandwidthSummary() (*BandwidthSummary, error) {
	bs := &BandwidthSummary{}
	queries := []struct {
		sql  string
		dest *float64
	}{
		{"SELECT IFNULL(SUM(mb_used),0) FROM session WHERE DATE(updated_at,'+8 hours') = DATE('now','+8 hours')", &bs.Day},
		{"SELECT IFNULL(SUM(mb_used),0) FROM session WHERE DATE(updated_at,'+8 hours') >= DATE('now','+8 hours','weekday 0','-7 days')", &bs.Week},
		{"SELECT IFNULL(SUM(mb_used),0) FROM session WHERE DATE(updated_at,'+8 hours') >= DATE('now','+8 hours','start of month')", &bs.Month},
		{"SELECT IFNULL(SUM(mb_used),0) FROM session WHERE DATE(updated_at,'+8 hours') >= DATE('now','+8 hours','start of year')", &bs.Year},
	}

	for _, q := range queries {
		if err := s.db.QueryRow(q.sql).Scan(q.dest); err != nil {
			return nil, err
		}
	}
	return bs, nil
}

// --- All forwarded IPs (for usage poller) ---

func (s *Store) GetAllDeviceIPs() (map[string]int64, error) {
	rows, err := s.db.Query("SELECT id, ip_addr FROM devices WHERE ip_addr != '127.0.0.1'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	m := make(map[string]int64)
	for rows.Next() {
		var id int64
		var ip string
		if err := rows.Scan(&id, &ip); err != nil {
			continue
		}
		m[ip] = id
	}
	return m, nil
}

// GetRecentSessionMB returns the MB allocated in the last N seconds for a device.
func (s *Store) GetRecentSessionMB(deviceID int64, seconds int) (float64, error) {
	var mb sql.NullFloat64
	err := s.db.QueryRow(
		fmt.Sprintf("SELECT mb_limit FROM session WHERE device_id = ? AND created_at > DATETIME('now','-%d seconds') ORDER BY id DESC LIMIT 1", seconds),
		deviceID,
	).Scan(&mb)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return mb.Float64, err
}

// ForceExhaustData sets mb_used = mb_limit for a device (admin block).
func (s *Store) ForceExhaustData(deviceID int64) error {
	_, err := s.db.Exec(
		`UPDATE session SET mb_used = mb_limit WHERE device_id = ? AND mb_limit > mb_used`,
		deviceID,
	)
	return err
}

// GetActiveDeviceIDs returns device IDs that have had session activity in the last interval.
func (s *Store) GetActiveDeviceIDs() ([]int64, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT device_id FROM session
		 WHERE updated_at > DATETIME(CURRENT_TIMESTAMP, '-2 minutes')`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// --- Batched queries (reduced I/O) ---

// GetDeviceFullInfo fetches device MAC and data usage in a single query.
// Replaces separate GetDeviceMAC + GetDataUsage calls.
func (s *Store) GetDeviceFullInfo(deviceID int64) (mac string, du DataUsage, err error) {
	var limit, used sql.NullFloat64
	err = s.db.QueryRow(
		`SELECT d.mac_addr,
		        COALESCE(SUM(sess.mb_limit), 0),
		        COALESCE(SUM(sess.mb_used), 0)
		 FROM devices d
		 LEFT JOIN session sess ON d.id = sess.device_id
		 WHERE d.id = ?
		 GROUP BY d.id`,
		deviceID,
	).Scan(&mac, &limit, &used)
	if err != nil {
		return "", du, err
	}
	du.MBLimit = limit.Float64
	du.MBUsed = used.Float64
	return mac, du, nil
}

// GetDeviceTopupInfo fetches MAC, topup count, and last topup time in a single query.
// Replaces separate GetDeviceMAC + GetTopupCount calls.
func (s *Store) GetDeviceTopupInfo(deviceID int64) (mac string, topupCount int, topupAt time.Time, err error) {
	err = s.db.QueryRow(
		"SELECT mac_addr, topup_count, topup_at FROM devices WHERE id = ?",
		deviceID,
	).Scan(&mac, &topupCount, &topupAt)
	if err == sql.ErrNoRows {
		return "", 0, time.Time{}, nil
	}
	return mac, topupCount, topupAt, err
}

// Utility: current time in unix ms.
func NowMS() int64 {
	return time.Now().UnixMilli()
}
