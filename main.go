package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

// The embed directive includes all files under the static/ directory
// and makes them available in the staticFiles variable.
//
//go:embed static/*
var staticFiles embed.FS

const (
	adminFileName    = "admin.json"
	dbFileName       = "kanly.db"
	sessionCookieKey = "kanly_session"
)

var appDB *sql.DB

func main() {
	db, err := initDatabase()
	if err != nil {
		log.Fatalf("failed to initialize sqlite database: %v", err)
	}
	appDB = db
	defer appDB.Close()

	if err := migrateLegacyAdminJSON(); err != nil {
		log.Fatalf("failed to migrate legacy admin data: %v", err)
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/api/status", apiStatusHandler)
	mux.HandleFunc("/api/admin/status", adminStatusHandler)
	mux.HandleFunc("/api/admin/create", createAdminHandler)
	mux.HandleFunc("/api/admin/login", loginAdminHandler)
	createSessionEndpoints(mux)

	var staticHandler http.Handler
	if os.Getenv("KANLY_DEV") == "1" {
		log.Println("running in development mode; serving ./static directly")
		staticHandler = http.FileServer(http.Dir("static"))
	} else {
		staticRoot, err := fs.Sub(staticFiles, "static")
		if err != nil {
			log.Fatalf("failed to prepare static filesystem: %v", err)
		}
		staticHandler = http.FileServer(http.FS(staticRoot))
	}

	mux.HandleFunc("/login.html", func(w http.ResponseWriter, r *http.Request) {
		authenticated, err := sessionAuthenticated(r)
		if err != nil {
			internalServerError(w, err)
			return
		}
		if authenticated {
			http.Redirect(w, r, "/dashboard.html", http.StatusFound)
			return
		}
		staticHandler.ServeHTTP(w, r)
	})

	mux.HandleFunc("/dashboard.html", func(w http.ResponseWriter, r *http.Request) {
		authenticated, err := sessionAuthenticated(r)
		if err != nil {
			internalServerError(w, err)
			return
		}
		if !authenticated {
			http.Redirect(w, r, "/login.html", http.StatusFound)
			return
		}
		staticHandler.ServeHTTP(w, r)
	})

	mux.HandleFunc("/setup.html", func(w http.ResponseWriter, r *http.Request) {
		authenticated, err := sessionAuthenticated(r)
		if err != nil {
			internalServerError(w, err)
			return
		}
		if authenticated {
			http.Redirect(w, r, "/dashboard.html", http.StatusFound)
			return
		}

		exists, err := adminExists()
		if err != nil {
			internalServerError(w, err)
			return
		}
		if exists {
			http.Redirect(w, r, "/login.html", http.StatusFound)
			return
		}

		staticHandler.ServeHTTP(w, r)
	})

	mux.Handle("/", staticHandler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "60000"
	}
	listenAddr := ":" + port

	log.Printf("Starting Kanly control panel on %s", listenAddr)
	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}

type adminData struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
	Salt         string `json:"salt,omitempty"`
	CreatedAt    string `json:"created_at"`
}

type genericResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	Exists  bool   `json:"exists,omitempty"`
}

type initCheckResult struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Output string `json:"output"`
}

type duneCommandSpec struct {
	ID          string   `json:"id"`
	Category    string   `json:"category"`
	Label       string   `json:"label"`
	Description string   `json:"description"`
	BaseArgs    []string `json:"base_args"`
	ArgMode     string   `json:"arg_mode"`
	ArgHint     string   `json:"arg_hint,omitempty"`
}

type duneContainerStats struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Image    string `json:"image"`
	Status   string `json:"status"`
	Ports    string `json:"ports"`
	CPU      string `json:"cpu"`
	MemUsage string `json:"mem_usage"`
	MemPct   string `json:"mem_percent"`
	NetIO    string `json:"net_io"`
	BlockIO  string `json:"block_io"`
	PIDs     string `json:"pids"`
}

func apiStatusHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	payload := map[string]any{
		"status":    "ok",
		"service":   "kanly-control-panel",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"message":   "A new Go-based control panel is starting here.",
	}
	_ = json.NewEncoder(w).Encode(payload)
}

func adminStatusHandler(w http.ResponseWriter, r *http.Request) {
	exists, err := adminExists()
	if err != nil {
		internalServerError(w, err)
		return
	}

	resp := genericResponse{
		Success: true,
		Exists:  exists,
		Message: fmt.Sprintf("admin user %s", map[bool]string{true: "found", false: "missing"}[exists]),
	}
	jsonResponse(w, resp)
}

func createAdminHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		clientError(w, http.StatusMethodNotAllowed, "only POST is allowed")
		return
	}

	exists, err := adminExists()
	if err != nil {
		internalServerError(w, err)
		return
	}
	if exists {
		clientError(w, http.StatusConflict, "admin user already exists")
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		clientError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Username = normalizeString(req.Username)
	if req.Username == "" || req.Password == "" {
		clientError(w, http.StatusBadRequest, "username and password are required")
		return
	}

	hash, err := hashPassword(req.Password)
	if err != nil {
		internalServerError(w, err)
		return
	}

	admin := adminData{
		Username:     req.Username,
		PasswordHash: hash,
		Salt:         "",
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	}

	if err := saveAdmin(&admin); err != nil {
		// If two create requests race, treat the loser as a normal conflict.
		exists, checkErr := adminExists()
		if checkErr == nil && exists {
			clientError(w, http.StatusConflict, "admin user already exists")
			return
		}
		internalServerError(w, err)
		return
	}

	jsonResponse(w, genericResponse{Success: true, Message: "admin account created"})
}

func loginAdminHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		clientError(w, http.StatusMethodNotAllowed, "only POST is allowed")
		return
	}

	exists, err := adminExists()
	if err != nil {
		internalServerError(w, err)
		return
	}

	if !exists {
		clientError(w, http.StatusNotFound, "admin user not found")
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		clientError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	admin, err := loadAdmin()
	if err != nil {
		internalServerError(w, err)
		return
	}

	if normalizeString(req.Username) != normalizeString(admin.Username) {
		clientError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	if err := verifyPassword(admin, req.Password); err != nil {
		clientError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	token, err := generateSessionToken()
	if err != nil {
		internalServerError(w, err)
		return
	}

	if _, err := appDB.Exec("INSERT INTO admin_sessions (token, created_at) VALUES (?, ?)", token, time.Now().UTC().Format(time.RFC3339)); err != nil {
		internalServerError(w, err)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieKey,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   60 * 60 * 24,
	})

	jsonResponse(w, genericResponse{Success: true, Message: "login successful"})
}

func clientError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(genericResponse{Success: false, Message: message})
}

