package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

//go:embed web
var webFS embed.FS

const (
	sessionCookie = "fc_session"
	sessionTTL    = 7 * 24 * time.Hour
	maxUpload     = 10 << 20
)

var (
	imgRe = regexp.MustCompile(`^uploads/[a-f0-9]{32}\.(jpg|png|gif|webp)$`)
	exts  = map[string]string{
		"image/jpeg": ".jpg",
		"image/png":  ".png",
		"image/gif":  ".gif",
		"image/webp": ".webp",
	}
)

type App struct {
	db        *sql.DB
	dataDir   string
	uploadDir string
	secure    bool

	mu    sync.Mutex
	fails map[string]*failInfo
}

type failInfo struct {
	count int
	until time.Time
}

type Topic struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	CardCount   int    `json:"card_count"`
}

type Card struct {
	ID        int64  `json:"id"`
	TopicID   int64  `json:"topic_id"`
	TopicName string `json:"topic_name"`
	Image     string `json:"image"`
	Back      string `json:"back"`
	Keywords  string `json:"keywords"`
}

type cardReq struct {
	TopicID  int64  `json:"topic_id"`
	Image    string `json:"image"`
	Back     string `json:"back"`
	Keywords string `json:"keywords"`
}

type topicReq struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ---------------------------------------------------------------- main

func main() {
	port := env("PORT", "3090")
	dataDir := env("DATA_DIR", "./data")
	uploadDir := filepath.Join(dataDir, "uploads")
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		log.Fatalf("không tạo được thư mục dữ liệu: %v", err)
	}

	db, err := openDB(filepath.Join(dataDir, "flashcards.db"))
	if err != nil {
		log.Fatalf("không mở được database: %v", err)
	}

	app := &App{
		db:        db,
		dataDir:   dataDir,
		uploadDir: uploadDir,
		secure:    os.Getenv("SECURE_COOKIE") == "true",
		fails:     map[string]*failInfo{},
	}
	if err := app.migrate(); err != nil {
		log.Fatalf("lỗi tạo bảng: %v", err)
	}
	firstRun, err := app.ensureAdmin()
	if err != nil {
		log.Fatalf("lỗi tạo tài khoản admin: %v", err)
	}
	if firstRun {
		app.seedTopics()
	}
	go app.janitor()

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           app.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		log.Printf("Flashcard đang chạy tại cổng %s (dữ liệu: %s)", port, dataDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	<-ctx.Done()
	log.Println("đang tắt...")
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	_ = db.Close()
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		log.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------- database

func openDB(path string) (*sql.DB, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Một kết nối duy nhất: đơn giản, an toàn cho SQLite trên Raspberry Pi.
	db.SetMaxOpenConns(1)
	return db, db.Ping()
}

func (a *App) migrate() error {
	_, err := a.db.Exec(`
CREATE TABLE IF NOT EXISTS users (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  username      TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  role          TEXT NOT NULL DEFAULT 'admin',
  created_at    TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS sessions (
  token      TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS topics (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  name        TEXT NOT NULL UNIQUE COLLATE NOCASE,
  description TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS flashcards (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  topic_id     INTEGER NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
  image_path   TEXT NOT NULL,
  back_content TEXT NOT NULL,
  keywords     TEXT NOT NULL DEFAULT '',
  created_at   TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at   TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_flashcards_topic ON flashcards(topic_id);
`)
	return err
}

// ensureAdmin tạo tài khoản admin lần đầu. Trả về true nếu vừa tạo mới.
func (a *App) ensureAdmin() (bool, error) {
	user := env("ADMIN_USER", "admin")
	pass := os.Getenv("ADMIN_PASSWORD")

	var n int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		if os.Getenv("ADMIN_RESET") == "true" && pass != "" {
			hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
			if err != nil {
				return false, err
			}
			res, err := a.db.Exec(`UPDATE users SET password_hash=? WHERE username=?`, string(hash), user)
			if err != nil {
				return false, err
			}
			if c, _ := res.RowsAffected(); c > 0 {
				a.db.Exec(`DELETE FROM sessions`)
				log.Printf("Đã đặt lại mật khẩu cho tài khoản %q. Hãy đặt ADMIN_RESET=false.", user)
			} else {
				log.Printf("ADMIN_RESET: không có tài khoản %q", user)
			}
		}
		return false, nil
	}

	if pass == "" {
		pass = randomHex(6)
		log.Printf("=== Chưa đặt ADMIN_PASSWORD. Tài khoản admin: %q  mật khẩu tạm: %s ===", user, pass)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	if err != nil {
		return false, err
	}
	_, err = a.db.Exec(`INSERT INTO users(username,password_hash,role) VALUES(?,?, 'admin')`, user, string(hash))
	if err == nil {
		log.Printf("Đã tạo tài khoản admin %q", user)
	}
	return err == nil, err
}

func (a *App) seedTopics() {
	seed := [][2]string{
		{"Tiếng Anh", "Từ vựng và mẫu câu tiếng Anh"},
		{"Tiếng Trung", "Chữ Hán, phiên âm và từ vựng"},
		{"Lịch sử", "Sự kiện, nhân vật và mốc thời gian"},
		{"Khoa học", "Kiến thức khoa học tự nhiên"},
		{"Đạo Đức Kinh", "Các chương và ý chính của Đạo Đức Kinh"},
	}
	for _, t := range seed {
		a.db.Exec(`INSERT OR IGNORE INTO topics(name,description) VALUES(?,?)`, t[0], t[1])
	}
}

// janitor dọn phiên hết hạn và ảnh mồ côi (đã tải lên nhưng không dùng) mỗi giờ.
func (a *App) janitor() {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		a.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, time.Now().Unix())
		a.cleanOrphans()
		<-t.C
	}
}

