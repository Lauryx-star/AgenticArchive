package access

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Lauryx-star/AgenticArchive/internal/agent"
)

var ErrChatNotFound = errors.New("Chat nicht gefunden.")
var ErrChatConflict = errors.New("Der Chat wurde inzwischen verändert. Bitte lade ihn erneut.")

type ChatImport struct {
	ID      int64  `json:"id"`
	Title   string `json:"title"`
	Version int64  `json:"version"`
}
type Chat struct {
	ID       int64           `json:"id"`
	Title    string          `json:"title"`
	Created  time.Time       `json:"created"`
	Updated  time.Time       `json:"updated"`
	Version  int64           `json:"version"`
	Notes    string          `json:"notes"`
	Imports  []ChatImport    `json:"imports"`
	Working  []agent.Message `json:"-"`
	Messages []ChatMessage   `json:"messages,omitempty"`
	Results  []ChatResult    `json:"results,omitempty"`
}
type ChatMessage struct {
	ID int64 `json:"id"`
	agent.Message
	Created time.Time `json:"created"`
}
type ChatResult struct {
	ID            int64          `json:"id"`
	MessageID     int64          `json:"message_id"`
	Content       string         `json:"content"`
	Sources       []agent.Source `json:"sources"`
	Partial       bool           `json:"partial"`
	OpenQuestions string         `json:"open_questions"`
	Created       time.Time      `json:"created"`
}

func (s *Store) Chats(ctx context.Context, userID int64, before int64) ([]Chat, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,title,created,updated,version FROM chats WHERE user_id=? AND (?=0 OR id<?) ORDER BY id DESC LIMIT 100`, userID, before, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	chats := []Chat{}
	for rows.Next() {
		var c Chat
		var created, updated int64
		if err := rows.Scan(&c.ID, &c.Title, &created, &updated, &c.Version); err != nil {
			return nil, err
		}
		c.Created = time.Unix(created, 0).UTC()
		c.Updated = time.Unix(updated, 0).UTC()
		chats = append(chats, c)
	}
	return chats, rows.Err()
}

func (s *Store) ChatState(ctx context.Context, userID, id int64) (Chat, error) {
	var c Chat
	var created, updated int64
	var working, imports string
	err := s.db.QueryRowContext(ctx, `SELECT id,title,created,updated,version,notes,working,imports FROM chats WHERE id=? AND user_id=?`, id, userID).Scan(&c.ID, &c.Title, &created, &updated, &c.Version, &c.Notes, &working, &imports)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrChatNotFound
	}
	if err != nil {
		return c, err
	}
	if json.Unmarshal([]byte(working), &c.Working) != nil || json.Unmarshal([]byte(imports), &c.Imports) != nil {
		return c, fmt.Errorf("Recherchekontext nicht lesbar.")
	}
	c.Created = time.Unix(created, 0).UTC()
	c.Updated = time.Unix(updated, 0).UTC()
	return c, nil
}

func (s *Store) Chat(ctx context.Context, userID, id int64) (Chat, error) {
	c, err := s.ChatState(ctx, userID, id)
	if err != nil {
		return c, err
	}
	c.Messages = []ChatMessage{}
	c.Results = []ChatResult{}
	rows, err := s.db.QueryContext(ctx, `SELECT id,role,content,sources,created FROM chat_messages WHERE chat_id=? ORDER BY id`, id)
	if err != nil {
		return c, err
	}
	for rows.Next() {
		var m ChatMessage
		var sources string
		var created int64
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &sources, &created); err != nil {
			rows.Close()
			return c, err
		}
		if err := json.Unmarshal([]byte(sources), &m.Sources); err != nil {
			rows.Close()
			return c, err
		}
		m.Created = time.Unix(created, 0).UTC()
		c.Messages = append(c.Messages, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return c, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT id,message_id,content,sources,partial,open_questions,created FROM chat_results WHERE chat_id=? ORDER BY id`, id)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var result ChatResult
		var sources string
		var created int64
		if err := rows.Scan(&result.ID, &result.MessageID, &result.Content, &sources, &result.Partial, &result.OpenQuestions, &created); err != nil {
			return c, err
		}
		if err := json.Unmarshal([]byte(sources), &result.Sources); err != nil {
			return c, err
		}
		result.Created = time.Unix(created, 0).UTC()
		c.Results = append(c.Results, result)
	}
	return c, rows.Err()
}

