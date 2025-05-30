package main

import (
	"bytes"
	"database/sql"
	"encoding/gob"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)

type User struct {
	ID          []byte
	Name        string
	DisplayName string
	Credentials []webauthn.Credential
}

func (u *User) WebAuthnID() []byte                         { return u.ID }
func (u *User) WebAuthnName() string                       { return u.Name }
func (u *User) WebAuthnDisplayName() string                { return u.DisplayName }
func (u *User) WebAuthnIcon() string                       { return "" }
func (u *User) WebAuthnCredentials() []webauthn.Credential { return u.Credentials }
func (u *User) AddCredential(cred webauthn.Credential)     { u.Credentials = append(u.Credentials, cred) }

var (
	webAuthn     *webauthn.WebAuthn
	db           *sql.DB
	sessionStore = struct {
		registration map[string]*webauthn.SessionData
		mu           sync.RWMutex
	}{registration: make(map[string]*webauthn.SessionData)}

	// Flag to control whether registration endpoints are enabled
	registrationOn bool
)

func init() {
	// Define the flag for enabling/disabling registration endpoints.
	// If the flag is passed, `registrationOn` will be set to true.
	// If it's not passed, it defaults to false.
	flag.BoolVar(&registrationOn, "registration-on", false, "Enable user registration endpoints")
	flag.Parse()

	dbPath := filepath.Join(".", "db", "users.db")
	os.MkdirAll(filepath.Dir(dbPath), os.ModePerm)

	var err error
	db, err = sql.Open("sqlite3", dbPath)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}

	schema := `
CREATE TABLE IF NOT EXISTS users (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	display_name TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS credentials (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL,
	credential_data BLOB NOT NULL,
	FOREIGN KEY(user_id) REFERENCES users(id)
);

CREATE TABLE IF NOT EXISTS logins (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id TEXT NOT NULL,
	success INTEGER NOT NULL,
	remote_ip TEXT,
	ts DATETIME DEFAULT CURRENT_TIMESTAMP,
	FOREIGN KEY(user_id) REFERENCES users(id)
);`

	_, err = db.Exec(schema)
	if err != nil {
		log.Fatalf("failed to initialize database schema: %v", err)
	}
}

func main() {
	// Log whether registration endpoints are enabled or not
	if registrationOn {
		log.Println("Registration endpoints are enabled.")
	} else {
		log.Println("Registration endpoints will serve 404 errors.")
	}

	var err error
	webAuthn, err = webauthn.New(&webauthn.Config{
		RPDisplayName: "My App",
		RPID:          "localhost",
		RPOrigins:     []string{"http://localhost:8080"},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Conditionally serve the registration routes based on the flag
	if registrationOn {
		http.HandleFunc("/register/start", handleRegisterStart)
		http.HandleFunc("/register/finish", handleRegisterFinish)
	} else {
		// Serve a clean 404 response if registration routes are disabled
		http.HandleFunc("/register/start", handle404)
		http.HandleFunc("/register/finish", handle404)
	}

	// Serve other routes
	// New handler to check if registration is enabled
	http.HandleFunc("/check-registration", handleCheckRegistration)
	http.HandleFunc("/login/start", handleLoginStart)
	http.HandleFunc("/login/finish", handleLoginFinish)
	http.Handle("/", http.FileServer(http.Dir("frontend")))

	log.Println("Listening on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func writeJSONError(w http.ResponseWriter, message string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// Handle 404 for disabled registration routes
func handle404(w http.ResponseWriter, r *http.Request) {
	log.Println("Handling 404 when routes are disabled")
	w.WriteHeader(http.StatusNotFound)
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte("<h1>404 Not Found</h1><p>The requested route is not available.</p>"))
}

// Handler to check if registration is enabled
func handleCheckRegistration(w http.ResponseWriter, r *http.Request) {
	if registrationOn {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]bool{"registrationEnabled": true})
	} else {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]bool{"registrationEnabled": false})
	}
}

func handleRegisterStart(w http.ResponseWriter, r *http.Request) {
	invite := r.URL.Query().Get("invite")
	if invite != "secret-token" {
		writeJSONError(w, "Forbidden", http.StatusForbidden)
		return
	}

	name := r.URL.Query().Get("user")
	if name == "" {
		writeJSONError(w, "Missing user", http.StatusBadRequest)
		return
	}

	var user User
	row := db.QueryRow(`SELECT id, name, display_name FROM users WHERE name = ?`, name)
	err := row.Scan(&user.ID, &user.Name, &user.DisplayName)
	if err == sql.ErrNoRows {
		userID := uuid.New().String()
		user = User{
			ID:          []byte(userID),
			Name:        name,
			DisplayName: name,
		}
		_, err := db.Exec(`INSERT INTO users (id, name, display_name) VALUES (?, ?, ?)`, user.ID, user.Name, user.DisplayName)
		if err != nil {
			writeJSONError(w, "Database error", http.StatusInternalServerError)
			return
		}
	} else if err != nil {
		writeJSONError(w, "Database error", http.StatusInternalServerError)
		return
	}

	opts, sessionData, err := webAuthn.BeginRegistration(&user)
	if err != nil {
		writeJSONError(w, "Registration start error", http.StatusInternalServerError)
		return
	}

	sessionStore.mu.Lock()
	sessionStore.registration[name] = sessionData
	sessionStore.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"publicKey": opts})
}

