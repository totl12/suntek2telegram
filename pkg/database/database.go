package database

import (
	"database/sql"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps a SQLite connection.
type DB struct {
	db *sql.DB
}

// Trap represents a camera trap. Credentials are matched against the shared
// FTP or SMTP server; each trap routes images to its own Telegram chat.
type Trap struct {
	ID        int64
	Name      string
	Type      string // "ftp" or "smtp"
	ChatID    int64
	Username  string
	Password  string
	Enabled   bool
	CreatedAt time.Time
}

// Photo represents a received and stored photo.
type Photo struct {
	ID         int64
	TrapID     int64
	TrapName   string
	Filename   string
	ReceivedAt time.Time
	SentOK     bool
}

// Open opens (or creates) the SQLite database at the given path.
func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}
	d := &DB{db: db}
	return d, d.migrate()
}

func (d *DB) migrate() error {
	d.db.Exec(`PRAGMA journal_mode=WAL`)

	// Detect old schema (had bind_port column) and migrate if needed.
	var oldSchema bool
	var dummy interface{}
	if err := d.db.QueryRow(`SELECT bind_port FROM traps LIMIT 0`).Scan(&dummy); err == nil {
		oldSchema = true
	}

	if oldSchema {
		// Recreate traps without the server-binding columns.
		if _, err := d.db.Exec(`
			CREATE TABLE IF NOT EXISTS traps_new (
				id         INTEGER PRIMARY KEY AUTOINCREMENT,
				name       TEXT    NOT NULL,
				type       TEXT    NOT NULL,
				chat_id    INTEGER NOT NULL,
				username   TEXT    NOT NULL,
				password   TEXT    NOT NULL,
				enabled    INTEGER NOT NULL DEFAULT 1,
				created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
			)
		`); err != nil {
			return err
		}
		if _, err := d.db.Exec(`
			INSERT INTO traps_new (id, name, type, chat_id, username, password, enabled, created_at)
			SELECT id, name, type, chat_id, username, password, enabled, created_at FROM traps
		`); err != nil {
			return err
		}
		if _, err := d.db.Exec(`DROP TABLE traps`); err != nil {
			return err
		}
		if _, err := d.db.Exec(`ALTER TABLE traps_new RENAME TO traps`); err != nil {
			return err
		}
	} else {
		if _, err := d.db.Exec(`
			CREATE TABLE IF NOT EXISTS traps (
				id         INTEGER PRIMARY KEY AUTOINCREMENT,
				name       TEXT    NOT NULL,
				type       TEXT    NOT NULL,
				chat_id    INTEGER NOT NULL,
				username   TEXT    NOT NULL,
				password   TEXT    NOT NULL,
				enabled    INTEGER NOT NULL DEFAULT 1,
				created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
			)
		`); err != nil {
			return err
		}
	}

	_, err := d.db.Exec(`
		CREATE TABLE IF NOT EXISTS photos (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			trap_id     INTEGER NOT NULL,
			trap_name   TEXT    NOT NULL DEFAULT '',
			filename    TEXT    NOT NULL,
			received_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			sent_ok     INTEGER NOT NULL DEFAULT 0
		)
	`)
	if err != nil {
		return err
	}
	_, err = d.db.Exec(`CREATE INDEX IF NOT EXISTS photos_trap_id ON photos(trap_id)`)
	return err
}

