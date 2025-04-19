package main

import (
	"bytes"
	"database/sql"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/go-webauthn/webauthn/webauthn"
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
)

func init() {
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
);`

	_, err = db.Exec(schema)
	if err != nil {
		log.Fatalf("failed to initialize database schema: %v", err)
	}
}

func main() {
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

	http.HandleFunc("/register/start", handleRegisterStart)
	http.HandleFunc("/register/finish", handleRegisterFinish)
	http.Handle("/", http.FileServer(http.Dir("frontend")))

	fmt.Println("Listening on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func handleRegisterStart(w http.ResponseWriter, r *http.Request) {
	invite := r.URL.Query().Get("invite")
	if invite != "secret-token" {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	name := r.URL.Query().Get("user")
	if name == "" {
		http.Error(w, "Missing user", http.StatusBadRequest)
		return
	}

	var user User
	row := db.QueryRow(`SELECT id, name, display_name FROM users WHERE name = ?`, name)
	err := row.Scan(&user.ID, &user.Name, &user.DisplayName)
	if err == sql.ErrNoRows {
		user = User{
			ID:          []byte(name),
			Name:        name,
			DisplayName: name,
		}
		_, err := db.Exec(`INSERT INTO users (id, name, display_name) VALUES (?, ?, ?)`, user.ID, user.Name, user.DisplayName)
		if err != nil {
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}
	} else if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	opts, sessionData, err := webAuthn.BeginRegistration(&user)
	if err != nil {
		http.Error(w, "Registration start error", http.StatusInternalServerError)
		return
	}

	sessionStore.mu.Lock()
	sessionStore.registration[name] = sessionData
	sessionStore.mu.Unlock()

	json.NewEncoder(w).Encode(opts)
}

func handleRegisterFinish(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("user")
	var user User
	row := db.QueryRow(`SELECT id, name, display_name FROM users WHERE name = ?`, name)
	err := row.Scan(&user.ID, &user.Name, &user.DisplayName)
	if err != nil {
		http.Error(w, "User not found", http.StatusBadRequest)
		return
	}

	sessionStore.mu.RLock()
	sessionData, ok := sessionStore.registration[name]
	sessionStore.mu.RUnlock()
	if !ok {
		http.Error(w, "Session not found", http.StatusBadRequest)
		return
	}

	cred, err := webAuthn.FinishRegistration(&user, *sessionData, r)
	if err != nil {
		http.Error(w, "Registration finish error", http.StatusBadRequest)
		return
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(cred); err != nil {
		http.Error(w, "Failed to encode credential", http.StatusInternalServerError)
		return
	}
	_, err = db.Exec(`INSERT INTO credentials (id, user_id, credential_data) VALUES (?, ?, ?)`, cred.ID, user.ID, buf.Bytes())
	if err != nil {
		http.Error(w, "Failed to store credential", http.StatusInternalServerError)
		return
	}

	user.AddCredential(*cred)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "Registration complete. You can now log in."})
}