func (s *Store) CreateChat(ctx context.Context, userID int64, title string, working []agent.Message, imports []ChatImport, now time.Time) (Chat, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Neue Recherche"
	}
	if utf8.RuneCountInString(title) > 120 || strings.ContainsAny(title, "\x00\r\n") {
		return Chat{}, fmt.Errorf("Chat-Titel ungültig (höchstens 120 Zeichen).")
	}
	if working == nil {
		working = []agent.Message{}
	}
	if imports == nil {
		imports = []ChatImport{}
	}
	data, _ := json.Marshal(working)
	parents, _ := json.Marshal(imports)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Chat{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO chats(user_id,title,created,updated,working,imports) VALUES(?,?,?,?,?,?)`, userID, title, now.Unix(), now.Unix(), string(data), string(parents))
	if err != nil {
		return Chat{}, err
	}
	id, _ := result.LastInsertId()
	for _, message := range working {
		if _, err := saveChatMessage(ctx, tx, id, message, now); err != nil {
			return Chat{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Chat{}, err
	}
	return s.Chat(ctx, userID, id)
}

func saveChatMessage(ctx context.Context, tx *sql.Tx, id int64, message agent.Message, now time.Time) (int64, error) {
	sources := message.Sources
	if sources == nil {
		sources = []agent.Source{}
	}
	data, _ := json.Marshal(sources)
	row, err := tx.ExecContext(ctx, `INSERT INTO chat_messages(chat_id,role,content,sources,created) VALUES(?,?,?,?,?)`, id, message.Role, message.Content, string(data), now.Unix())
	if err != nil {
		return 0, err
	}
	messageID, _ := row.LastInsertId()
	if message.Role == "assistant" {
		lower := strings.ToLower(message.Content)
		partial := strings.Contains(lower, "teilergebnis") || strings.Contains(lower, "unvollständig") || strings.Contains(lower, "nicht vollständig")
		questions := []string{}
		capture := false
		for _, line := range strings.Split(message.Content, "\n") {
			trimmed := strings.TrimSpace(line)
			low := strings.ToLower(trimmed)
			if strings.Contains(low, "recherche-lücken") || strings.Contains(low, "offene fragen") || strings.Contains(low, "offene teilfragen") {
				capture = true
				questions = append(questions, trimmed)
				continue
			}
			if capture {
				if trimmed == "" {
					if len(questions) > 1 {
						capture = false
					}
					continue
				}
				questions = append(questions, trimmed)
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO chat_results(chat_id,message_id,content,sources,partial,open_questions,created) VALUES(?,?,?,?,?,?,?)`, id, messageID, message.Content, string(data), partial, strings.Join(questions, "\n"), now.Unix())
	}
	return messageID, err
}

// A version check prevents overlapping tabs/requests from replacing each other's context.
// Messages, research results and working memory commit together, never partially.
func (s *Store) SaveChatTurn(ctx context.Context, userID, id, version int64, question agent.Message, answer agent.Answer, working []agent.Message, now time.Time) (int64, error) {
	data, _ := json.Marshal(working)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE chats SET working=?,version=version+1,updated=? WHERE id=? AND user_id=? AND version=?`, string(data), now.Unix(), id, userID, version)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return 0, ErrChatConflict
	}
	if _, err = saveChatMessage(ctx, tx, id, question, now); err != nil {
		return 0, err
	}
	if _, err = saveChatMessage(ctx, tx, id, agent.Message{Role: "assistant", Content: answer.Text, Sources: answer.Sources}, now); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return version + 1, nil
}

func (s *Store) UpdateChat(ctx context.Context, userID, id, version int64, title, notes string, now time.Time) (int64, error) {
	title = strings.TrimSpace(title)
	if title == "" || utf8.RuneCountInString(title) > 120 || strings.ContainsAny(title, "\x00\r\n") || len(notes) > 4000 {
		return 0, fmt.Errorf("Titel oder Notizen zu lang oder ungültig.")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE chats SET title=?,notes=?,version=version+1,updated=? WHERE id=? AND user_id=? AND version=?`, title, notes, now.Unix(), id, userID, version)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return 0, ErrChatConflict
	}
	return version + 1, nil
}
func (s *Store) DeleteChat(ctx context.Context, userID, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM chats WHERE id=? AND user_id=?`, id, userID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ErrChatNotFound
	}
	return nil
}
