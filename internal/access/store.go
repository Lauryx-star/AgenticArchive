// Package access owns identities and sessions independently of the document index.
package access

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const passwordIterations = 600000
const SessionLifetime = 12 * time.Hour

var ErrCredentials = errors.New("Benutzername oder Passwort ist falsch.")

type Permission string

const (
	ReadArchive    Permission = "archive.read"
	ManageSettings Permission = "settings.manage"
	ManageImports  Permission = "imports.manage"
	ManageUsers    Permission = "users.manage"
)

type User struct {
	ID             int64  `json:"id"`
	Username       string `json:"username"`
	Role           string `json:"role"`
	credentialHash [32]byte
}

func (u User) Can(permission Permission) bool {
	switch permission {
	case ReadArchive:
		return u.Role == "admin" || u.Role == "reader"
	case ManageSettings, ManageImports, ManageUsers:
		return u.Role == "admin"
	default:
		return false
	}
}

type Session struct {
	User     User
	CSRFHash []byte
	Expires  time.Time
}

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// Create the file privately before SQLite opens it. An existing file is preserved.
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	err = f.Chmod(0600)
	f.Close()
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	db, err := sql.Open("sqlite3", u.String()+"?_foreign_keys=on&_busy_timeout=5000&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS users (
 id INTEGER PRIMARY KEY, username TEXT NOT NULL UNIQUE,
 role TEXT NOT NULL CHECK(role IN ('admin','reader')),
 salt BLOB NOT NULL, password_hash BLOB NOT NULL, iterations INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
 token_hash BLOB PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 csrf_hash BLOB NOT NULL, expires INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_user ON sessions(user_id);
CREATE TABLE IF NOT EXISTS agent_tokens (
 id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 name TEXT NOT NULL, token_hash BLOB NOT NULL UNIQUE, created INTEGER NOT NULL, expires INTEGER NOT NULL,
 temporary INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS agent_tokens_user ON agent_tokens(user_id);
CREATE TABLE IF NOT EXISTS chats (
 id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 title TEXT NOT NULL, created INTEGER NOT NULL, updated INTEGER NOT NULL,
 version INTEGER NOT NULL DEFAULT 1, working TEXT NOT NULL DEFAULT '[]', notes TEXT NOT NULL DEFAULT '', imports TEXT NOT NULL DEFAULT '[]'
);
CREATE INDEX IF NOT EXISTS chats_user_updated ON chats(user_id, updated DESC);
CREATE TABLE IF NOT EXISTS chat_messages (
 id INTEGER PRIMARY KEY AUTOINCREMENT, chat_id INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
 role TEXT NOT NULL CHECK(role IN ('user','assistant')), content TEXT NOT NULL, sources TEXT NOT NULL DEFAULT '[]', created INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS chat_messages_chat ON chat_messages(chat_id,id);
CREATE TABLE IF NOT EXISTS chat_results (
 id INTEGER PRIMARY KEY AUTOINCREMENT, chat_id INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
 message_id INTEGER NOT NULL REFERENCES chat_messages(id) ON DELETE CASCADE,
 content TEXT NOT NULL, sources TEXT NOT NULL, partial INTEGER NOT NULL, open_questions TEXT NOT NULL, created INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS chat_results_chat ON chat_results(chat_id,id);
CREATE INDEX IF NOT EXISTS chats_user_id ON chats(user_id,id DESC);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Configured(ctx context.Context) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM users").Scan(&count)
	return count > 0, err
}

// SetupAdmin may run only once, including when concurrent requests arrive.
func (s *Store) SetupAdmin(ctx context.Context, username, password string) error {
	if err := validateUsername(username); err != nil {
		return err
	}
	salt, hash, err := passwordRecord(password)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO users (username,role,salt,password_hash,iterations)
 SELECT ?, 'admin', ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM users)`, username, salt, hash, passwordIterations)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("Das Administratorkonto ist bereits eingerichtet.")
	}
	return nil
}

func (s *Store) Authenticate(ctx context.Context, username, password string) (User, error) {
	var user User
	var salt, expected []byte
	iterations := passwordIterations
	err := s.db.QueryRowContext(ctx, "SELECT id,username,role,salt,password_hash,iterations FROM users WHERE username=?", username).Scan(&user.ID, &user.Username, &user.Role, &salt, &expected, &iterations)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return User{}, err
	}
	missing := errors.Is(err, sql.ErrNoRows)
	if missing {
		salt = make([]byte, 16)
		expected = make([]byte, 32)
	}
	if iterations < passwordIterations || iterations > 2000000 {
		return User{}, fmt.Errorf("unsupported password parameters")
	}
	actual, err := pbkdf2.Key(sha256.New, password, salt, iterations, 32)
	if err != nil {
		return User{}, err
	}
	if subtle.ConstantTimeCompare(actual, expected) != 1 || missing {
		return User{}, ErrCredentials
	}
	copy(user.credentialHash[:], expected)
	return user, nil
}

func RandomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func tokenHash(token string) []byte { h := sha256.Sum256([]byte(token)); return h[:] }

func (s *Store) NewSession(ctx context.Context, user User, now time.Time) (token, csrf string, err error) {
	token, err = RandomToken()
	if err != nil {
		return
	}
	csrf, err = RandomToken()
	if err != nil {
		return
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	// Password recovery may have committed after authentication. Never issue a
	// fresh session from credentials that have since been replaced.
	var currentHash []byte
	if err = tx.QueryRowContext(ctx, "SELECT password_hash FROM users WHERE id=?", user.ID).Scan(&currentHash); err != nil {
		return "", "", err
	}
	if subtle.ConstantTimeCompare(currentHash, user.credentialHash[:]) != 1 {
		return "", "", ErrCredentials
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM sessions WHERE expires<=?", now.Unix()); err != nil {
		return "", "", err
	}
	// Bound storage even if a valid account repeatedly logs in.
	if _, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=? AND token_hash NOT IN
 (SELECT token_hash FROM sessions WHERE user_id=? ORDER BY expires DESC, rowid DESC LIMIT 19)`, user.ID, user.ID); err != nil {
		return "", "", err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO sessions(token_hash,user_id,csrf_hash,expires) VALUES(?,?,?,?)", tokenHash(token), user.ID, tokenHash(csrf), now.Add(SessionLifetime).Unix())
	if err != nil {
		return "", "", err
	}
	err = tx.Commit()
	return
}

func (s *Store) Session(ctx context.Context, token string, now time.Time) (Session, error) {
	var session Session
	var expires int64
	var passwordHash []byte
	if len(token) != 64 {
		return session, sql.ErrNoRows
	}
	err := s.db.QueryRowContext(ctx, `SELECT u.id,u.username,u.role,s.csrf_hash,s.expires,u.password_hash
 FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=? AND s.expires>?`, tokenHash(token), now.Unix()).Scan(&session.User.ID, &session.User.Username, &session.User.Role, &session.CSRFHash, &expires, &passwordHash)
	copy(session.User.credentialHash[:], passwordHash)
	session.Expires = time.Unix(expires, 0)
	return session, err
}
func (s *Store) Logout(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash=?", tokenHash(token))
	return err
}

// ResetAdminPassword is a local recovery operation; it revokes every session for that user.
func (s *Store) ResetAdminPassword(ctx context.Context, username, password string) error {
	salt, hash, err := passwordRecord(password)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int64
	if err := tx.QueryRowContext(ctx, "SELECT id FROM users WHERE username=? AND role='admin'", username).Scan(&id); err != nil {
		return fmt.Errorf("Administratorkonto nicht gefunden.")
	}
	if err := replacePassword(ctx, tx, id, salt, hash); err != nil {
		return err
	}
	return tx.Commit()
}
func (s Session) ValidCSRF(token string) bool {
	return len(token) == 64 && subtle.ConstantTimeCompare(tokenHash(token), s.CSRFHash) == 1
}