func handleRegisterFinish(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("user")
	var user User
	row := db.QueryRow(`SELECT id, name, display_name FROM users WHERE name = ?`, name)
	err := row.Scan(&user.ID, &user.Name, &user.DisplayName)
	if err != nil {
		writeJSONError(w, "User not found", http.StatusBadRequest)
		return
	}

	sessionStore.mu.RLock()
	sessionData, ok := sessionStore.registration[name]
	sessionStore.mu.RUnlock()
	if !ok {
		writeJSONError(w, "Session not found", http.StatusBadRequest)
		return
	}

	cred, err := webAuthn.FinishRegistration(&user, *sessionData, r)
	if err != nil {
		writeJSONError(w, fmt.Sprintf("Registration finish error: %v", err), http.StatusBadRequest)
		return
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(cred); err != nil {
		writeJSONError(w, "Failed to encode credential", http.StatusInternalServerError)
		return
	}
	_, err = db.Exec(`INSERT INTO credentials (id, user_id, credential_data) VALUES (?, ?, ?)`, cred.ID, user.ID, buf.Bytes())
	if err != nil {
		writeJSONError(w, "Failed to store credential", http.StatusInternalServerError)
		return
	}

	user.AddCredential(*cred)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "Registration complete. You can now log in."})
}

func handleLoginStart(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("user")
	if name == "" {
		writeJSONError(w, "Missing user", http.StatusBadRequest)
		return
	}

	var user User
	row := db.QueryRow(`SELECT id, name, display_name FROM users WHERE name = ?`, name)
	err := row.Scan(&user.ID, &user.Name, &user.DisplayName)
	if err != nil {
		writeJSONError(w, "User not found", http.StatusBadRequest)
		return
	}

	rows, err := db.Query(`SELECT credential_data FROM credentials WHERE user_id = ?`, user.ID)
	if err != nil {
		writeJSONError(w, "Credential lookup error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var blob []byte
		if err := rows.Scan(&blob); err == nil {
			var cred webauthn.Credential
			if err := gob.NewDecoder(bytes.NewReader(blob)).Decode(&cred); err == nil {
				user.AddCredential(cred)
			}
		}
	}

	opts, sessionData, err := webAuthn.BeginLogin(&user)
	if err != nil {
		writeJSONError(w, "Login start error", http.StatusInternalServerError)
		return
	}

	sessionStore.mu.Lock()
	if sessionStore.registration == nil {
		sessionStore.registration = make(map[string]*webauthn.SessionData)
	}
	sessionStore.registration["login:"+name] = sessionData
	sessionStore.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"publicKey": opts})
}

func handleLoginFinish(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("user")
	var user User
	row := db.QueryRow(`SELECT id, name, display_name FROM users WHERE name = ?`, name)
	err := row.Scan(&user.ID, &user.Name, &user.DisplayName)
	if err != nil {
		writeJSONError(w, "User not found", http.StatusBadRequest)
		return
	}

	rows, err := db.Query(`SELECT credential_data FROM credentials WHERE user_id = ?`, user.ID)
	if err != nil {
		writeJSONError(w, "Credential loading error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var blob []byte
		if err := rows.Scan(&blob); err == nil {
			var cred webauthn.Credential
			if err := gob.NewDecoder(bytes.NewReader(blob)).Decode(&cred); err == nil {
				user.AddCredential(cred)
			}
		}
	}

	sessionStore.mu.RLock()
	sessionData, ok := sessionStore.registration["login:"+name]
	sessionStore.mu.RUnlock()
	if !ok {
		writeJSONError(w, "Login session not found", http.StatusBadRequest)
		return
	}

	_, err = webAuthn.FinishLogin(&user, *sessionData, r)
	if err != nil {
		_, _ = db.Exec(`INSERT INTO logins (user_id, success, remote_ip) VALUES (?, 0, ?)`, user.ID, r.RemoteAddr)
		writeJSONError(w, fmt.Sprintf("Login finish error: %v", err), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = db.Exec(`INSERT INTO logins (user_id, success, remote_ip) VALUES (?, 1, ?)`, user.ID, r.RemoteAddr)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "Authentication successful."})
}