func internalServerError(w http.ResponseWriter, err error) {
	log.Printf("internal error: %v", err)
	clientError(w, http.StatusInternalServerError, "an internal error occurred")
}

func jsonResponse(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(payload)
}

func adminDataPath() string {
	if path := strings.TrimSpace(os.Getenv("KANLY_DB_PATH")); path != "" {
		return path
	}

	dir, err := os.Getwd()
	if err != nil {
		return dbFileName
	}
	return filepath.Join(dir, dbFileName)
}

func legacyAdminDataPath() string {
	if path := strings.TrimSpace(os.Getenv("KANLY_ADMIN_JSON_PATH")); path != "" {
		return path
	}

	dir, err := os.Getwd()
	if err != nil {
		return adminFileName
	}
	return filepath.Join(dir, adminFileName)
}

func initDatabase() (*sql.DB, error) {
	db, err := sql.Open("sqlite", adminDataPath())
	if err != nil {
		return nil, err
	}

	if _, err := db.Exec(`
        CREATE TABLE IF NOT EXISTS admin_users (
            id INTEGER PRIMARY KEY CHECK (id = 1),
            username TEXT NOT NULL UNIQUE,
            password_hash TEXT NOT NULL,
			salt TEXT NOT NULL DEFAULT '',
            created_at TEXT NOT NULL
        );
    `); err != nil {
		db.Close()
		return nil, err
	}

	if err := initSessionTable(db); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

func adminExists() (bool, error) {
	var exists bool
	err := appDB.QueryRow("SELECT EXISTS(SELECT 1 FROM admin_users LIMIT 1)").Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

func loadAdmin() (*adminData, error) {
	var admin adminData
	err := appDB.QueryRow(
		"SELECT username, password_hash, COALESCE(salt, ''), created_at FROM admin_users LIMIT 1",
	).Scan(&admin.Username, &admin.PasswordHash, &admin.Salt, &admin.CreatedAt)
	if err != nil {
		return nil, err
	}

	return &admin, nil
}

func saveAdmin(admin *adminData) error {
	_, err := appDB.Exec(
		`INSERT INTO admin_users (id, username, password_hash, salt, created_at) VALUES (1, ?, ?, ?, ?)`,
		admin.Username,
		admin.PasswordHash,
		admin.Salt,
		admin.CreatedAt,
	)
	return err
}

func migrateLegacyAdminJSON() error {
	exists, err := adminExists()
	if err != nil {
		return err
	}

	if exists {
		return nil
	}

	path := legacyAdminDataPath()
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()

	var legacy adminData
	if err := json.NewDecoder(file).Decode(&legacy); err != nil {
		return err
	}

	legacy.Username = normalizeString(legacy.Username)
	if legacy.Username == "" || legacy.PasswordHash == "" {
		return fmt.Errorf("legacy admin file at %s is missing required fields", path)
	}

	if legacy.CreatedAt == "" {
		legacy.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}

	if err := saveAdmin(&legacy); err != nil {
		return err
	}

	log.Printf("migrated legacy admin credentials from %s to sqlite", path)
	return nil
}

func hashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func verifyPassword(admin *adminData, password string) error {
	if strings.HasPrefix(admin.PasswordHash, "$2") {
		return bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(password))
	}

	if admin.Salt == "" {
		return bcrypt.ErrMismatchedHashAndPassword
	}

	salt, err := base64.RawStdEncoding.DecodeString(admin.Salt)
	if err != nil {
		return err
	}

	mac := hmac.New(sha256.New, salt)
	mac.Write([]byte(password))
	expectedHash := base64.RawStdEncoding.EncodeToString(mac.Sum(nil))
	if len(expectedHash) != len(admin.PasswordHash) {
		return bcrypt.ErrMismatchedHashAndPassword
	}
	if subtle.ConstantTimeCompare([]byte(expectedHash), []byte(admin.PasswordHash)) != 1 {
		return bcrypt.ErrMismatchedHashAndPassword
	}

	return nil
}

func normalizeString(value string) string {
	return strings.TrimSpace(value)
}

func createSessionEndpoints(mux *http.ServeMux) {
	mux.HandleFunc("/api/admin/logout", logoutAdminHandler)
	mux.HandleFunc("/api/auth/me", authMeHandler)
	mux.HandleFunc("/api/system/initialize", initializeSystemHandler)
	mux.HandleFunc("/api/system/update/check", systemUpdateCheckHandler)
	mux.HandleFunc("/api/system/stats", systemStatsHandler)
	mux.HandleFunc("/api/system/containers", systemContainersHandler)
	mux.HandleFunc("/api/system/database/insights", systemDatabaseInsightsHandler)
	mux.HandleFunc("/api/system/map-configs", systemMapConfigsHandler)
	mux.HandleFunc("/api/system/commands", systemCommandsHandler)
	mux.HandleFunc("/api/system/command/run", runSystemCommandHandler)
}

func initSessionTable(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS admin_sessions (
			token TEXT PRIMARY KEY,
			created_at TEXT NOT NULL
		);
	`)
	return err
}

func generateSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func authMeHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	authenticated, err := sessionAuthenticated(r)
	if err != nil {
		internalServerError(w, err)
		return
	}

	if !authenticated {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"authenticated": false})
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]any{"authenticated": true})
}

func sessionAuthenticated(r *http.Request) (bool, error) {
	cookie, err := r.Cookie(sessionCookieKey)
	if err != nil || cookie.Value == "" {
		return false, nil
	}

	var created string
	err = appDB.QueryRow("SELECT created_at FROM admin_sessions WHERE token = ?", cookie.Value).Scan(&created)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	return true, nil
}

func logoutAdminHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		clientError(w, http.StatusMethodNotAllowed, "only POST allowed")
		return
	}

	cookie, err := r.Cookie(sessionCookieKey)
	if err == nil && cookie.Value != "" {
		if _, err := appDB.Exec("DELETE FROM admin_sessions WHERE token = ?", cookie.Value); err != nil {
			internalServerError(w, err)
			return
		}
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieKey,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})

	jsonResponse(w, genericResponse{Success: true, Message: "logged out"})
}

func initializeSystemHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		clientError(w, http.StatusMethodNotAllowed, "only POST is allowed")
		return
	}

	authenticated, err := sessionAuthenticated(r)
	if err != nil {
		internalServerError(w, err)
		return
	}
	if !authenticated {
		clientError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	duneRoot, rootCheck := resolveDuneRoot()
	checks := []initCheckResult{rootCheck}

	if rootCheck.OK {
		checks = append(checks,
			runCommandCheck("docker_cli", "", "docker", "--version"),
			runCommandCheck("docker_daemon", "", "docker", "info", "--format", "{{.ServerVersion}}"),
			runCommandCheck("dune_compose_config", duneRoot, "docker", "compose", "config", "--quiet"),
			runCommandCheck("dune_doctor_script", duneRoot, "bash", "-lc", "[ -x ./runtime/scripts/doctor.sh ] && echo doctor.sh found"),
		)
	}

	allOK := true
	for _, check := range checks {
		if !check.OK {
			allOK = false
			break
		}
	}

	message := "initialization checks complete"
	if !allOK {
		message = "initialization checks completed with issues"
	}

	jsonResponse(w, map[string]any{
		"success":   allOK,
		"message":   message,
		"dune_root": duneRoot,
		"checks":    checks,
	})
}

func systemUpdateCheckHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		clientError(w, http.StatusMethodNotAllowed, "only GET is allowed")
		return
	}

	authenticated, err := sessionAuthenticated(r)
	if err != nil {
		internalServerError(w, err)
		return
	}
	if !authenticated {
		clientError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	status, err := checkGitUpdateStatus()
	if err != nil {
		clientError(w, http.StatusBadRequest, err.Error())
		return
	}

	jsonResponse(w, map[string]any{
		"success":          true,
		"message":          status["message"],
		"repo_dir":         status["repo_dir"],
		"branch":           status["branch"],
		"current_commit":   status["current_commit"],
		"remote_commit":    status["remote_commit"],
		"ahead_by":         status["ahead_by"],
		"behind_by":        status["behind_by"],
		"update_available": status["update_available"],
		"checked_at":       time.Now().UTC().Format(time.RFC3339),
	})
}

func checkGitUpdateStatus() (map[string]any, error) {
	repoDir := strings.TrimSpace(os.Getenv("KANLY_REPO_DIR"))
	if repoDir == "" {
		repoDir = "/kanly-repo"
	}

	if _, err := os.Stat(repoDir); err != nil {
		return nil, fmt.Errorf("repo path not available in container: %s", repoDir)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	runGit := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		message := strings.TrimSpace(string(out))
		if err != nil {
			if message == "" {
				message = err.Error()
			}
			return "", fmt.Errorf("%s", message)
		}
		return message, nil
	}

	insideWorkTree, err := runGit("rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(insideWorkTree) != "true" {
		if err != nil {
			return nil, fmt.Errorf("git check failed: %s", err.Error())
		}
		return nil, fmt.Errorf("path is not a git checkout: %s", repoDir)
	}

	branch, err := runGit("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("failed to detect branch: %s", err.Error())
	}
	if branch == "HEAD" {
		return nil, fmt.Errorf("detached HEAD detected; checkout a branch first")
	}

	currentCommit, err := runGit("rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("failed to read local commit: %s", err.Error())
	}

	remoteRef := "refs/heads/" + branch
	remoteLine, err := runGit("ls-remote", "--heads", "origin", remoteRef)
	if err != nil {
		return nil, fmt.Errorf("failed to query remote branch %s: %s", remoteRef, err.Error())
	}

	lineParts := strings.Fields(remoteLine)
	if len(lineParts) == 0 {
		return nil, fmt.Errorf("remote branch not found: %s", remoteRef)
	}
	remoteCommit := strings.TrimSpace(lineParts[0])

	updateAvailable := strings.TrimSpace(currentCommit) != remoteCommit
	aheadBy := 0
	behindBy := 0
	message := "Kanly is up to date with origin/" + branch
	if updateAvailable {
		message = "Update available: local and remote commits differ on origin/" + branch
	}

	return map[string]any{
		"repo_dir":         repoDir,
		"branch":           branch,
		"current_commit":   currentCommit,
		"remote_commit":    remoteCommit,
		"ahead_by":         aheadBy,
		"behind_by":        behindBy,
		"update_available": updateAvailable,
		"message":          message,
	}, nil
}

func resolveDuneRoot() (string, initCheckResult) {
	if envRoot := strings.TrimSpace(os.Getenv("KANLY_DUNE_ROOT")); envRoot != "" {
		composePath := filepath.Join(envRoot, "docker-compose.yml")
		if _, err := os.Stat(composePath); err == nil {
			return envRoot, initCheckResult{Name: "dune_root", OK: true, Output: envRoot}
		}
		return envRoot, initCheckResult{Name: "dune_root", OK: false, Output: "KANLY_DUNE_ROOT set but docker-compose.yml not found"}
	}

	candidates := []string{
		"/srv/kanly/server/dune-awakening-selfhost-docker",
		filepath.Clean(filepath.Join("..", "..", "server", "dune-awakening-selfhost-docker")),
	}

	for _, candidate := range candidates {
		composePath := filepath.Join(candidate, "docker-compose.yml")
		if _, err := os.Stat(composePath); err == nil {
			return candidate, initCheckResult{Name: "dune_root", OK: true, Output: candidate}
		}
	}

	return "", initCheckResult{
		Name:   "dune_root",
		OK:     false,
		Output: "could not locate dune docker root; set KANLY_DUNE_ROOT",
	}
}

func runCommandCheck(name, dir string, command string, args ...string) initCheckResult {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, command, args...)
	if dir != "" {
		cmd.Dir = dir
	}

	output, err := cmd.CombinedOutput()
	out := strings.TrimSpace(string(output))
	if out == "" {
		out = "ok"
	}
	if len(out) > 2000 {
		out = out[:2000] + "..."
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return initCheckResult{Name: name, OK: false, Output: "timed out"}
		}
		if out == "ok" {
			out = err.Error()
		}
		return initCheckResult{Name: name, OK: false, Output: out}
	}

	return initCheckResult{Name: name, OK: true, Output: out}
}

func systemStatsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		clientError(w, http.StatusMethodNotAllowed, "only GET is allowed")
		return
	}

	authenticated, err := sessionAuthenticated(r)
	if err != nil {
		internalServerError(w, err)
		return
	}
	if !authenticated {
		clientError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	memory, memErr := readMemoryStats()
	disk, diskErr := readDiskStats("/")
	load, loadErr := readLoadAverage()
	uptime, uptimeErr := readUptimeSeconds()

	warnings := []string{}
	if memErr != nil {
		warnings = append(warnings, "memory: "+memErr.Error())
	}
	if diskErr != nil {
		warnings = append(warnings, "disk: "+diskErr.Error())
	}
	if loadErr != nil {
		warnings = append(warnings, "load: "+loadErr.Error())
	}
	if uptimeErr != nil {
		warnings = append(warnings, "uptime: "+uptimeErr.Error())
	}

	jsonResponse(w, map[string]any{
		"success":      len(warnings) == 0,
		"timestamp":    time.Now().UTC().Format(time.RFC3339),
		"memory":       memory,
		"disk":         disk,
		"load_average": load,
		"uptime_sec":   uptime,
		"warnings":     warnings,
	})
}

func readMemoryStats() (map[string]any, error) {
	content, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return nil, err
	}

	values := map[string]uint64{}
	for _, line := range strings.Split(string(content), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		key := strings.TrimSuffix(parts[0], ":")
		v, parseErr := strconv.ParseUint(parts[1], 10, 64)
		if parseErr != nil {
			continue
		}
		values[key] = v * 1024
	}

	total := values["MemTotal"]
	available := values["MemAvailable"]
	if total == 0 {
		return nil, fmt.Errorf("MemTotal unavailable")
	}
	used := total - available
	usedPct := float64(used) / float64(total) * 100

	return map[string]any{
		"total_bytes":     total,
		"available_bytes": available,
		"used_bytes":      used,
		"used_percent":    usedPct,
	}, nil
}

func readDiskStats(path string) (map[string]any, error) {
	var fsStat syscall.Statfs_t
	if err := syscall.Statfs(path, &fsStat); err != nil {
		return nil, err
	}

	total := fsStat.Blocks * uint64(fsStat.Bsize)
	available := fsStat.Bavail * uint64(fsStat.Bsize)
	used := total - (fsStat.Bfree * uint64(fsStat.Bsize))
	usedPct := 0.0
	if total > 0 {
		usedPct = float64(used) / float64(total) * 100
	}

	return map[string]any{
		"path":            path,
		"total_bytes":     total,
		"available_bytes": available,
		"used_bytes":      used,
		"used_percent":    usedPct,
	}, nil
}

func readLoadAverage() (map[string]any, error) {
	content, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return nil, err
	}
	parts := strings.Fields(string(content))
	if len(parts) < 3 {
		return nil, fmt.Errorf("unexpected /proc/loadavg format")
	}

	one, err1 := strconv.ParseFloat(parts[0], 64)
	five, err2 := strconv.ParseFloat(parts[1], 64)
	fifteen, err3 := strconv.ParseFloat(parts[2], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return nil, fmt.Errorf("failed parsing load average")
	}

	return map[string]any{
		"one":     one,
		"five":    five,
		"fifteen": fifteen,
	}, nil
}

func readUptimeSeconds() (float64, error) {
	content, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	parts := strings.Fields(string(content))
	if len(parts) < 1 {
		return 0, fmt.Errorf("unexpected /proc/uptime format")
	}

	uptime, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0, err
	}
	return uptime, nil
}

func systemContainersHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		clientError(w, http.StatusMethodNotAllowed, "only GET is allowed")
		return
	}

	authenticated, err := sessionAuthenticated(r)
	if err != nil {
		internalServerError(w, err)
		return
	}
	if !authenticated {
		clientError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	containers, warnings := readDuneContainerStats()
	jsonResponse(w, map[string]any{
		"success":    len(warnings) == 0,
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
		"containers": containers,
		"warnings":   warnings,
	})
}

func systemDatabaseInsightsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		clientError(w, http.StatusMethodNotAllowed, "only GET is allowed")
		return
	}

	authenticated, err := sessionAuthenticated(r)
	if err != nil {
		internalServerError(w, err)
		return
	}
	if !authenticated {
		clientError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	insights, warnings := readDuneDatabaseInsights()
	jsonResponse(w, map[string]any{
		"success":               len(warnings) == 0,
		"timestamp":             time.Now().UTC().Format(time.RFC3339),
		"summary":               insights["summary"],
		"farm":                  insights["farm"],
		"players":               insights["players"],
		"top_tables":            insights["top_tables"],
		"player_related_tables": insights["player_related_tables"],
		"warnings":              warnings,
	})
}

func readDuneDatabaseInsights() (map[string]any, []string) {
	result := map[string]any{}
	warnings := []string{}

	summaryRows, err := runDunePostgresQuery(`
select
  case when to_regclass('dune.farm_state') is null then 0 else coalesce((select sum(coalesce(connected_players, 0)) from dune.farm_state), 0) end as live_players,
  case when to_regclass('dune.active_server_ids') is null then 0 else (select count(*) from dune.active_server_ids) end as active_servers,
  case when to_regclass('dune.world_partition') is null then 0 else (select count(*) from dune.world_partition) end as world_partitions,
  case when to_regclass('dune.player_state') is null then 0 else (select count(*) from dune.player_state) end as player_state_rows,
  case when to_regclass('dune.encrypted_player_state') is null then 0 else (select count(*) from dune.encrypted_player_state) end as encrypted_player_state_rows;
`)
	if err != nil {
		warnings = append(warnings, "summary: "+err.Error())
		result["summary"] = map[string]any{}
	} else {
		summary := map[string]any{}
		if len(summaryRows) > 0 && len(summaryRows[0]) >= 5 {
			summary["live_players"] = parseInt64Default(summaryRows[0][0], 0)
			summary["active_servers"] = parseInt64Default(summaryRows[0][1], 0)
			summary["world_partitions"] = parseInt64Default(summaryRows[0][2], 0)
			summary["player_state_rows"] = parseInt64Default(summaryRows[0][3], 0)
			summary["encrypted_player_state_rows"] = parseInt64Default(summaryRows[0][4], 0)
		}
		result["summary"] = summary
	}

	farmRows, err := runDunePostgresQuery(`
select coalesce(server_id, '') as server_id, coalesce(connected_players, 0)::text as connected_players
from dune.farm_state
order by connected_players desc nulls last
limit 24;
`)
	if err != nil {
		warnings = append(warnings, "farm_state: "+err.Error())
		result["farm"] = []map[string]any{}
	} else {
		farm := []map[string]any{}
		for _, row := range farmRows {
			if len(row) < 2 {
				continue
			}
			farm = append(farm, map[string]any{
				"server_id":         row[0],
				"connected_players": parseInt64Default(row[1], 0),
			})
		}
		result["farm"] = farm
	}

	playerRows, err := runDunePostgresQuery(`
select
  coalesce(character_name, '') as character_name,
  coalesce(server_id, '') as server_id,
  coalesce(player_state_id::text, '') as player_state_id,
  coalesce(online_status::text, 'Unknown') as online_status
from dune.player_state
where coalesce(character_name, '') <> ''
order by
  case
    when coalesce(online_status::text, '') = 'Online' then 0
    when coalesce(online_status::text, '') = 'Offline' then 1
    else 2
  end,
  lower(character_name),
  player_state_id desc
limit 80;
`)
	if err != nil {
		warnings = append(warnings, "players: "+err.Error())
		result["players"] = []map[string]any{}
	} else {
		players := []map[string]any{}
		for _, row := range playerRows {
			if len(row) < 4 {
				continue
			}
			players = append(players, map[string]any{
				"name":            row[0],
				"server_id":       row[1],
				"player_state_id": parseInt64Default(row[2], 0),
				"status":          row[3],
			})
		}
		result["players"] = players
	}

	topTableRows, err := runDunePostgresQuery(`
select relname as table_name, coalesce(n_live_tup, 0)::bigint as estimated_rows
from pg_stat_user_tables
where schemaname = 'dune'
order by estimated_rows desc, relname
limit 40;
`)
	if err != nil {
		warnings = append(warnings, "top_tables: "+err.Error())
		result["top_tables"] = []map[string]any{}
	} else {
		topTables := []map[string]any{}
		for _, row := range topTableRows {
			if len(row) < 2 {
				continue
			}
			topTables = append(topTables, map[string]any{
				"table":          row[0],
				"estimated_rows": parseInt64Default(row[1], 0),
			})
		}
		result["top_tables"] = topTables
	}

	relatedRows, err := runDunePostgresQuery(`
select t.table_name, coalesce(s.n_live_tup, 0)::bigint as estimated_rows
from information_schema.tables t
left join pg_stat_user_tables s on s.schemaname = t.table_schema and s.relname = t.table_name
where t.table_schema = 'dune'
  and (
    lower(t.table_name) like '%player%'
    or lower(t.table_name) like '%session%'
    or lower(t.table_name) like '%server%'
    or lower(t.table_name) like '%partition%'
    or lower(t.table_name) like '%travel%'
  )
order by t.table_name
limit 120;
`)
	if err != nil {
		warnings = append(warnings, "player_related_tables: "+err.Error())
		result["player_related_tables"] = []map[string]any{}
	} else {
		related := []map[string]any{}
		for _, row := range relatedRows {
			if len(row) < 2 {
				continue
			}
			related = append(related, map[string]any{
				"table":          row[0],
				"estimated_rows": parseInt64Default(row[1], 0),
			})
		}
		result["player_related_tables"] = related
	}

	return result, warnings
}

func runDunePostgresQuery(query string) ([][]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	defer cancel()

	cmd := exec.CommandContext(
		ctx,
		"docker", "exec", "dune-postgres",
		"psql", "-U", "postgres", "-d", "dune", "-At", "-F", "\t", "-c", query,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		if message == "" {
			message = err.Error()
		}
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("query timed out")
		}
		return nil, fmt.Errorf("%s", message)
	}

	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return [][]string{}, nil
	}

	rows := [][]string{}
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		rows = append(rows, strings.Split(line, "\t"))
	}

	return rows, nil
}

func parseInt64Default(value string, fallback int64) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

func readDuneContainerStats() ([]duneContainerStats, []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	psCmd := exec.CommandContext(ctx, "docker", "ps", "--filter", "name=dune-", "--format", "{{.ID}}\t{{.Names}}\t{{.Image}}\t{{.Status}}\t{{.Ports}}")
	psOut, err := psCmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(psOut))
		if message == "" {
			message = err.Error()
		}
		return nil, []string{"docker ps: " + message}
	}

	containers := []duneContainerStats{}
	containerNames := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(psOut)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 5)
		if len(parts) < 5 {
			continue
		}
		stat := duneContainerStats{
			ID:       strings.TrimSpace(parts[0]),
			Name:     strings.TrimSpace(parts[1]),
			Image:    strings.TrimSpace(parts[2]),
			Status:   strings.TrimSpace(parts[3]),
			Ports:    strings.TrimSpace(parts[4]),
			CPU:      "--",
			MemUsage: "--",
			MemPct:   "--",
			NetIO:    "--",
			BlockIO:  "--",
			PIDs:     "--",
		}
		containers = append(containers, stat)
		containerNames = append(containerNames, stat.Name)
	}

	if len(containers) == 0 {
		return containers, nil
	}

	statsArgs := []string{"stats", "--no-stream", "--format", "{{.Container}}\t{{.CPUPerc}}\t{{.MemUsage}}\t{{.MemPerc}}\t{{.NetIO}}\t{{.BlockIO}}\t{{.PIDs}}"}
	statsArgs = append(statsArgs, containerNames...)
	statsCmd := exec.CommandContext(ctx, "docker", statsArgs...)
	statsOut, statsErr := statsCmd.CombinedOutput()

	statsByName := map[string]duneContainerStats{}
	for _, line := range strings.Split(strings.TrimSpace(string(statsOut)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 7)
		if len(parts) < 7 {
			continue
		}
		statsByName[strings.TrimSpace(parts[0])] = duneContainerStats{
			CPU:      strings.TrimSpace(parts[1]),
			MemUsage: strings.TrimSpace(parts[2]),
			MemPct:   strings.TrimSpace(parts[3]),
			NetIO:    strings.TrimSpace(parts[4]),
			BlockIO:  strings.TrimSpace(parts[5]),
			PIDs:     strings.TrimSpace(parts[6]),
		}
	}

	warnings := []string{}
	if statsErr != nil {
		message := strings.TrimSpace(string(statsOut))
		if message == "" {
			message = statsErr.Error()
		}
		warnings = append(warnings, "docker stats: "+message)
	}

	for i := range containers {
		if stat, ok := statsByName[containers[i].Name]; ok {
			containers[i].CPU = stat.CPU
			containers[i].MemUsage = stat.MemUsage
			containers[i].MemPct = stat.MemPct
			containers[i].NetIO = stat.NetIO
			containers[i].BlockIO = stat.BlockIO
			containers[i].PIDs = stat.PIDs
		}
	}

	return containers, warnings
}

func systemMapConfigsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		clientError(w, http.StatusMethodNotAllowed, "only GET and POST are allowed")
		return
	}

	authenticated, err := sessionAuthenticated(r)
	if err != nil {
		internalServerError(w, err)
		return
	}
	if !authenticated {
		clientError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	duneRoot, rootCheck := resolveDuneRoot()
	if !rootCheck.OK {
		clientError(w, http.StatusBadRequest, rootCheck.Output)
		return
	}

	mapNames, err := listRuntimeMapNames(duneRoot)
	if err != nil {
		internalServerError(w, err)
		return
	}
	if len(mapNames) == 0 {
		clientError(w, http.StatusNotFound, "no map directories found")
		return
	}

	selectedMap := strings.TrimSpace(r.URL.Query().Get("map"))
	if selectedMap == "" {
		selectedMap = mapNames[0]
	}

	if r.Method == http.MethodPost {
		var req struct {
			Map           string `json:"map"`
			UserGameINI   string `json:"user_game_ini"`
			UserEngineINI string `json:"user_engine_ini"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			clientError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		targetMap := strings.TrimSpace(req.Map)
		if targetMap == "" {
			clientError(w, http.StatusBadRequest, "map is required")
			return
		}

		if !containsString(mapNames, targetMap) {
			clientError(w, http.StatusBadRequest, "unknown map")
			return
		}

		gamePath := mapUserSettingsPath(duneRoot, targetMap, "UserGame.ini")
		enginePath := mapUserSettingsPath(duneRoot, targetMap, "UserEngine.ini")

		if err := os.WriteFile(gamePath, []byte(req.UserGameINI), 0644); err != nil {
			internalServerError(w, err)
			return
		}
		if err := os.WriteFile(enginePath, []byte(req.UserEngineINI), 0644); err != nil {
			internalServerError(w, err)
			return
		}

		jsonResponse(w, map[string]any{
			"success":  true,
			"message":  "map INI files saved",
			"map":      targetMap,
			"maps":     mapNames,
			"saved_at": time.Now().UTC().Format(time.RFC3339),
		})
		return
	}

	if !containsString(mapNames, selectedMap) {
		clientError(w, http.StatusBadRequest, "unknown map")
		return
	}

	gamePath := mapUserSettingsPath(duneRoot, selectedMap, "UserGame.ini")
	enginePath := mapUserSettingsPath(duneRoot, selectedMap, "UserEngine.ini")

	gameContent, err := os.ReadFile(gamePath)
	if err != nil {
		internalServerError(w, err)
		return
	}
	engineContent, err := os.ReadFile(enginePath)
	if err != nil {
		internalServerError(w, err)
		return
	}

	jsonResponse(w, map[string]any{
		"success":              true,
		"map":                  selectedMap,
		"maps":                 mapNames,
		"user_game_ini":        string(gameContent),
		"user_engine_ini":      string(engineContent),
		"user_game_ini_path":   gamePath,
		"user_engine_ini_path": enginePath,
	})
}