func (a *App) cleanOrphans() {
	rows, err := a.db.Query(`SELECT image_path FROM flashcards`)
	if err != nil {
		return
	}
	used := map[string]bool{}
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			used[filepath.Base(p)] = true
		}
	}
	rows.Close()

	entries, err := os.ReadDir(a.uploadDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || used[e.Name()] {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > 24*time.Hour {
			os.Remove(filepath.Join(a.uploadDir, e.Name()))
		}
	}
}

// ---------------------------------------------------------------- routes

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/me", a.me)
	mux.HandleFunc("POST /api/login", a.login)
	mux.HandleFunc("POST /api/logout", a.logout)

	mux.HandleFunc("GET /api/topics", a.listTopics)
	mux.HandleFunc("GET /api/cards", a.listCards)

	mux.HandleFunc("POST /api/admin/topics", a.admin(a.createTopic))
	mux.HandleFunc("PUT /api/admin/topics/{id}", a.admin(a.updateTopic))
	mux.HandleFunc("DELETE /api/admin/topics/{id}", a.admin(a.deleteTopic))

	mux.HandleFunc("POST /api/admin/cards", a.admin(a.createCard))
	mux.HandleFunc("PUT /api/admin/cards/{id}", a.admin(a.updateCard))
	mux.HandleFunc("DELETE /api/admin/cards/{id}", a.admin(a.deleteCard))

	mux.HandleFunc("POST /api/admin/upload", a.admin(a.upload))
	mux.HandleFunc("POST /api/admin/password", a.admin(a.changePassword))
	mux.HandleFunc("GET /api/admin/backup", a.admin(a.backup))

	mux.Handle("GET /uploads/", http.StripPrefix("/uploads/", a.uploadsHandler()))

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		b, err := fs.ReadFile(sub, "admin.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(b)
	})
	static := http.FileServer(http.FS(sub))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		static.ServeHTTP(w, r)
	}))

	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; script-src 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func (a *App) uploadsHandler() http.Handler {
	fsrv := http.FileServer(http.Dir(a.uploadDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "" || strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r) // không liệt kê thư mục
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		fsrv.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------- helpers

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "Dữ liệu gửi lên không hợp lệ")
		return false
	}
	return true
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func clientIP(r *http.Request) string {
	if os.Getenv("TRUST_PROXY") == "true" {
		if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
			return strings.TrimSpace(strings.Split(xf, ",")[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---------------------------------------------------------------- auth

func (a *App) currentUser(r *http.Request) (id int64, username string, ok bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return 0, "", false
	}
	err = a.db.QueryRow(`
		SELECT u.id, u.username FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token = ? AND s.expires_at > ? AND u.role = 'admin'`,
		c.Value, time.Now().Unix()).Scan(&id, &username)
	return id, username, err == nil
}

// admin kiểm tra quyền ở backend cho mọi API quản trị.
func (a *App) admin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Header.Get("X-Requested-With") != "fetch" {
			writeErr(w, http.StatusForbidden, "Yêu cầu không hợp lệ")
			return
		}
		if _, _, ok := a.currentUser(r); !ok {
			writeErr(w, http.StatusUnauthorized, "Cần đăng nhập admin")
			return
		}
		h(w, r)
	}
}

func (a *App) me(w http.ResponseWriter, r *http.Request) {
	_, name, ok := a.currentUser(r)
	writeJSON(w, http.StatusOK, map[string]any{"admin": ok, "username": name})
}

func (a *App) locked(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	f := a.fails[ip]
	return f != nil && time.Now().Before(f.until)
}

func (a *App) recordFail(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f := a.fails[ip]
	if f == nil {
		f = &failInfo{}
		a.fails[ip] = f
	}
	f.count++
	if f.count >= 5 {
		f.until = time.Now().Add(5 * time.Minute)
		f.count = 0
	}
}

func (a *App) clearFails(ip string) {
	a.mu.Lock()
	delete(a.fails, ip)
	a.mu.Unlock()
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Requested-With") != "fetch" {
		writeErr(w, http.StatusForbidden, "Yêu cầu không hợp lệ")
		return
	}
	ip := clientIP(r)
	if a.locked(ip) {
		writeErr(w, http.StatusTooManyRequests, "Sai quá nhiều lần. Hãy thử lại sau 5 phút.")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	var id int64
	var hash string
	err := a.db.QueryRow(`SELECT id, password_hash FROM users WHERE username=? AND role='admin'`,
		strings.TrimSpace(req.Username)).Scan(&id, &hash)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)) != nil {
		a.recordFail(ip)
		writeErr(w, http.StatusUnauthorized, "Sai tên đăng nhập hoặc mật khẩu")
		return
	}
	a.clearFails(ip)

	token := randomHex(32)
	exp := time.Now().Add(sessionTTL)
	if _, err := a.db.Exec(`INSERT INTO sessions(token,user_id,expires_at) VALUES(?,?,?)`, token, id, exp.Unix()); err != nil {
		writeErr(w, http.StatusInternalServerError, "Không tạo được phiên đăng nhập")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", Expires: exp,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: a.secure,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		a.db.Exec(`DELETE FROM sessions WHERE token=?`, c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: a.secure,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) changePassword(w http.ResponseWriter, r *http.Request) {
	uid, _, _ := a.currentUser(r)
	var req struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len([]rune(req.New)) < 8 {
		writeErr(w, http.StatusBadRequest, "Mật khẩu mới cần ít nhất 8 ký tự")
		return
	}
	var hash string
	if err := a.db.QueryRow(`SELECT password_hash FROM users WHERE id=?`, uid).Scan(&hash); err != nil ||
		bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Old)) != nil {
		writeErr(w, http.StatusUnauthorized, "Mật khẩu hiện tại không đúng")
		return
	}
	nh, err := bcrypt.GenerateFromPassword([]byte(req.New), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Lỗi mã hóa mật khẩu")
		return
	}
	a.db.Exec(`UPDATE users SET password_hash=? WHERE id=?`, string(nh), uid)
	if c, err := r.Cookie(sessionCookie); err == nil {
		a.db.Exec(`DELETE FROM sessions WHERE user_id=? AND token<>?`, uid, c.Value)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------- topics

func (a *App) listTopics(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(`
		SELECT t.id, t.name, t.description, COUNT(f.id)
		FROM topics t LEFT JOIN flashcards f ON f.topic_id = t.id
		GROUP BY t.id ORDER BY t.name COLLATE NOCASE`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Lỗi đọc chủ đề")
		return
	}
	defer rows.Close()
	out := []Topic{}
	for rows.Next() {
		var t Topic
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.CardCount); err == nil {
			out = append(out, t)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func cleanTopic(t *topicReq) string {
	t.Name = strings.TrimSpace(t.Name)
	t.Description = strings.TrimSpace(t.Description)
	if t.Name == "" {
		return "Tên chủ đề không được để trống"
	}
	if len([]rune(t.Name)) > 100 {
		return "Tên chủ đề tối đa 100 ký tự"
	}
	if len([]rune(t.Description)) > 500 {
		return "Mô tả tối đa 500 ký tự"
	}
	return ""
}

func (a *App) createTopic(w http.ResponseWriter, r *http.Request) {
	var req topicReq
	if !readJSON(w, r, &req) {
		return
	}
	if msg := cleanTopic(&req); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	res, err := a.db.Exec(`INSERT INTO topics(name,description) VALUES(?,?)`, req.Name, req.Description)
	if isUnique(err) {
		writeErr(w, http.StatusConflict, "Tên chủ đề đã tồn tại")
		return
	} else if err != nil {
		writeErr(w, http.StatusInternalServerError, "Không lưu được chủ đề")
		return
	}
	id, _ := res.LastInsertId()
	writeJSON(w, http.StatusCreated, Topic{ID: id, Name: req.Name, Description: req.Description})
}

func (a *App) updateTopic(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "ID không hợp lệ")
		return
	}
	var req topicReq
	if !readJSON(w, r, &req) {
		return
	}
	if msg := cleanTopic(&req); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	res, err := a.db.Exec(`UPDATE topics SET name=?, description=? WHERE id=?`, req.Name, req.Description, id)
	if isUnique(err) {
		writeErr(w, http.StatusConflict, "Tên chủ đề đã tồn tại")
		return
	} else if err != nil {
		writeErr(w, http.StatusInternalServerError, "Không lưu được chủ đề")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusNotFound, "Không tìm thấy chủ đề")
		return
	}
	writeJSON(w, http.StatusOK, Topic{ID: id, Name: req.Name, Description: req.Description})
}

