package webserver

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"suntek2telegram/pkg/database"
	"suntek2telegram/pkg/trapmanager"
)

const sessionCookie = "s2t_session"
const sessionTTL = 24 * time.Hour

// Server is the web UI and REST API server.
type Server struct {
	db        *database.DB
	mgr       *trapmanager.Manager
	photosDir string
	addr      string
	mux       *http.ServeMux
	username  string
	password  string
	sessions  sync.Map // token(string) → expiry(time.Time)
}

// New creates a new Server.
func New(db *database.DB, mgr *trapmanager.Manager, photosDir, bindHost string, bindPort int, username, password string) *Server {
	s := &Server{
		db:        db,
		mgr:       mgr,
		photosDir: photosDir,
		addr:      fmt.Sprintf("%s:%d", bindHost, bindPort),
		username:  username,
		password:  password,
	}
	s.mux = http.NewServeMux()
	s.registerRoutes()
	return s
}

func (s *Server) authEnabled() bool { return s.username != "" }

func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authEnabled() {
			h(w, r)
			return
		}
		cookie, err := r.Cookie(sessionCookie)
		if err == nil {
			if exp, ok := s.sessions.Load(cookie.Value); ok {
				if time.Now().Before(exp.(time.Time)) {
					h(w, r)
					return
				}
				s.sessions.Delete(cookie.Value)
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			jsonError(w, "unauthorized", http.StatusUnauthorized)
		} else {
			http.Redirect(w, r, "/login", http.StatusFound)
		}
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.authEnabled() {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if r.Method == http.MethodPost {
		if r.FormValue("username") == s.username && r.FormValue("password") == s.password {
			token, err := newToken()
			if err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			s.sessions.Store(token, time.Now().Add(sessionTTL))
			http.SetCookie(w, &http.Cookie{
				Name: sessionCookie, Value: token, Path: "/",
				HttpOnly: true, SameSite: http.SameSiteLaxMode,
				MaxAge: int(sessionTTL.Seconds()),
			})
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, loginHTML("Неверный логин или пароль"))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, loginHTML(""))
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		s.sessions.Delete(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusFound)
}

func newToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Start begins listening and serving.
func (s *Server) Start() {
	log.Printf("Web interface available at http://%s", s.addr)
	if err := http.ListenAndServe(s.addr, s.mux); err != nil {
		log.Fatalf("Web server error: %v", err)
	}
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("/login", s.handleLogin)
	s.mux.HandleFunc("/logout", s.handleLogout)

	s.mux.HandleFunc("/", s.auth(s.handleIndex))
	s.mux.HandleFunc("/photos/", s.auth(s.handlePhotoFile))

	s.mux.HandleFunc("/api/traps", s.auth(s.handleTraps))
	s.mux.HandleFunc("/api/traps/", s.auth(s.handleTrap))
	s.mux.HandleFunc("/api/photos", s.auth(s.handlePhotos))
	s.mux.HandleFunc("/api/photos/", s.auth(s.handlePhoto))
	s.mux.HandleFunc("/api/servers", s.auth(s.handleServers))
}

// ---- HTML UI ----------------------------------------------------------------

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, indexHTML)
}

// ---- Photo file serving -----------------------------------------------------

func (s *Server) handlePhotoFile(w http.ResponseWriter, r *http.Request) {
	filename := filepath.Base(strings.TrimPrefix(r.URL.Path, "/photos/"))
	if filename == "." || filename == "/" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(s.photosDir, filename))
}

// ---- Servers API ------------------------------------------------------------

func (s *Server) handleServers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	jsonOK(w, s.mgr.Servers())
}

// ---- Traps API --------------------------------------------------------------

