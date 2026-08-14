package server

import (
	"encoding/json"
	"net/http"

	"cadence-cards/internal/claude"
	"cadence-cards/internal/store"
)

// topicConfig loads a topic (ownership-checked) and builds its prompt config.
func (s *Server) topicConfig(r *http.Request, topicID int64) (store.Topic, claude.TopicConfig, error) {
	topic, err := s.store.GetTopic(r.Context(), userFrom(r).ID, topicID)
	if err != nil {
		return store.Topic{}, claude.TopicConfig{}, err
	}
	cfg := claude.NewTopicConfig(topic.Name, topic.TopicDescription, topic.Expertise, topic.Focus,
		topic.ContextType, topic.Example, topic.Question)
	return topic, cfg, nil
}

// parseHistory decodes the hidden chat-history input (a JSON transcript).
func parseHistory(raw string) []claude.Message {
	var msgs []claude.Message
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &msgs)
	}
	// Drop anything malformed.
	valid := msgs[:0]
	for _, m := range msgs {
		if (m.Role == "user" || m.Role == "assistant") && m.Content != "" {
			valid = append(valid, m)
		}
	}
	return valid
}

func historyJSON(msgs []claude.Message) string {
	b, _ := json.Marshal(msgs)
	return string(b)
}

func (s *Server) handleChatIndex(w http.ResponseWriter, r *http.Request) {
	topics, err := s.store.ListTopics(r.Context(), userFrom(r).ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "chat_index", topics)
}

func (s *Server) handleChatShow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "topicId")
	if !ok {
		http.NotFound(w, r)
		return
	}
	topic, _, err := s.topicConfig(r, id)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "chat_show", topic)
}

// chatExchangeData feeds the chat_messages.html fragment: the user's bubble,
// Claude's reply, and an OOB update of the hidden history input.
type chatExchangeData struct {
	UserMessage string
	Assistant   string
	IsError     bool
	HistoryJSON string
}

func (s *Server) handleChatMessage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "topicId")
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, cfg, err := s.topicConfig(r, id)
	if err != nil {
		s.storeError(w, r, err)
		return
	}

	message := formStr(r, "message")
	if message == "" {
		http.Error(w, "Message is required", http.StatusBadRequest)
		return
	}
	history := parseHistory(r.FormValue("history"))
	isFirst := len(history) == 0

	reply, err := s.ai.ChatAboutTopic(r.Context(), cfg, message, history, isFirst)
	data := chatExchangeData{UserMessage: message}
	if err != nil {
		// Keep the conversation alive with an error bubble (matches the
		// Svelte chat's failure handling), with distinct copy per failure class.
		data.Assistant = aiErrorMessage(err)
		data.IsError = true
		data.HistoryJSON = historyJSON(history)
	} else {
		data.Assistant = reply
		data.HistoryJSON = historyJSON(append(history,
			claude.Message{Role: "user", Content: message},
			claude.Message{Role: "assistant", Content: reply}))
	}
	s.fragment(w, http.StatusOK, "chat_exchange", data)
}
