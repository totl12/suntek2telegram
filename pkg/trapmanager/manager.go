package trapmanager

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	tb "gopkg.in/tucnak/telebot.v2"

	"suntek2telegram/pkg/config"
	"suntek2telegram/pkg/database"
	"suntek2telegram/pkg/events"
	"suntek2telegram/pkg/ftpserver"
	"suntek2telegram/pkg/smtpserver"
)

// Manager runs a single shared FTP server and a single shared SMTP server.
// Traps are identified by their credentials; each routes images to its own
// Telegram chat.
type Manager struct {
	db        *database.DB
	bot       *tb.Bot
	photosDir string
	ftpCfg    *config.FTPServer
	smtpCfg   *config.SMTPServer

	eventChan chan events.ImageEvent

	// Credential maps: username → trap. Protected by mu.
	ftpTraps  map[string]*database.Trap
	smtpTraps map[string]*database.Trap
	mu        sync.RWMutex

	ftpRunning  bool
	smtpRunning bool
	ftpErr      string
	smtpErr     string
}

// ServerStatus holds the state of the shared servers.
type ServerStatus struct {
	FTPRunning  bool
	SMTPRunning bool
	FTPErr      string
	SMTPErr     string
}

// TrapStatus holds the runtime state of a single trap.
type TrapStatus struct {
	Active bool // trap is enabled and its server is running
}

// New creates a new Manager.
func New(db *database.DB, bot *tb.Bot, photosDir string, ftpCfg *config.FTPServer, smtpCfg *config.SMTPServer) *Manager {
	return &Manager{
		db:        db,
		bot:       bot,
		photosDir: photosDir,
		ftpCfg:    ftpCfg,
		smtpCfg:   smtpCfg,
		eventChan: make(chan events.ImageEvent, 20),
		ftpTraps:  make(map[string]*database.Trap),
		smtpTraps: make(map[string]*database.Trap),
	}
}

// Start loads all enabled traps and starts the shared servers.
func (m *Manager) Start() error {
	go m.processImages()

	traps, err := m.db.GetTraps()
	if err != nil {
		return fmt.Errorf("loading traps: %w", err)
	}

	m.mu.Lock()
	for i := range traps {
		if traps[i].Enabled {
			m.registerLocked(&traps[i])
		}
	}
	m.mu.Unlock()

	m.startServers()
	return nil
}

// Shutdown stops image processing.
func (m *Manager) Shutdown() {
	close(m.eventChan)
}

// Servers returns the running state of the shared FTP and SMTP servers.
func (m *Manager) Servers() ServerStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return ServerStatus{
		FTPRunning:  m.ftpRunning,
		SMTPRunning: m.smtpRunning,
		FTPErr:      m.ftpErr,
		SMTPErr:     m.smtpErr,
	}
}

// Status returns the runtime status of a single trap.
func (m *Manager) Status(t *database.Trap) TrapStatus {
	if !t.Enabled {
		return TrapStatus{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	switch t.Type {
	case "ftp":
		return TrapStatus{Active: m.ftpRunning}
	case "smtp":
		return TrapStatus{Active: m.smtpRunning}
	}
	return TrapStatus{}
}

// AddTrap saves a new trap and registers its credentials.
func (m *Manager) AddTrap(t *database.Trap) error {
	id, err := m.db.CreateTrap(t)
	if err != nil {
		return err
	}
	t.ID = id
	if t.Enabled {
		m.mu.Lock()
		m.registerLocked(t)
		m.mu.Unlock()
	}
	return nil
}

// UpdateTrap re-registers the trap with updated credentials.
func (m *Manager) UpdateTrap(t *database.Trap) error {
	old, err := m.db.GetTrap(t.ID)
	if err != nil {
		return err
	}
	if err := m.db.UpdateTrap(t); err != nil {
		return err
	}
	m.mu.Lock()
	m.unregisterLocked(old)
	if t.Enabled {
		m.registerLocked(t)
	}
	m.mu.Unlock()
	return nil
}

// DeleteTrap removes a trap and its credentials.
func (m *Manager) DeleteTrap(id int64) error {
	t, err := m.db.GetTrap(id)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.unregisterLocked(t)
	m.mu.Unlock()
	return m.db.DeleteTrap(id)
}

// ---- internal ---------------------------------------------------------------

func (m *Manager) registerLocked(t *database.Trap) {
	cp := *t
	switch t.Type {
	case "ftp":
		m.ftpTraps[t.Username] = &cp
	case "smtp":
		m.smtpTraps[t.Username] = &cp
	}
}

func (m *Manager) unregisterLocked(t *database.Trap) {
	switch t.Type {
	case "ftp":
		delete(m.ftpTraps, t.Username)
	case "smtp":
		delete(m.smtpTraps, t.Username)
	}
}

func (m *Manager) ftpLookup(username, password string) *database.Trap {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.ftpTraps[username]
	if !ok || t.Password != password {
		return nil
	}
	return t
}

func (m *Manager) smtpLookup(username, password string) *database.Trap {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.smtpTraps[username]
	if !ok || t.Password != password {
		return nil
	}
	return t
}

func (m *Manager) startServers() {
	if m.ftpCfg != nil && m.ftpCfg.Enabled {
		_, err := ftpserver.Start(m.ftpCfg, ftpserver.TrapLookup{
			ByCredentials: m.ftpLookup,
		}, m.eventChan)
		m.mu.Lock()
		if err != nil {
			m.ftpErr = err.Error()
			log.Printf("Failed to start FTP server: %v", err)
		} else {
			m.ftpRunning = true
		}
		m.mu.Unlock()
	}

	if m.smtpCfg != nil && m.smtpCfg.Enabled {
		_, err := smtpserver.Start(m.smtpCfg, smtpserver.TrapLookup{
			ByCredentials: m.smtpLookup,
		}, m.eventChan)
		m.mu.Lock()
		if err != nil {
			m.smtpErr = err.Error()
			log.Printf("Failed to start SMTP server: %v", err)
		} else {
			m.smtpRunning = true
		}
		m.mu.Unlock()
	}
}

func (m *Manager) processImages() {
	for ev := range m.eventChan {
		m.handleImage(ev)
	}
}

func (m *Manager) handleImage(ev events.ImageEvent) {
	filename := fmt.Sprintf("%d_%d.jpg", ev.TrapID, time.Now().UnixNano())
	filePath := filepath.Join(m.photosDir, filename)

	if err := os.WriteFile(filePath, ev.Data, 0644); err != nil {
		log.Printf("Failed to save photo %s: %v", filename, err)
	}

	sentOK := false
	to := &tb.Chat{ID: ev.ChatID}
	photo := &tb.Photo{File: tb.FromReader(bytes.NewReader(ev.Data))}
	if _, err := m.bot.Send(to, photo); err != nil {
		log.Printf("Failed to send photo to chat %d (trap %d): %v", ev.ChatID, ev.TrapID, err)
	} else {
		sentOK = true
		log.Printf("Photo sent to chat %d (trap %d %s)", ev.ChatID, ev.TrapID, ev.TrapName)
	}

	if _, err := m.db.AddPhoto(ev.TrapID, ev.TrapName, filename, sentOK); err != nil {
		log.Printf("Failed to record photo in DB: %v", err)
	}
}