func (s *Server) handleTraps(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		traps, err := s.db.GetTraps()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		type trapResponse struct {
			database.Trap
			Active bool `json:"Active"`
		}
		resp := make([]trapResponse, len(traps))
		for i, t := range traps {
			resp[i] = trapResponse{Trap: t, Active: s.mgr.Status(&traps[i]).Active}
		}
		jsonOK(w, resp)

	case http.MethodPost:
		var t database.Trap
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			jsonError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := validateTrap(&t); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.mgr.AddTrap(&t); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		jsonOK(w, t)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleTrap(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		t, err := s.db.GetTrap(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		jsonOK(w, t)

	case http.MethodPut:
		var t database.Trap
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			jsonError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		t.ID = id
		if err := validateTrap(&t); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.mgr.UpdateTrap(&t); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonOK(w, t)

	case http.MethodDelete:
		if err := s.mgr.DeleteTrap(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ---- Photos API -------------------------------------------------------------

func (s *Server) handlePhotos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	trapID, _ := strconv.ParseInt(r.URL.Query().Get("trap_id"), 10, 64)
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	const limit = 24
	offset := (page - 1) * limit

	photos, err := s.db.GetPhotos(trapID, limit, offset)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	total, err := s.db.CountPhotos(trapID)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	jsonOK(w, map[string]interface{}{
		"photos": photos,
		"total":  total,
		"page":   page,
		"pages":  (total + limit - 1) / limit,
	})
}

func (s *Server) handlePhoto(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		jsonError(w, "invalid id", http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	filename, err := s.db.DeletePhoto(id)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	os.Remove(filepath.Join(s.photosDir, filename))
	w.WriteHeader(http.StatusNoContent)
}

// ---- Helpers ----------------------------------------------------------------

func validateTrap(t *database.Trap) error {
	if t.Name == "" {
		return fmt.Errorf("name is required")
	}
	if t.Type != "ftp" && t.Type != "smtp" {
		return fmt.Errorf("type must be 'ftp' or 'smtp'")
	}
	if t.ChatID == 0 {
		return fmt.Errorf("chat_id is required")
	}
	if t.Username == "" || t.Password == "" {
		return fmt.Errorf("username and password are required")
	}
	return nil
}

func jsonOK(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// ---- Embedded HTML ----------------------------------------------------------

const indexHTML = `<!DOCTYPE html>
<html lang="ru">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Suntek2Telegram</title>
<style>
:root{--bg:#0f1117;--surface:#1a1d27;--border:#2a2d3a;--accent:#4f8ef7;--danger:#e05252;--text:#e0e0e0;--muted:#888}
*{box-sizing:border-box;margin:0;padding:0}
body{background:var(--bg);color:var(--text);font:14px/1.5 'Segoe UI',system-ui,sans-serif;min-height:100vh}
header{background:var(--surface);border-bottom:1px solid var(--border);padding:12px 24px;display:flex;align-items:center;gap:16px}
header h1{font-size:18px;font-weight:600}
nav button{background:none;border:none;color:var(--muted);cursor:pointer;font-size:14px;padding:6px 14px;border-radius:6px;transition:.15s}
nav button.active,nav button:hover{background:var(--accent);color:#fff}
main{padding:24px;max-width:1200px;margin:0 auto}
.tab{display:none}.tab.active{display:block}
.toolbar{display:flex;align-items:center;justify-content:space-between;margin-bottom:16px;flex-wrap:wrap;gap:10px}
.toolbar h2{font-size:16px}
.toolbar-right{display:flex;align-items:center;gap:10px;flex-wrap:wrap}
button.btn{border:none;cursor:pointer;padding:7px 16px;border-radius:6px;font-size:13px;font-weight:500;transition:.15s}
.btn-primary{background:var(--accent);color:#fff}.btn-primary:hover{background:#3a7ae0}
.btn-danger{background:var(--danger);color:#fff}.btn-danger:hover{background:#c44}
.btn-sm{padding:4px 10px;font-size:12px}
.btn-icon{background:none;border:none;cursor:pointer;color:var(--muted);font-size:16px;padding:4px 6px;border-radius:4px}.btn-icon:hover{color:#fff;background:var(--border)}
table{width:100%;border-collapse:collapse;background:var(--surface);border-radius:10px;overflow:hidden}
th{background:#1e2130;color:var(--muted);font-weight:500;font-size:12px;text-transform:uppercase;letter-spacing:.04em;padding:10px 14px;text-align:left}
td{padding:10px 14px;border-top:1px solid var(--border);vertical-align:middle}
tr:hover td{background:#1e2130}
.badge{display:inline-block;padding:2px 8px;border-radius:12px;font-size:11px;font-weight:600}
.badge-ftp{background:#1a3a5c;color:#5ba3f5}
.badge-smtp{background:#2a1a4a;color:#a07af5}
.badge-on{background:#1a3a1a;color:#5af576}
.badge-off{background:#3a1a1a;color:#f57676}
.badge-ok{background:#1a3a1a;color:#5af576}
.badge-fail{background:#3a1a1a;color:#f57676}
.server-bar{display:flex;gap:12px;margin-bottom:18px;flex-wrap:wrap}
.server-pill{background:var(--surface);border:1px solid var(--border);border-radius:8px;padding:8px 14px;font-size:13px;display:flex;align-items:center;gap:8px}
.photos-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(200px,1fr));gap:14px}
.photo-card{background:var(--surface);border-radius:10px;overflow:hidden;position:relative}
.photo-card img{width:100%;aspect-ratio:4/3;object-fit:cover;display:block;cursor:pointer}
.photo-card .info{padding:8px 10px;font-size:12px;color:var(--muted)}
.photo-card .info strong{color:var(--text);display:block}
.photo-card .del-btn{position:absolute;top:6px;right:6px;background:rgba(0,0,0,.6);border:none;color:#fff;width:26px;height:26px;border-radius:50%;cursor:pointer;font-size:14px;line-height:26px;text-align:center;opacity:0;transition:.15s}
.photo-card:hover .del-btn{opacity:1}
.pagination{display:flex;gap:8px;justify-content:center;margin-top:20px}
.pagination button{background:var(--surface);border:1px solid var(--border);color:var(--text);padding:5px 12px;border-radius:6px;cursor:pointer}
.pagination button.active,.pagination button:hover{background:var(--accent);border-color:var(--accent);color:#fff}
.overlay{display:none;position:fixed;inset:0;background:rgba(0,0,0,.6);z-index:100;align-items:center;justify-content:center}
.overlay.open{display:flex}
.modal{background:var(--surface);border:1px solid var(--border);border-radius:12px;padding:24px;width:440px;max-width:95vw;max-height:90vh;overflow-y:auto}
.modal h3{margin-bottom:18px;font-size:16px}
.field{margin-bottom:14px}
.field label{display:block;font-size:12px;color:var(--muted);margin-bottom:5px}
.field input,.field select{width:100%;background:#111420;border:1px solid var(--border);border-radius:6px;color:var(--text);padding:8px 10px;font-size:13px;outline:none;transition:.15s}
.field input:focus,.field select:focus{border-color:var(--accent)}
.field-row{display:grid;grid-template-columns:1fr 1fr;gap:12px}
.modal-actions{display:flex;gap:10px;justify-content:flex-end;margin-top:18px}
.lightbox{display:none;position:fixed;inset:0;background:rgba(0,0,0,.9);z-index:200;align-items:center;justify-content:center;cursor:zoom-out}
.lightbox.open{display:flex}
.lightbox img{max-width:95vw;max-height:95vh;object-fit:contain;border-radius:6px}
.empty{color:var(--muted);text-align:center;padding:48px;font-size:14px}
.status-dot{display:inline-block;width:8px;height:8px;border-radius:50%;margin-right:4px}
.dot-on{background:#5af576}.dot-off{background:#f57676}
select.filter{background:#111420;border:1px solid var(--border);border-radius:6px;color:var(--text);padding:6px 10px;font-size:13px;outline:none}
select.filter:focus{border-color:var(--accent)}
</style>
</head>
<body>
<header>
  <h1>📷 Suntek2Telegram</h1>
  <nav>
    <button class="active" onclick="showTab('traps',this)">Ловушки</button>
    <button onclick="showTab('photos',this)">Фотографии</button>
  </nav>
  <a href="/logout" style="margin-left:auto;color:var(--muted);font-size:13px;text-decoration:none;padding:6px 12px;border-radius:6px;border:1px solid var(--border)" onmouseover="this.style.color='#fff'" onmouseout="this.style.color='var(--muted)'">Выйти</a>
</header>
<main>

  <!-- TRAPS TAB -->
  <div id="tab-traps" class="tab active">
    <div id="server-bar" class="server-bar"></div>
    <div class="toolbar">
      <h2>Камеры / Ловушки</h2>
      <button class="btn btn-primary" onclick="openAdd()">+ Добавить ловушку</button>
    </div>
    <table>
      <thead><tr>
        <th>ID</th><th>Название</th><th>Тип</th><th>Логин</th><th>Telegram Chat ID</th><th>Статус</th><th>Действия</th>
      </tr></thead>
      <tbody id="traps-body"><tr><td colspan="7" class="empty">Загрузка...</td></tr></tbody>
    </table>
  </div>

  <!-- PHOTOS TAB -->
  <div id="tab-photos" class="tab">
    <div class="toolbar">
      <h2>История фотографий</h2>
      <div class="toolbar-right">
        <select class="filter" id="filter-trap" onchange="loadPhotos(1)">
          <option value="0">Все ловушки</option>
        </select>
        <span id="photos-count" style="color:var(--muted);font-size:13px"></span>
      </div>
    </div>
    <div id="photos-grid" class="photos-grid"></div>
    <div id="pagination" class="pagination"></div>
  </div>
</main>

<!-- Add/Edit Modal -->
<div id="modal-overlay" class="overlay" onclick="closeModal(event)">
  <div class="modal" onclick="event.stopPropagation()">
    <h3 id="modal-title">Добавить ловушку</h3>
    <div class="field">
      <label>Название</label>
      <input id="f-name" type="text" placeholder="Например: Передний вход">
    </div>
    <div class="field-row">
      <div class="field">
        <label>Тип подключения</label>
        <select id="f-type">
          <option value="ftp">FTP</option>
          <option value="smtp">SMTP</option>
        </select>
      </div>
      <div class="field">
        <label>Telegram Chat ID</label>
        <input id="f-chat-id" type="number" placeholder="-100123456789">
      </div>
    </div>
    <div class="field-row">
      <div class="field">
        <label>Логин (для камеры)</label>
        <input id="f-username" type="text" placeholder="camera1">
      </div>
      <div class="field">
        <label>Пароль (для камеры)</label>
        <input id="f-password" type="text" placeholder="secret">
      </div>
    </div>
    <div class="field">
      <label style="display:flex;align-items:center;gap:8px;cursor:pointer">
        <input id="f-enabled" type="checkbox" checked style="width:auto">
        Активна
      </label>
    </div>
    <div class="modal-actions">
      <button class="btn" onclick="closeModal()" style="background:var(--border);color:var(--text)">Отмена</button>
      <button class="btn btn-primary" onclick="saveTrap()">Сохранить</button>
    </div>
  </div>
</div>

<!-- Lightbox -->
<div id="lightbox" class="lightbox" onclick="closeLightbox()">
  <img id="lightbox-img" src="">
</div>

<script>
let editingID = null;
let currentPage = 1;
let trapList = []; // for filter dropdown

// ---- Tab management --------------------------------------------------------
function showTab(name, btn) {
  document.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
  document.querySelectorAll('nav button').forEach(b => b.classList.remove('active'));
  document.getElementById('tab-' + name).classList.add('active');
  if (btn) btn.classList.add('active');
  if (name === 'photos') { populateTrapFilter(); loadPhotos(1); }
  if (name === 'traps')  { loadServers(); loadTraps(); }
}

// ---- Server status bar -----------------------------------------------------
async function loadServers() {
  try {
    const res = await fetch('/api/servers');
    const s = await res.json();
    const bar = document.getElementById('server-bar');
    bar.innerHTML = [
      pill('FTP',  s.FTPRunning,  s.FTPErr),
      pill('SMTP', s.SMTPRunning, s.SMTPErr),
    ].join('');
  } catch {}
}
function pill(name, running, errMsg) {
  const dot = ` + "`" + `<span class="status-dot ${running ? 'dot-on' : 'dot-off'}"></span>` + "`" + `;
  const label = running ? 'работает' : (errMsg ? 'ошибка' : 'выкл');
  const title = errMsg ? ` + "`" + ` title="${esc(errMsg)}"` + "`" + ` : '';
  return ` + "`" + `<div class="server-pill"${title}>${dot}<strong>${name}</strong> — ${label}</div>` + "`" + `;
}

// ---- Traps -----------------------------------------------------------------
async function loadTraps() {
  const res = await fetch('/api/traps');
  trapList = await res.json() || [];
  const tbody = document.getElementById('traps-body');
  if (!trapList.length) {
    tbody.innerHTML = '<tr><td colspan="7" class="empty">Ловушки не настроены. Нажмите «+ Добавить ловушку»</td></tr>';
    return;
  }
  tbody.innerHTML = trapList.map(t => ` + "`" + `
    <tr>
      <td style="color:var(--muted)">${t.ID}</td>
      <td><strong>${esc(t.Name)}</strong></td>
      <td><span class="badge badge-${t.Type}">${t.Type.toUpperCase()}</span></td>
      <td>${esc(t.Username)}</td>
      <td><code style="font-size:12px">${t.ChatID}</code></td>
      <td>
        <span class="status-dot ${t.Active ? 'dot-on' : 'dot-off'}"></span>
        ${t.Active
          ? '<span class="badge badge-on">Активна</span>'
          : t.Enabled
            ? '<span class="badge badge-fail">Выкл (сервер не запущен)</span>'
            : '<span class="badge badge-off">Отключена</span>'
        }
      </td>
      <td>
        <button class="btn-icon" title="Редактировать" onclick="openEdit(${t.ID})">✏️</button>
        <button class="btn-icon" title="Удалить" onclick="deleteTrap(${t.ID}, '${esc(t.Name)}')">🗑️</button>
      </td>
    </tr>
  ` + "`" + `).join('');
}

function openAdd() {
  editingID = null;
  document.getElementById('modal-title').textContent = 'Добавить ловушку';
  document.getElementById('f-name').value = '';
  document.getElementById('f-type').value = 'ftp';
  document.getElementById('f-chat-id').value = '';
  document.getElementById('f-username').value = '';
  document.getElementById('f-password').value = '';
  document.getElementById('f-enabled').checked = true;
  document.getElementById('modal-overlay').classList.add('open');
  document.getElementById('f-name').focus();
}

async function openEdit(id) {
  const res = await fetch('/api/traps/' + id);
  if (!res.ok) { alert('Ошибка загрузки'); return; }
  const t = await res.json();
  editingID = id;
  document.getElementById('modal-title').textContent = 'Редактировать ловушку';
  document.getElementById('f-name').value = t.Name;
  document.getElementById('f-type').value = t.Type;
  document.getElementById('f-chat-id').value = t.ChatID;
  document.getElementById('f-username').value = t.Username;
  document.getElementById('f-password').value = t.Password;
  document.getElementById('f-enabled').checked = t.Enabled;
  document.getElementById('modal-overlay').classList.add('open');
}

async function saveTrap() {
  const body = {
    Name:    document.getElementById('f-name').value.trim(),
    Type:    document.getElementById('f-type').value,
    ChatID:  parseInt(document.getElementById('f-chat-id').value) || 0,
    Username:document.getElementById('f-username').value.trim(),
    Password:document.getElementById('f-password').value,
    Enabled: document.getElementById('f-enabled').checked,
  };
  const url    = editingID ? '/api/traps/' + editingID : '/api/traps';
  const method = editingID ? 'PUT' : 'POST';
  const res = await fetch(url, {method, headers:{'Content-Type':'application/json'}, body: JSON.stringify(body)});
  const data = await res.json();
  if (!res.ok) { alert('Ошибка: ' + (data.error || res.statusText)); return; }
  closeModal();
  loadTraps();
}

async function deleteTrap(id, name) {
  if (!confirm('Удалить ловушку «' + name + '»?')) return;
  const res = await fetch('/api/traps/' + id, {method: 'DELETE'});
  if (!res.ok) { const d = await res.json(); alert('Ошибка: ' + d.error); return; }
  loadTraps();
}

function closeModal(e) {
  if (e && e.target !== document.getElementById('modal-overlay')) return;
  document.getElementById('modal-overlay').classList.remove('open');
}

// ---- Photos ----------------------------------------------------------------
function populateTrapFilter() {
  const sel = document.getElementById('filter-trap');
  const current = sel.value;
  // keep first "All" option, rebuild the rest
  while (sel.options.length > 1) sel.remove(1);
  (trapList || []).forEach(t => {
    const opt = document.createElement('option');
    opt.value = t.ID;
    opt.textContent = t.Name + ' (' + t.Type.toUpperCase() + ')';
    sel.appendChild(opt);
  });
  sel.value = current || '0';
}

async function loadPhotos(page) {
  currentPage = page;
  const trapID = document.getElementById('filter-trap').value || '0';
  const res = await fetch('/api/photos?page=' + page + '&trap_id=' + trapID);
  const data = await res.json();

  document.getElementById('photos-count').textContent =
    data.total > 0 ? 'Всего: ' + data.total + ' фото' : '';

  const grid = document.getElementById('photos-grid');
  if (!data.photos || data.photos.length === 0) {
    grid.innerHTML = '<p class="empty" style="grid-column:1/-1">Фотографий пока нет</p>';
    document.getElementById('pagination').innerHTML = '';
    return;
  }

  grid.innerHTML = data.photos.map(p => ` + "`" + `
    <div class="photo-card">
      <img src="/photos/${p.Filename}" loading="lazy" onclick="openLightbox('/photos/${p.Filename}')">
      <button class="del-btn" onclick="deletePhoto(${p.ID}, event)" title="Удалить">✕</button>
      <div class="info">
        <strong>${esc(p.TrapName || 'Ловушка ' + p.TrapID)}</strong>
        ${fmtDate(p.ReceivedAt)}
        <span class="badge ${p.SentOK ? 'badge-ok' : 'badge-fail'}" style="margin-left:4px">${p.SentOK ? 'TG ✓' : 'TG ✗'}</span>
      </div>
    </div>
  ` + "`" + `).join('');

  const pag = document.getElementById('pagination');
  if (data.pages <= 1) { pag.innerHTML = ''; return; }
  let btns = '';
  for (let i = 1; i <= data.pages; i++) {
    btns += ` + "`" + `<button class="${i===page?'active':''}" onclick="loadPhotos(${i})">${i}</button>` + "`" + `;
  }
  pag.innerHTML = btns;
}

async function deletePhoto(id, e) {
  e.stopPropagation();
  if (!confirm('Удалить это фото?')) return;
  const res = await fetch('/api/photos/' + id, {method: 'DELETE'});
  if (res.ok) loadPhotos(currentPage);
  else { const d = await res.json(); alert('Ошибка: ' + d.error); }
}

// ---- Lightbox --------------------------------------------------------------
function openLightbox(src) {
  document.getElementById('lightbox-img').src = src;
  document.getElementById('lightbox').classList.add('open');
}
function closeLightbox() { document.getElementById('lightbox').classList.remove('open'); }

// ---- Utilities -------------------------------------------------------------
function esc(s) {
  return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}
function fmtDate(s) {
  try { return new Date(s).toLocaleString('ru-RU'); } catch { return s; }
}

// Initial load
loadServers();
loadTraps();
</script>
</body>
</html>`

func loginHTML(errMsg string) string {
	errBlock := ""
	if errMsg != "" {
		errBlock = `<div style="background:#3a1a1a;color:#f57676;padding:10px 14px;border-radius:8px;font-size:13px;margin-bottom:16px">` + errMsg + `</div>`
	}
	return `<!DOCTYPE html>
<html lang="ru">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Вход — Suntek2Telegram</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{background:#0f1117;color:#e0e0e0;font:14px/1.5 'Segoe UI',system-ui,sans-serif;display:flex;align-items:center;justify-content:center;min-height:100vh}
.card{background:#1a1d27;border:1px solid #2a2d3a;border-radius:14px;padding:36px 32px;width:360px}
h1{font-size:20px;margin-bottom:24px;text-align:center}
label{display:block;font-size:12px;color:#888;margin-bottom:5px}
input{width:100%;background:#111420;border:1px solid #2a2d3a;border-radius:6px;color:#e0e0e0;padding:9px 12px;font-size:14px;outline:none;margin-bottom:14px}
input:focus{border-color:#4f8ef7}
button{width:100%;background:#4f8ef7;border:none;border-radius:6px;color:#fff;padding:10px;font-size:14px;font-weight:600;cursor:pointer;margin-top:4px}
button:hover{background:#3a7ae0}
</style>
</head>
<body>
<div class="card">
  <h1>📷 Suntek2Telegram</h1>
  ` + errBlock + `
  <form method="POST" action="/login">
    <label>Логин</label>
    <input name="username" type="text" autocomplete="username" autofocus>
    <label>Пароль</label>
    <input name="password" type="password" autocomplete="current-password">
    <button type="submit">Войти</button>
  </form>
</div>
</body>
</html>`
}