// GetTraps returns all traps ordered by id.
func (d *DB) GetTraps() ([]Trap, error) {
	rows, err := d.db.Query(`
		SELECT id, name, type, chat_id, username, password, enabled, created_at
		FROM traps ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var traps []Trap
	for rows.Next() {
		var t Trap
		var enabled int
		if err := rows.Scan(&t.ID, &t.Name, &t.Type, &t.ChatID,
			&t.Username, &t.Password, &enabled, &t.CreatedAt); err != nil {
			return nil, err
		}
		t.Enabled = enabled == 1
		traps = append(traps, t)
	}
	return traps, rows.Err()
}

// GetTrap returns a single trap by id.
func (d *DB) GetTrap(id int64) (*Trap, error) {
	var t Trap
	var enabled int
	err := d.db.QueryRow(`
		SELECT id, name, type, chat_id, username, password, enabled, created_at
		FROM traps WHERE id = ?
	`, id).Scan(&t.ID, &t.Name, &t.Type, &t.ChatID,
		&t.Username, &t.Password, &enabled, &t.CreatedAt)
	if err != nil {
		return nil, err
	}
	t.Enabled = enabled == 1
	return &t, nil
}

// CreateTrap inserts a new trap and returns its id.
func (d *DB) CreateTrap(t *Trap) (int64, error) {
	res, err := d.db.Exec(`
		INSERT INTO traps (name, type, chat_id, username, password, enabled)
		VALUES (?, ?, ?, ?, ?, ?)
	`, t.Name, t.Type, t.ChatID, t.Username, t.Password, boolToInt(t.Enabled))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateTrap updates an existing trap.
func (d *DB) UpdateTrap(t *Trap) error {
	_, err := d.db.Exec(`
		UPDATE traps SET name=?, type=?, chat_id=?, username=?, password=?, enabled=?
		WHERE id=?
	`, t.Name, t.Type, t.ChatID, t.Username, t.Password, boolToInt(t.Enabled), t.ID)
	return err
}

// DeleteTrap removes a trap by id.
func (d *DB) DeleteTrap(id int64) error {
	_, err := d.db.Exec(`DELETE FROM traps WHERE id = ?`, id)
	return err
}

// AddPhoto inserts a photo record and returns its id.
func (d *DB) AddPhoto(trapID int64, trapName, filename string, sentOK bool) (int64, error) {
	res, err := d.db.Exec(`
		INSERT INTO photos (trap_id, trap_name, filename, sent_ok) VALUES (?, ?, ?, ?)
	`, trapID, trapName, filename, boolToInt(sentOK))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetPhotos returns photos newest-first with optional trap filter (trapID=0 → all).
func (d *DB) GetPhotos(trapID int64, limit, offset int) ([]Photo, error) {
	var rows *sql.Rows
	var err error
	if trapID > 0 {
		rows, err = d.db.Query(`
			SELECT id, trap_id, trap_name, filename, received_at, sent_ok
			FROM photos WHERE trap_id = ?
			ORDER BY id DESC LIMIT ? OFFSET ?
		`, trapID, limit, offset)
	} else {
		rows, err = d.db.Query(`
			SELECT id, trap_id, trap_name, filename, received_at, sent_ok
			FROM photos ORDER BY id DESC LIMIT ? OFFSET ?
		`, limit, offset)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var photos []Photo
	for rows.Next() {
		var p Photo
		var sentOK int
		if err := rows.Scan(&p.ID, &p.TrapID, &p.TrapName, &p.Filename, &p.ReceivedAt, &sentOK); err != nil {
			return nil, err
		}
		p.SentOK = sentOK == 1
		photos = append(photos, p)
	}
	return photos, rows.Err()
}

// CountPhotos returns the total number of photos, optionally filtered by trap.
func (d *DB) CountPhotos(trapID int64) (int, error) {
	var count int
	var err error
	if trapID > 0 {
		err = d.db.QueryRow(`SELECT COUNT(*) FROM photos WHERE trap_id = ?`, trapID).Scan(&count)
	} else {
		err = d.db.QueryRow(`SELECT COUNT(*) FROM photos`).Scan(&count)
	}
	return count, err
}

// DeletePhoto removes a photo record and returns its filename.
func (d *DB) DeletePhoto(id int64) (string, error) {
	var filename string
	if err := d.db.QueryRow(`SELECT filename FROM photos WHERE id = ?`, id).Scan(&filename); err != nil {
		return "", err
	}
	_, err := d.db.Exec(`DELETE FROM photos WHERE id = ?`, id)
	return filename, err
}

// Close closes the database connection.
func (d *DB) Close() error {
	return d.db.Close()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
