package access

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type AgentToken struct {
	ID      int64     `json:"id"`
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
}

// TokenPrincipal retains both identities for a future read-access audit log.
// Token authentication always grants archive reads only, even for administrators.
type TokenPrincipal struct {
	User    User
	TokenID int64
}

func (s *Store) CreateAgentToken(ctx context.Context, user User, name string, lifetime time.Duration, temporary bool, now time.Time) (AgentToken, string, error) {
	if !utf8.ValidString(name) || strings.TrimSpace(name) != name || len(name) < 1 || len(name) > 80 || strings.ContainsFunc(name, unicode.IsControl) || lifetime <= 0 || lifetime > 365*24*time.Hour || (temporary && lifetime > 5*time.Minute) {
		return AgentToken{}, "", fmt.Errorf("%w: Tokenname oder Laufzeit ungültig.", ErrInvalid)
	}
	raw, err := RandomToken()
	if err != nil {
		return AgentToken{}, "", err
	}
	raw = "aa_" + raw
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentToken{}, "", err
	}
	defer tx.Rollback()
	var current User
	var hash []byte
	err = tx.QueryRowContext(ctx, "SELECT id,username,role,password_hash FROM users WHERE id=?", user.ID).Scan(&current.ID, &current.Username, &current.Role, &hash)
	if err != nil {
		return AgentToken{}, "", err
	}
	if !current.Can(ReadArchive) || subtle.ConstantTimeCompare(hash, user.credentialHash[:]) != 1 {
		return AgentToken{}, "", ErrForbidden
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM agent_tokens WHERE expires<=?", now.Unix()); err != nil {
		return AgentToken{}, "", err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM agent_tokens WHERE user_id=? AND temporary=?", user.ID, temporary).Scan(&count); err != nil {
		return AgentToken{}, "", err
	}
	if count >= 20 {
		return AgentToken{}, "", fmt.Errorf("%w: Höchstens 20 aktive Tokens. Bitte alte Tokens widerrufen.", ErrInvalid)
	}
	token := AgentToken{Name: name, Created: time.Unix(now.Unix(), 0), Expires: time.Unix(now.Add(lifetime).Unix(), 0)}
	result, err := tx.ExecContext(ctx, "INSERT INTO agent_tokens(user_id,name,token_hash,created,expires,temporary) VALUES(?,?,?,?,?,?)", user.ID, name, tokenHash(raw), token.Created.Unix(), token.Expires.Unix(), temporary)
	if err != nil {
		return AgentToken{}, "", err
	}
	token.ID, err = result.LastInsertId()
	if err != nil {
		return AgentToken{}, "", err
	}
	if err = tx.Commit(); err != nil {
		return AgentToken{}, "", err
	}
	return token, raw, nil
}

func (s *Store) AgentTokens(ctx context.Context, userID int64, now time.Time) ([]AgentToken, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,name,created,expires FROM agent_tokens WHERE user_id=? AND temporary=0 AND expires>? ORDER BY id DESC", userID, now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tokens := []AgentToken{}
	for rows.Next() {
		var token AgentToken
		var created, expires int64
		if err := rows.Scan(&token.ID, &token.Name, &created, &expires); err != nil {
			return nil, err
		}
		token.Created = time.Unix(created, 0)
		token.Expires = time.Unix(expires, 0)
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

func (s *Store) RevokeAgentToken(ctx context.Context, userID, id int64) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM agent_tokens WHERE id=? AND user_id=?", id, userID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) AgentToken(ctx context.Context, raw string, now time.Time) (TokenPrincipal, error) {
	var principal TokenPrincipal
	if len(raw) != 67 || !strings.HasPrefix(raw, "aa_") {
		return principal, sql.ErrNoRows
	}
	err := s.db.QueryRowContext(ctx, `SELECT u.id,u.username,u.role,t.id FROM agent_tokens t JOIN users u ON u.id=t.user_id WHERE t.token_hash=? AND t.expires>?`, tokenHash(raw), now.Unix()).Scan(&principal.User.ID, &principal.User.Username, &principal.User.Role, &principal.TokenID)
	if err == nil && !principal.User.Can(ReadArchive) {
		return TokenPrincipal{}, ErrForbidden
	}
	return principal, err
}
