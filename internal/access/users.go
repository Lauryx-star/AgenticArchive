package access

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalid         = errors.New("Ungültige Eingabe")
	ErrForbidden       = errors.New("Für diese Aktion fehlt die Berechtigung.")
	ErrNotFound        = errors.New("Benutzerkonto nicht gefunden.")
	ErrUsernameExists  = errors.New("Dieser Benutzername ist bereits vergeben.")
	ErrLastAdmin       = errors.New("Mindestens ein Administratorkonto muss erhalten bleiben.")
	ErrCurrentPassword = errors.New("Das bisherige Passwort ist falsch.")
)

func validateUsername(username string) error {
	if !utf8.ValidString(username) || username != strings.TrimSpace(username) || len(username) < 1 || len(username) > 64 || strings.ContainsFunc(username, unicode.IsControl) {
		return fmt.Errorf("%w: Benutzername muss 1–64 Bytes ohne Steuerzeichen oder Rand-Leerzeichen enthalten.", ErrInvalid)
	}
	return nil
}

func passwordRecord(password string) (salt, hash []byte, err error) {
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < 15 || len(password) > 1024 {
		return nil, nil, fmt.Errorf("%w: Passwort muss mindestens 15 Zeichen und höchstens 1024 Bytes enthalten.", ErrInvalid)
	}
	salt = make([]byte, 16)
	if _, err = rand.Read(salt); err != nil {
		return nil, nil, err
	}
	hash, err = pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
	return
}

func validateRole(role string) error {
	if role != "admin" && role != "reader" {
		return fmt.Errorf("%w: Rolle muss admin oder reader sein.", ErrInvalid)
	}
	return nil
}

func requireAdmin(ctx context.Context, tx *sql.Tx, actorID int64) error {
	var role string
	err := tx.QueryRowContext(ctx, "SELECT role FROM users WHERE id=?", actorID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && role != "admin") {
		return ErrForbidden
	}
	return err
}

func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,username,role FROM users ORDER BY username COLLATE NOCASE, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		var user User
		if err := rows.Scan(&user.ID, &user.Username, &user.Role); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func (s *Store) CreateUser(ctx context.Context, actorID int64, username, password, role string) (User, error) {
	if err := validateUsername(username); err != nil {
		return User{}, err
	}
	if err := validateRole(role); err != nil {
		return User{}, err
	}
	salt, hash, err := passwordRecord(password)
	if err != nil {
		return User{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	if err := requireAdmin(ctx, tx, actorID); err != nil {
		return User{}, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM users WHERE username=?", username).Scan(&count); err != nil {
		return User{}, err
	}
	if count != 0 {
		return User{}, ErrUsernameExists
	}
	result, err := tx.ExecContext(ctx, "INSERT INTO users(username,role,salt,password_hash,iterations) VALUES(?,?,?,?,?)", username, role, salt, hash, passwordIterations)
	if err != nil {
		return User{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return User{ID: id, Username: username, Role: role}, nil
}

func (s *Store) ChangeRole(ctx context.Context, actorID, targetID int64, role string) (User, error) {
	if err := validateRole(role); err != nil {
		return User{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	if err := requireAdmin(ctx, tx, actorID); err != nil {
		return User{}, err
	}
	var target User
	err = tx.QueryRowContext(ctx, "SELECT id,username,role FROM users WHERE id=?", targetID).Scan(&target.ID, &target.Username, &target.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	if target.Role == "admin" && role != "admin" {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM users WHERE role='admin'").Scan(&count); err != nil {
			return User{}, err
		}
		if count <= 1 {
			return User{}, ErrLastAdmin
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE users SET role=? WHERE id=?", role, targetID); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	target.Role = role
	return target, nil
}

func replacePassword(ctx context.Context, tx *sql.Tx, id int64, salt, hash []byte) error {
	if _, err := tx.ExecContext(ctx, "UPDATE users SET salt=?,password_hash=?,iterations=? WHERE id=?", salt, hash, passwordIterations, id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id=?", id)
	return err
}

// Administrators reset other accounts. Their own account uses current-password verification.
func (s *Store) ResetUserPassword(ctx context.Context, actorID, targetID int64, password string) error {
	if actorID == targetID {
		return fmt.Errorf("%w: Für das eigene Konto bitte „Mein Passwort ändern“ verwenden.", ErrInvalid)
	}
	salt, hash, err := passwordRecord(password)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireAdmin(ctx, tx, actorID); err != nil {
		return err
	}
	var id int64
	if err := tx.QueryRowContext(ctx, "SELECT id FROM users WHERE id=?", targetID).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if err := replacePassword(ctx, tx, id, salt, hash); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ChangePassword(ctx context.Context, userID int64, currentPassword, newPassword string) error {
	if len(currentPassword) > 1024 {
		return ErrCurrentPassword
	}
	salt, hash, err := passwordRecord(newPassword)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var currentSalt, expected []byte
	var iterations int
	if err := tx.QueryRowContext(ctx, "SELECT salt,password_hash,iterations FROM users WHERE id=?", userID).Scan(&currentSalt, &expected, &iterations); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCurrentPassword
		}
		return err
	}
	if iterations < passwordIterations || iterations > 2000000 {
		return fmt.Errorf("unsupported password parameters")
	}
	actual, err := pbkdf2.Key(sha256.New, currentPassword, currentSalt, iterations, 32)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare(actual, expected) != 1 {
		return ErrCurrentPassword
	}
	if err := replacePassword(ctx, tx, userID, salt, hash); err != nil {
		return err
	}
	return tx.Commit()
}