func (a *App) deleteTopic(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "ID không hợp lệ")
		return
	}
	// Lấy danh sách ảnh trước khi xóa (flashcard bị xóa theo chủ đề).
	rows, err := a.db.Query(`SELECT image_path FROM flashcards WHERE topic_id=?`, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Lỗi đọc dữ liệu")
		return
	}
	var imgs []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			imgs = append(imgs, p)
		}
	}
	rows.Close()

	res, err := a.db.Exec(`DELETE FROM topics WHERE id=?`, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Không xóa được chủ đề")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusNotFound, "Không tìm thấy chủ đề")
		return
	}
	for _, p := range imgs {
		a.removeImageIfUnused(p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------- cards

func (a *App) queryCards(where string, args ...any) ([]Card, error) {
	rows, err := a.db.Query(`
		SELECT f.id, f.topic_id, t.name, f.image_path, f.back_content, f.keywords
		FROM flashcards f JOIN topics t ON t.id = f.topic_id `+where+` ORDER BY f.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Card{}
	for rows.Next() {
		var c Card
		var img string
		if err := rows.Scan(&c.ID, &c.TopicID, &c.TopicName, &img, &c.Back, &c.Keywords); err != nil {
			return nil, err
		}
		c.Image = "/" + img
		out = append(out, c)
	}
	return out, rows.Err()
}

func (a *App) listCards(w http.ResponseWriter, r *http.Request) {
	where := ""
	var args []any
	if t := r.URL.Query().Get("topic"); t != "" {
		id, err := strconv.ParseInt(t, 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "Chủ đề không hợp lệ")
			return
		}
		where = "WHERE f.topic_id = ?"
		args = append(args, id)
	}
	cards, err := a.queryCards(where, args...)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Lỗi đọc flashcard")
		return
	}
	// Tìm kiếm theo từ khóa (không phân biệt hoa/thường, hỗ trợ Unicode).
	if q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q"))); q != "" {
		filtered := cards[:0]
		for _, c := range cards {
			hay := strings.ToLower(c.Back + " " + c.Keywords + " " + c.TopicName)
			if strings.Contains(hay, q) {
				filtered = append(filtered, c)
			}
		}
		cards = filtered
	}
	writeJSON(w, http.StatusOK, cards)
}

func (a *App) validateCard(c *cardReq) string {
	c.Back = strings.TrimSpace(c.Back)
	c.Keywords = strings.TrimSpace(c.Keywords)
	if c.TopicID <= 0 {
		return "Hãy chọn chủ đề"
	}
	var n int
	a.db.QueryRow(`SELECT COUNT(*) FROM topics WHERE id=?`, c.TopicID).Scan(&n)
	if n == 0 {
		return "Chủ đề không tồn tại"
	}
	if !imgRe.MatchString(c.Image) {
		return "Hãy tải hình ảnh cho mặt trước"
	}
	if _, err := os.Stat(filepath.Join(a.dataDir, filepath.FromSlash(c.Image))); err != nil {
		return "Không tìm thấy hình ảnh đã tải, hãy tải lại"
	}
	if c.Back == "" {
		return "Nội dung mặt sau không được để trống"
	}
	if len([]rune(c.Back)) > 5000 {
		return "Nội dung mặt sau tối đa 5000 ký tự"
	}
	if len([]rune(c.Keywords)) > 500 {
		return "Từ khóa/ghi chú tối đa 500 ký tự"
	}
	return ""
}

func (a *App) removeImageIfUnused(path string) {
	if !imgRe.MatchString(path) {
		return
	}
	var n int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM flashcards WHERE image_path=?`, path).Scan(&n); err == nil && n == 0 {
		os.Remove(filepath.Join(a.dataDir, filepath.FromSlash(path)))
	}
}

func (a *App) createCard(w http.ResponseWriter, r *http.Request) {
	var req cardReq
	if !readJSON(w, r, &req) {
		return
	}
	if msg := a.validateCard(&req); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	res, err := a.db.Exec(`INSERT INTO flashcards(topic_id,image_path,back_content,keywords) VALUES(?,?,?,?)`,
		req.TopicID, req.Image, req.Back, req.Keywords)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Không lưu được flashcard")
		return
	}
	id, _ := res.LastInsertId()
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (a *App) updateCard(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "ID không hợp lệ")
		return
	}
	var req cardReq
	if !readJSON(w, r, &req) {
		return
	}
	if msg := a.validateCard(&req); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	var old string
	if err := a.db.QueryRow(`SELECT image_path FROM flashcards WHERE id=?`, id).Scan(&old); err != nil {
		writeErr(w, http.StatusNotFound, "Không tìm thấy flashcard")
		return
	}
	if _, err := a.db.Exec(`UPDATE flashcards SET topic_id=?, image_path=?, back_content=?, keywords=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		req.TopicID, req.Image, req.Back, req.Keywords, id); err != nil {
		writeErr(w, http.StatusInternalServerError, "Không lưu được flashcard")
		return
	}
	if old != req.Image {
		a.removeImageIfUnused(old)
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func (a *App) deleteCard(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "ID không hợp lệ")
		return
	}
	var img string
	if err := a.db.QueryRow(`SELECT image_path FROM flashcards WHERE id=?`, id).Scan(&img); err != nil {
		writeErr(w, http.StatusNotFound, "Không tìm thấy flashcard")
		return
	}
	if _, err := a.db.Exec(`DELETE FROM flashcards WHERE id=?`, id); err != nil {
		writeErr(w, http.StatusInternalServerError, "Không xóa được flashcard")
		return
	}
	a.removeImageIfUnused(img)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------- upload

func (a *App) upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload+(2<<20))
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "Không đọc được tệp (tối đa 10MB)")
		return
	}
	file, _, err := r.FormFile("image")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "Chưa chọn hình ảnh")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxUpload+1))
	if err != nil || len(data) == 0 {
		writeErr(w, http.StatusBadRequest, "Không đọc được hình ảnh")
		return
	}
	if len(data) > maxUpload {
		writeErr(w, http.StatusRequestEntityTooLarge, "Hình ảnh tối đa 10MB")
		return
	}
	ext, ok := exts[http.DetectContentType(data)]
	if !ok {
		writeErr(w, http.StatusBadRequest, "Chỉ hỗ trợ ảnh JPG, PNG, GIF hoặc WebP")
		return
	}
	name := randomHex(16) + ext
	if err := os.WriteFile(filepath.Join(a.uploadDir, name), data, 0o644); err != nil {
		writeErr(w, http.StatusInternalServerError, "Không lưu được hình ảnh")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": "uploads/" + name, "url": "/uploads/" + name})
}

// ---------------------------------------------------------------- backup

func (a *App) backup(w http.ResponseWriter, r *http.Request) {
	tmp := filepath.Join(a.dataDir, fmt.Sprintf(".backup-%d.db", time.Now().UnixNano()))
	defer os.Remove(tmp)
	if _, err := a.db.Exec("VACUUM INTO '" + strings.ReplaceAll(tmp, "'", "''") + "'"); err != nil {
		writeErr(w, http.StatusInternalServerError, "Không tạo được bản sao lưu database")
		return
	}

	name := "flashcard-backup-" + time.Now().Format("20060102-150405") + ".tar.gz"
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	if err := addToTar(tw, tmp, "flashcards.db"); err != nil {
		log.Printf("backup: %v", err)
		return
	}
	entries, _ := os.ReadDir(a.uploadDir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := addToTar(tw, filepath.Join(a.uploadDir, e.Name()), "uploads/"+e.Name()); err != nil {
			log.Printf("backup: %v", err)
		}
	}
	tw.Close()
	gz.Close()
}

func addToTar(tw *tar.Writer, path, name string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: st.Size(), ModTime: st.ModTime()}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}
