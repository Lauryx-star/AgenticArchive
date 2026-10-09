package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Lauryx-star/AgenticArchive/internal/access"
	"github.com/Lauryx-star/AgenticArchive/internal/agent"
	"github.com/Lauryx-star/AgenticArchive/internal/archive"
)

func chatUser(r *http.Request) int64 {
	return r.Context().Value(sessionContextKey{}).(access.Session).User.ID
}
func chatID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		return 0, access.ErrChatNotFound
	}
	return id, nil
}
func chatError(w http.ResponseWriter, err error) {
	status := 500
	message := "Chat konnte nicht gespeichert oder geladen werden."
	if errors.Is(err, access.ErrChatNotFound) {
		status = 404
		message = err.Error()
	}
	if errors.Is(err, access.ErrChatConflict) {
		status = 409
		message = err.Error()
	}
	apiError(w, status, fmt.Errorf("%s", message))
}
func chatJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	if r.Header.Get("Content-Type") != "application/json" {
		apiError(w, 415, fmt.Errorf("application/json erforderlich."))
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		apiError(w, 400, fmt.Errorf("Ungültige Chat-Anfrage."))
		return false
	}
	return true
}

func savedChatRoutes(mux *http.ServeMux, auth *authService, archiveStore *archive.Store) {
	mux.HandleFunc("GET /api/chats", func(w http.ResponseWriter, r *http.Request) {
		before, err := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
		if r.URL.Query().Get("before") == "" {
			before = 0
			err = nil
		}
		if err != nil || before < 0 {
			apiError(w, 400, fmt.Errorf("Ungültige Seite."))
			return
		}
		chats, err := auth.store.Chats(r.Context(), chatUser(r), before)
		if err != nil {
			chatError(w, err)
			return
		}
		next := int64(0)
		if len(chats) == 100 {
			next = chats[len(chats)-1].ID
		}
		writeJSON(w, map[string]any{"chats": chats, "next_before": next})
	})
	mux.HandleFunc("POST /api/chats", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Title      string `json:"title"`
			Transcript string `json:"transcript,omitempty"`
		}
		if !chatJSON(w, r, &input) {
			return
		}
		if len([]rune(strings.TrimSpace(input.Title))) > 120 || strings.ContainsAny(input.Title, "\x00\r\n") {
			apiError(w, 400, fmt.Errorf("Ungültiger Chat-Titel."))
			return
		}
		var working []agent.Message
		if input.Transcript != "" {
			if len(input.Transcript) > 32000 {
				apiError(w, 400, fmt.Errorf("Der übernommene Verlauf ist zu lang (höchstens 32 KB)."))
				return
			}
			sources := []agent.Source{}
			seen := map[[2]int64]bool{}
			pattern := regexp.MustCompile(`\[Dokument (\d+), Seite (\d+)\]`)
			for _, match := range pattern.FindAllStringSubmatch(input.Transcript, -1) {
				id, _ := strconv.ParseInt(match[1], 10, 64)
				page, _ := strconv.ParseInt(match[2], 10, 64)
				ref := [2]int64{id, page}
				if seen[ref] {
					continue
				}
				seen[ref] = true
				doc, err := archiveStore.Document(r.Context(), id)
				if err == nil && page > 0 && page <= int64(doc.Pages) {
					sources = append(sources, agent.Source{DocumentID: id, Page: int(page), Path: doc.Path, Revision: doc.Fingerprint, Imported: true})
				}
			}
			working = []agent.Message{{Role: "user", Content: "Übernommener früherer Chat. Die enthaltenen Aussagen und Verweise wurden bei der Übernahme nicht neu geprüft."}, {Role: "assistant", Content: input.Transcript, Sources: sources}}
			if err := agent.ValidateRequest(agent.Request{Messages: append(append([]agent.Message{}, working...), agent.Message{Role: "user", Content: "Fortsetzung"})}); err != nil {
				apiError(w, 400, err)
				return
			}
		}
		chat, err := auth.store.CreateChat(r.Context(), chatUser(r), input.Title, working, nil, time.Now())
		if err != nil {
			chatError(w, err)
			return
		}
		writeJSON(w, chat)
	})
	mux.HandleFunc("GET /api/chats/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := chatID(r)
		if err != nil {
			chatError(w, err)
			return
		}
		chat, err := auth.store.Chat(r.Context(), chatUser(r), id)
		if err != nil {
			chatError(w, err)
			return
		}
		// Historic answers remain readable even when indexed documents change.
		refresh := func(sources []agent.Source) {
			for i, source := range sources {
				doc, err := archiveStore.Document(r.Context(), source.DocumentID)
				sources[i].Unavailable = err != nil || source.Page < 1 || source.Page > doc.Pages || (source.Revision != "" && source.Revision != doc.Fingerprint)
			}
		}
		for i := range chat.Messages {
			refresh(chat.Messages[i].Sources)
		}
		for i := range chat.Results {
			refresh(chat.Results[i].Sources)
		}
		writeJSON(w, chat)
	})
	mux.HandleFunc("PATCH /api/chats/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := chatID(r)
		if err != nil {
			chatError(w, err)
			return
		}
		var input struct {
			Title   string `json:"title"`
			Notes   string `json:"notes"`
			Version int64  `json:"version"`
		}
		if !chatJSON(w, r, &input) {
			return
		}
		if input.Version < 1 || strings.TrimSpace(input.Title) == "" || len([]rune(input.Title)) > 120 || strings.ContainsAny(input.Title, "\x00\r\n") || len(input.Notes) > 4000 {
			apiError(w, 400, fmt.Errorf("Titel oder Notizen ungültig oder zu lang."))
			return
		}
		if _, err := auth.store.ChatState(r.Context(), chatUser(r), id); err != nil {
			chatError(w, err)
			return
		}
		version, err := auth.store.UpdateChat(r.Context(), chatUser(r), id, input.Version, input.Title, input.Notes, time.Now())
		if err != nil {
			chatError(w, err)
			return
		}
		writeJSON(w, map[string]any{"version": version})
	})
	mux.HandleFunc("DELETE /api/chats/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := chatID(r)
		if err != nil {
			chatError(w, err)
			return
		}
		if err := auth.store.DeleteChat(r.Context(), chatUser(r), id); err != nil {
			chatError(w, err)
			return
		}
		writeJSON(w, map[string]bool{"deleted": true})
	})
	mux.HandleFunc("POST /api/chats/merge", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			IDs   []int64 `json:"ids"`
			Title string  `json:"title"`
		}
		if !chatJSON(w, r, &input) {
			return
		}
		if len(input.IDs) < 2 || len(input.IDs) > 5 {
			apiError(w, 400, fmt.Errorf("Wähle zwei bis fünf Chats aus."))
			return
		}
		working := []agent.Message{}
		imports := []access.ChatImport{}
		seen := map[int64]bool{}
		for _, id := range input.IDs {
			if id < 1 || seen[id] {
				apiError(w, 400, fmt.Errorf("Ungültige Chat-Auswahl."))
				return
			}
			seen[id] = true
			chat, err := auth.store.ChatState(r.Context(), chatUser(r), id)
			if err != nil {
				chatError(w, err)
				return
			}
			if len(chat.Working) == 0 {
				apiError(w, 400, fmt.Errorf("Chat %q enthält noch keine Ergebnisse.", chat.Title))
				return
			}
			var content strings.Builder
			for _, message := range chat.Working {
				content.WriteString(message.Role + ":\n" + message.Content + "\n\n")
			}
			working = append(working, agent.Message{Role: "user", Content: "Übernommene Recherche aus Chat: " + chat.Title + "\nOffene Fragen und Hinweise: " + chat.Notes}, agent.Message{Role: "assistant", Content: content.String(), Sources: chatHistorySources(chat.Working)})
			imports = append(imports, access.ChatImport{ID: id, Title: chat.Title, Version: chat.Version})
		}
		test := append(append([]agent.Message{}, working...), agent.Message{Role: "user", Content: "Führe die bisherigen Rechercheergebnisse zusammen."})
		if err := agent.ValidateRequest(agent.Request{Messages: test, Mode: "summary"}); err != nil {
			apiError(w, 400, fmt.Errorf("Die Auswahl enthält zu viel Recherchekontext. Fasse einzelne Chats zuerst zusammen oder wähle weniger Chats aus."))
			return
		}
		if input.Title == "" {
			input.Title = "Zusammengeführte Recherche"
		}
		if len([]rune(input.Title)) > 120 || strings.ContainsAny(input.Title, "\x00\r\n") {
			apiError(w, 400, fmt.Errorf("Ungültiger Chat-Titel."))
			return
		}
		chat, err := auth.store.CreateChat(r.Context(), chatUser(r), input.Title, working, imports, time.Now())
		if err != nil {
			chatError(w, err)
			return
		}
		writeJSON(w, chat)
	})
}