func listRuntimeMapNames(duneRoot string) ([]string, error) {
	gameRoot := filepath.Join(duneRoot, "runtime", "game")
	entries, err := os.ReadDir(gameRoot)
	if err != nil {
		return nil, err
	}

	maps := []string{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == "artifacts" {
			continue
		}
		gamePath := mapUserSettingsPath(duneRoot, name, "UserGame.ini")
		enginePath := mapUserSettingsPath(duneRoot, name, "UserEngine.ini")
		if _, err := os.Stat(gamePath); err != nil {
			continue
		}
		if _, err := os.Stat(enginePath); err != nil {
			continue
		}
		maps = append(maps, name)
	}
	return maps, nil
}

func mapUserSettingsPath(duneRoot, mapName, fileName string) string {
	return filepath.Join(duneRoot, "runtime", "game", mapName, "Saved", "UserSettings", fileName)
}

func containsString(items []string, needle string) bool {
	for _, item := range items {
		if item == needle {
			return true
		}
	}
	return false
}

func systemCommandsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		clientError(w, http.StatusMethodNotAllowed, "only GET is allowed")
		return
	}

	authenticated, err := sessionAuthenticated(r)
	if err != nil {
		internalServerError(w, err)
		return
	}
	if !authenticated {
		clientError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	jsonResponse(w, map[string]any{
		"success":  true,
		"commands": duneCommandCatalog(),
	})
}

func runSystemCommandHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		clientError(w, http.StatusMethodNotAllowed, "only POST is allowed")
		return
	}

	authenticated, err := sessionAuthenticated(r)
	if err != nil {
		internalServerError(w, err)
		return
	}
	if !authenticated {
		clientError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var req struct {
		CommandID string `json:"command_id"`
		Args      string `json:"args"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		clientError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	commandID := strings.TrimSpace(req.CommandID)
	if commandID == "" {
		clientError(w, http.StatusBadRequest, "command_id is required")
		return
	}

	spec, ok := findDuneCommand(commandID)
	if !ok {
		clientError(w, http.StatusBadRequest, "unknown command_id")
		return
	}

	extraArgs, err := parseCommandArgs(req.Args)
	if err != nil {
		clientError(w, http.StatusBadRequest, err.Error())
		return
	}
	if spec.ArgMode == "required" && len(extraArgs) == 0 {
		clientError(w, http.StatusBadRequest, "this command requires arguments")
		return
	}
	for _, arg := range extraArgs {
		if !isSafeCommandArg(arg) {
			clientError(w, http.StatusBadRequest, "arguments contain unsupported control characters")
			return
		}
	}

	duneRoot, rootCheck := resolveDuneRoot()
	if !rootCheck.OK {
		clientError(w, http.StatusBadRequest, rootCheck.Output)
		return
	}

	duneScriptPath := filepath.Join(duneRoot, "runtime", "scripts", "dune")
	if _, err := os.Stat(duneScriptPath); err != nil {
		clientError(w, http.StatusBadRequest, "dune command wrapper not found")
		return
	}

	args := append([]string{}, spec.BaseArgs...)
	args = append(args, extraArgs...)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(ctx, duneScriptPath, args...)
	cmd.Dir = duneRoot
	outputBytes, runErr := cmd.CombinedOutput()
	elapsed := time.Since(start)

	output := strings.TrimSpace(string(outputBytes))
	if output == "" {
		output = "(no output)"
	}
	if len(output) > 12000 {
		output = output[:12000] + "\n...output truncated..."
	}

	exitCode := 0
	if runErr != nil {
		exitCode = 1
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		if ctx.Err() == context.DeadlineExceeded {
			output += "\n\nCommand timed out after 5 minutes."
		}
	}

	jsonResponse(w, map[string]any{
		"success":       runErr == nil,
		"command_id":    spec.ID,
		"command_label": spec.Label,
		"base_args":     spec.BaseArgs,
		"extra_args":    extraArgs,
		"exit_code":     exitCode,
		"duration_ms":   elapsed.Milliseconds(),
		"parsed":        parseCommandOutput(spec.ID, output),
		"output":        output,
	})
}

func parseCommandOutput(commandID string, output string) map[string]any {
	switch commandID {
	case "status":
		return parseStatusOutput(output)
	case "ready":
		return parseReadyOutput(output)
	case "version":
		return parseVersionOutput(output)
	default:
		return nil
	}
}

func parseStatusOutput(output string) map[string]any {
	sections := splitOutputSections(output)
	overview := parseKeyValueLines(sections["Dune status"])
	db := parseKeyValueLines(sections["Database"])
	automation := parseKeyValueLines(sections["Automation"])
	rabbit := parseKeyValueLines(sections["RabbitMQ game connections"])
	fls := parseKeyValueLines(sections["Funcom/FLS summary"])

	return map[string]any{
		"kind":                 "status",
		"overview":             overview,
		"database":             db,
		"automation":           automation,
		"rabbitmq_connections": rabbit,
		"fls_summary":          fls,
		"containers":           parseTable(sections["Containers"]),
		"listeners":            parseTable(sections["Listeners"]),
		"game_servers":         parseTable(sections["Game servers"]),
	}
}

func parseReadyOutput(output string) map[string]any {
	sections := splitOutputSections(output)
	checks := []map[string]any{}
	stateCounts := map[string]int{"OK": 0, "WAIT": 0, "FAIL": 0}

	for sectionName, lines := range sections {
		if sectionName == "" {
			continue
		}
		for _, raw := range lines {
			line := strings.TrimSpace(raw)
			if line == "" {
				continue
			}
			state, message, ok := parseCheckLine(line)
			if !ok {
				continue
			}
			stateCounts[state] += 1
			checks = append(checks, map[string]any{
				"section": sectionName,
				"state":   state,
				"message": message,
			})
		}
	}

	overall := "WARMING"
	upper := strings.ToUpper(output)
	if strings.Contains(upper, "READY:") {
		overall = "READY"
	}
	if strings.Contains(upper, "FAIL:") {
		overall = "FAIL"
	}

	return map[string]any{
		"kind":    "ready",
		"overall": overall,
		"counts":  stateCounts,
		"checks":  checks,
	}
}

func parseVersionOutput(output string) map[string]any {
	sections := splitOutputSections(output)
	parsedSections := map[string]map[string]string{}
	for name, lines := range sections {
		if name == "" {
			continue
		}
		parsedSections[name] = parseKeyValueLines(lines)
	}

	return map[string]any{
		"kind":     "version",
		"sections": parsedSections,
	}
}

func splitOutputSections(output string) map[string][]string {
	sections := map[string][]string{}
	current := ""

	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "===") && strings.HasSuffix(trimmed, "===") {
			title := strings.TrimSpace(strings.Trim(trimmed, "="))
			current = title
			if _, exists := sections[current]; !exists {
				sections[current] = []string{}
			}
			continue
		}
		sections[current] = append(sections[current], line)
	}

	return sections
}

func parseKeyValueLines(lines []string) map[string]string {
	result := map[string]string{}
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key == "" {
			continue
		}
		result[key] = value
	}
	return result
}

func parseTable(lines []string) map[string]any {
	headers := []string{}
	rows := []map[string]string{}
	splitter := regexp.MustCompile(`\s{2,}`)

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		parts := splitter.Split(line, -1)
		if len(parts) < 2 {
			continue
		}

		if len(headers) == 0 {
			headers = parts
			continue
		}

		row := map[string]string{}
		for i := range headers {
			if i < len(parts) {
				row[headers[i]] = strings.TrimSpace(parts[i])
			} else {
				row[headers[i]] = ""
			}
		}
		rows = append(rows, row)
	}

	return map[string]any{
		"headers": headers,
		"rows":    rows,
	}
}

func parseCheckLine(line string) (string, string, bool) {
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return "", "", false
	}
	state := strings.TrimSpace(parts[0])
	if state != "OK" && state != "WAIT" && state != "FAIL" {
		return "", "", false
	}
	message := strings.TrimSpace(strings.TrimPrefix(line, state))
	return state, message, true
}

func duneCommandCatalog() []duneCommandSpec {
	return []duneCommandSpec{
		{ID: "status", Category: "Overview", Label: "Status", Description: "Show battlegroup dashboard summary.", BaseArgs: []string{"status"}, ArgMode: "none"},
		{ID: "ready", Category: "Overview", Label: "Ready", Description: "Quick readiness check (OK/WAIT/FAIL).", BaseArgs: []string{"ready"}, ArgMode: "none"},
		{ID: "version", Category: "Overview", Label: "Version", Description: "Show launcher and stack version details.", BaseArgs: []string{"version"}, ArgMode: "none"},
		{ID: "doctor", Category: "Overview", Label: "Doctor", Description: "Run troubleshooting diagnostics.", BaseArgs: []string{"doctor"}, ArgMode: "none"},
		{ID: "ps", Category: "Overview", Label: "Containers", Description: "List dune containers and status.", BaseArgs: []string{"ps"}, ArgMode: "none"},
		{ID: "ports", Category: "Overview", Label: "Ports", Description: "Display mapped and required ports.", BaseArgs: []string{"ports"}, ArgMode: "none"},

		{ID: "start", Category: "Battlegroup", Label: "Start", Description: "Start battlegroup and autoscaler.", BaseArgs: []string{"start"}, ArgMode: "none"},
		{ID: "stop", Category: "Battlegroup", Label: "Stop", Description: "Stop battlegroup and autoscaler.", BaseArgs: []string{"stop"}, ArgMode: "none"},
		{ID: "servers", Category: "Battlegroup", Label: "Servers", Description: "List running server maps.", BaseArgs: []string{"servers"}, ArgMode: "none"},
		{ID: "restart", Category: "Battlegroup", Label: "Restart Service", Description: "Restart one service target.", BaseArgs: []string{"restart"}, ArgMode: "required", ArgHint: "survival|overmap|gateway|director|text-router"},

		{ID: "logs", Category: "Diagnostics", Label: "Logs", Description: "View service logs.", BaseArgs: []string{"logs"}, ArgMode: "required", ArgHint: "service-name [--raw]"},

		{ID: "autoscaler", Category: "Autoscaler", Label: "Autoscaler", Description: "Control autoscaler status/start/stop/restart/logs/run.", BaseArgs: []string{"autoscaler"}, ArgMode: "optional", ArgHint: "status|start|stop|restart|logs|run"},
		{ID: "spawn", Category: "Autoscaler", Label: "Spawn Map", Description: "Spawn dynamic map by map name or partition id.", BaseArgs: []string{"spawn"}, ArgMode: "required", ArgHint: "map-name|partition-id"},
		{ID: "despawn", Category: "Autoscaler", Label: "Despawn Map", Description: "Despawn map by name/id/container.", BaseArgs: []string{"despawn"}, ArgMode: "required", ArgHint: "map-name|partition-id|container-name"},

		{ID: "update", Category: "Updates", Label: "Game Update", Description: "Run game server update commands.", BaseArgs: []string{"update"}, ArgMode: "optional", ArgHint: "check|auto enable|auto disable|auto status|--yes"},
		{ID: "self-update", Category: "Updates", Label: "Stack Update", Description: "Run self-host stack updater.", BaseArgs: []string{"self-update"}, ArgMode: "optional", ArgHint: "check|install latest|install <tag>"},
		{ID: "restart-schedule", Category: "Updates", Label: "Restart Schedule", Description: "Manage scheduled restarts.", BaseArgs: []string{"restart-schedule"}, ArgMode: "optional", ArgHint: "enable <hours>|disable|status"},

		{ID: "db", Category: "Database", Label: "Database", Description: "Run database backup/import/restore commands.", BaseArgs: []string{"db"}, ArgMode: "required", ArgHint: "backup|list|status|import <file>|restore <file>|delete <file>"},

		{ID: "config", Category: "Config", Label: "Config", Description: "Manage battlegroup config values.", BaseArgs: []string{"config"}, ArgMode: "required", ArgHint: "title [\"New Server Name\"]"},
		{ID: "memory", Category: "Config", Label: "Memory", Description: "Manage map memory configuration.", BaseArgs: []string{"memory"}, ArgMode: "required", ArgHint: "status|list-maps|set <map> <memory>|unset <map>"},
		{ID: "sietches", Category: "Config", Label: "Sietches", Description: "Inspect and edit sietch settings.", BaseArgs: []string{"sietches"}, ArgMode: "optional", ArgHint: "list|show <map>|set-max <map> <count>"},
	}
}

func findDuneCommand(commandID string) (duneCommandSpec, bool) {
	for _, spec := range duneCommandCatalog() {
		if spec.ID == commandID {
			return spec, true
		}
	}
	return duneCommandSpec{}, false
}

func isSafeCommandArg(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

func parseCommandArgs(input string) ([]string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil, nil
	}

	args := []string{}
	var current strings.Builder
	inQuotes := false
	quoteRune := rune(0)
	escaped := false

	flushCurrent := func() {
		if current.Len() > 0 {
			args = append(args, current.String())
			current.Reset()
		}
	}

	for _, r := range trimmed {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}

		if r == '\\' {
			escaped = true
			continue
		}

		if inQuotes {
			if r == quoteRune {
				inQuotes = false
				quoteRune = 0
				continue
			}
			current.WriteRune(r)
			continue
		}

		if r == '"' || r == '\'' {
			inQuotes = true
			quoteRune = r
			continue
		}

		if r == ' ' || r == '\t' || r == '\n' {
			flushCurrent()
			continue
		}

		current.WriteRune(r)
	}

	if escaped {
		return nil, fmt.Errorf("invalid trailing escape in args")
	}
	if inQuotes {
		return nil, fmt.Errorf("unterminated quoted argument")
	}

	flushCurrent()
	return args, nil
}
