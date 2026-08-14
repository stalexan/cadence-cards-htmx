package server

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"cadence-cards/internal/claude"
	"cadence-cards/internal/sm2"
	"cadence-cards/internal/store"
)

// studyParams are the session parameters carried in the query string of every
// study fragment request — the server stays stateless and the session
// survives refresh/back. All repeated deckIds values are honored (the fix over
// the source's .get('deckIds')).
type studyParams struct {
	TopicID    int64
	DeckIDs    []int64
	Priority   string
	IncludeNew bool
	Limit      int // 0 = no limit
	Total      int // due count at session start (progress bar)
	Completed  int
	ScheduleID int64 // current card; 0 = pick a fresh one
}

// parseStudyParams reads session parameters from url.Values, covering both
// the query string (fragment GETs) and a parsed form (the grade POST) with one
// parser. All repeated deckIds values are honored — collapsing to the first
// value was the source app's bug.
func parseStudyParams(v url.Values, topicID int64) studyParams {
	p := studyParams{
		TopicID:    topicID,
		Priority:   v.Get("priority"),
		IncludeNew: v.Get("includeNew") != "false",
	}
	for _, s := range v["deckIds"] {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			p.DeckIDs = append(p.DeckIDs, n)
		}
	}
	if !sm2.ValidPriority(p.Priority) {
		p.Priority = ""
	}
	if n, err := strconv.Atoi(v.Get("limit")); err == nil && n > 0 {
		p.Limit = n
	}
	if n, err := strconv.Atoi(v.Get("total")); err == nil && n >= 0 {
		p.Total = n
	}
	if n, err := strconv.Atoi(v.Get("completed")); err == nil && n >= 0 {
		p.Completed = n
	}
	if n, err := strconv.ParseInt(v.Get("scheduleId"), 10, 64); err == nil && n > 0 {
		p.ScheduleID = n
	}
	return p
}

func (p studyParams) filter() store.StudyFilter {
	return store.StudyFilter{DeckIDs: p.DeckIDs, Priority: p.Priority, IncludeNew: p.IncludeNew}
}

// queryValues encodes the session parameters (with an explicit completed
// value). scheduleId is deliberately not included — the URL builders that
// resume the current card add it themselves.
func (p studyParams) queryValues(completed int) url.Values {
	v := url.Values{}
	for _, id := range p.DeckIDs {
		v.Add("deckIds", itoa(id))
	}
	if p.Priority != "" {
		v.Set("priority", p.Priority)
	}
	if !p.IncludeNew {
		v.Set("includeNew", "false")
	}
	if p.Limit > 0 {
		v.Set("limit", strconv.Itoa(p.Limit))
	}
	v.Set("total", strconv.Itoa(p.Total))
	v.Set("completed", strconv.Itoa(completed))
	return v
}

// NextURL is the fragment URL that fetches the next card. It never carries
// scheduleId: Next/Skip and the conflict refetch must pick a fresh card.
func (p studyParams) NextURL(completed int) string {
	return "/study/" + itoa(p.TopicID) + "/next?" + p.queryValues(completed).Encode()
}

// ResumeURL is NextURL plus the current card — used only by the session
// page's initial loader, so a refresh re-serves the same card instead of
// burning a fresh question on a random one.
func (p studyParams) ResumeURL() string {
	v := p.queryValues(p.Completed)
	if p.ScheduleID != 0 {
		v.Set("scheduleId", itoa(p.ScheduleID))
	}
	return "/study/" + itoa(p.TopicID) + "/next?" + v.Encode()
}

// SessionURL is the full-page session URL with the given progress and, when a
// card is being served, its scheduleId. handleStudyNext pushes it via the
// HX-Push-Url response header — the server is the only party that knows which
// card it picked, so a template attribute can't build this URL.
func (p studyParams) SessionURL(completed int) string {
	v := p.queryValues(completed)
	if p.ScheduleID != 0 {
		v.Set("scheduleId", itoa(p.ScheduleID))
	}
	return "/study/" + itoa(p.TopicID) + "?" + v.Encode()
}

// studyIndexData feeds study_index.html.
type studyIndexData struct {
	Topics    []store.Topic
	DueCounts map[int64]store.DueByPriority
}

func (s *Server) handleStudyIndex(w http.ResponseWriter, r *http.Request) {
	userID := userFrom(r).ID
	topics, err := s.store.ListTopics(r.Context(), userID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	counts, err := s.store.TopicDueCounts(r.Context(), userID, time.Now())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "study_index", studyIndexData{Topics: topics, DueCounts: counts})
}

// studySetupData feeds study_setup.html.
type studySetupData struct {
	Topic store.Topic
	Decks []store.Deck
	Stats store.StudyStats
}

func (s *Server) handleStudySetup(w http.ResponseWriter, r *http.Request) {
	topicID, ok := pathID(r, "topicId")
	if !ok {
		http.NotFound(w, r)
		return
	}
	userID := userFrom(r).ID
	topic, err := s.store.GetTopic(r.Context(), userID, topicID)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	decks, err := s.store.ListDecks(r.Context(), userID, &topicID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	stats, err := s.store.StudyStats(r.Context(), userID, topicID, nil, time.Now())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "study_setup", studySetupData{Topic: topic, Decks: decks, Stats: stats})
}

// studySessionData feeds the study_session.html shell.
type studySessionData struct {
	Topic    store.Topic
	Params   studyParams
	Progress progressData
}

func (s *Server) handleStudySession(w http.ResponseWriter, r *http.Request) {
	topicID, ok := pathID(r, "topicId")
	if !ok {
		http.NotFound(w, r)
		return
	}
	userID := userFrom(r).ID
	topic, err := s.store.GetTopic(r.Context(), userID, topicID)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	p := parseStudyParams(r.URL.Query(), topicID)
	if p.Total == 0 {
		// Session start: pin the progress-bar total (capped by limit).
		due, err := s.store.CountDue(r.Context(), userID, topicID, p.filter(), time.Now())
		if err != nil {
			s.storeError(w, r, err)
			return
		}
		p.Total = due
		if p.Limit > 0 && p.Limit < due {
			p.Total = p.Limit
		}
	}
	s.render(w, r, http.StatusOK, "study_session", studySessionData{
		Topic:    topic,
		Params:   p,
		Progress: newProgressData(p.Completed, p.Total),
	})
}

// studyCardData feeds the study_card.html fragment. Messages restores an
// existing chat when the same card is re-served after a refresh; it is empty
// for a freshly picked card. All nested partial data (progress bar, grade
// area, composer) is typed and built here rather than assembled with dict in
// the template.
type studyCardData struct {
	Item      store.StudyItem
	Params    studyParams
	Messages  []store.ChatMessage
	Progress  progressData
	GradeArea gradeAreaData
	Composer  chatComposerData
}

// sessionCompleteData feeds session_complete.html.
type sessionCompleteData struct {
	Completed int
	TopicID   int64
	Progress  progressData
}

// progressData feeds the study_progress partial (the OOB progress-bar update).
type progressData struct {
	Done    int
	Total   int
	Percent int
	// Tone picks the fill class; the reference deepens the indigo as the
	// session advances, and the CSP forbids an inline style, so the band is
	// chosen server-side and applied as a class.
	Tone string
}

func newProgressData(done, total int) progressData {
	p := progressData{Done: done, Total: total, Tone: "low"}
	if total > 0 {
		p.Percent = min(100, done*100/total)
		switch {
		case p.Percent >= 66:
			p.Tone = "high"
		case p.Percent >= 33:
			p.Tone = "mid"
		}
	}
	return p
}

func (s *Server) handleStudyNext(w http.ResponseWriter, r *http.Request) {
	topicID, ok := pathID(r, "topicId")
	if !ok {
		http.NotFound(w, r)
		return
	}
	userID := userFrom(r).ID
	p := parseStudyParams(r.URL.Query(), topicID)
	now := time.Now()

	complete := func() {
		p.ScheduleID = 0
		w.Header().Set("HX-Push-Url", p.SessionURL(p.Completed))
		s.fragment(w, http.StatusOK, "session_complete", sessionCompleteData{
			Completed: p.Completed,
			TopicID:   topicID,
			// Always a full bar — the session is done regardless of counts.
			Progress: progressData{Done: p.Completed, Total: p.Completed, Percent: 100, Tone: "high"},
		})
	}
	// Card-limit reached -> session complete.
	if p.Limit > 0 && p.Completed >= p.Limit {
		complete()
		return
	}

	// Resume: a scheduleId in the URL (put there by this handler's own
	// HX-Push-Url) means a refresh mid-card — re-serve that card with its
	// transcript instead of burning a fresh question on a random one. A card
	// that is gone, foreign, from another topic, or no longer due (e.g.
	// refreshed after grading) falls through to a fresh pick.
	var data studyCardData
	if p.ScheduleID != 0 {
		resumed, itemTopicID, err := s.store.GetStudyItem(r.Context(), userID, p.ScheduleID)
		switch {
		case err != nil && !errors.Is(err, store.ErrNotFound):
			s.serverError(w, r, err)
			return
		case err == nil && itemTopicID == topicID && resumed.State.IsDue(now):
			conv, msgs, err := s.store.LatestConversationForSchedule(r.Context(), userID, p.ScheduleID)
			if err != nil {
				s.serverError(w, r, err)
				return
			}
			data.Item = resumed
			data.Messages = msgs
			data.Composer.ConversationID = conv.ID
		}
	}
	if data.Item.ScheduleID == 0 {
		item, err := s.store.NextDue(r.Context(), userID, topicID, p.filter(), now)
		if err != nil {
			s.storeError(w, r, err)
			return
		}
		if item == nil {
			// Nothing due (replaces the source's 404-means-done convention).
			complete()
			return
		}
		data.Item = *item
	}

	p.ScheduleID = data.Item.ScheduleID
	data.Params = p
	data.Progress = newProgressData(p.Completed, p.Total)
	data.GradeArea = gradeAreaData{ScheduleID: data.Item.ScheduleID, Version: data.Item.Version, Params: p}
	data.Composer.PostURL = "/study/" + itoa(topicID) + "/chat"
	data.Composer.Placeholder = "Type your answer…"
	data.Composer.ScheduleID = data.Item.ScheduleID
	w.Header().Set("HX-Push-Url", p.SessionURL(p.Completed))
	s.fragment(w, http.StatusOK, "study_card", data)
}

// studyItemFor loads and ownership-checks the schedule referenced by the
// scheduleId form value, ensuring it belongs to the URL topic.
func (s *Server) studyItemFor(w http.ResponseWriter, r *http.Request) (store.StudyItem, claude.TopicConfig, bool) {
	topicID, ok := pathID(r, "topicId")
	if !ok {
		http.NotFound(w, r)
		return store.StudyItem{}, claude.TopicConfig{}, false
	}
	scheduleID := int64(formInt(r, "scheduleId", 0))
	if scheduleID == 0 {
		http.Error(w, "scheduleId is required", http.StatusBadRequest)
		return store.StudyItem{}, claude.TopicConfig{}, false
	}
	item, itemTopicID, err := s.store.GetStudyItem(r.Context(), userFrom(r).ID, scheduleID)
	if err != nil {
		s.storeError(w, r, err)
		return store.StudyItem{}, claude.TopicConfig{}, false
	}
	if itemTopicID != topicID {
		http.NotFound(w, r)
		return store.StudyItem{}, claude.TopicConfig{}, false
	}
	_, cfg, err := s.topicConfig(r, topicID)
	if err != nil {
		s.storeError(w, r, err)
		return store.StudyItem{}, claude.TopicConfig{}, false
	}
	return item, cfg, true
}

// studyQuestionData feeds the question fragment (assistant bubble + OOB
// conversation-ID update) that replaces the "Claude is preparing a question"
// stub.
type studyQuestionData struct {
	Question       string
	IsError        bool
	ConversationID int64
}

func (s *Server) handleStudyQuestion(w http.ResponseWriter, r *http.Request) {
	item, cfg, ok := s.studyItemFor(w, r)
	if !ok {
		return
	}
	topicID, _ := pathID(r, "topicId") // already validated by studyItemFor
	userID := userFrom(r).ID

	// The nightly batch may already have generated this card's question;
	// consume it and skip the live API call entirely.
	question, pregenerated, err := s.store.TakeGeneratedQuestion(r.Context(), userID, item.ScheduleID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !pregenerated {
		// Direction-aware: Claude sees prompt/answer as front/back.
		card := claude.CardContent{Front: item.Prompt, Back: item.Answer, Note: item.Note}
		question, err = s.ai.GenerateQuestion(r.Context(), cfg, card)
		if err != nil {
			// Distinct copy per failure class, and the card's own prompt so
			// the user can keep studying without the assistant (same fallback
			// shape the Svelte UI used). No conversation row: the chat
			// handler creates one lazily on the first successful exchange.
			s.fragment(w, http.StatusOK, "study_question", studyQuestionData{
				Question: aiErrorMessage(err) + " You can practice with the following prompt: " + item.Prompt,
				IsError:  true,
			})
			return
		}
	} else {
		slog.Info("served pre-generated question", "scheduleId", item.ScheduleID)
	}

	convID, err := s.store.CreateConversation(r.Context(), userID, topicID, &item.ScheduleID,
		[]store.ChatMessage{{Role: "assistant", Content: question}})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.fragment(w, http.StatusOK, "study_question", studyQuestionData{
		Question:       question,
		ConversationID: convID,
	})
}

func (s *Server) handleStudyChat(w http.ResponseWriter, r *http.Request) {
	item, cfg, ok := s.studyItemFor(w, r)
	if !ok {
		return
	}
	answer := formStr(r, "message")
	if answer == "" {
		http.Error(w, "Message is required", http.StatusBadRequest)
		return
	}
	topicID, _ := pathID(r, "topicId") // already validated by studyItemFor
	convID, history, ok := s.conversationFor(w, r, topicID, &item.ScheduleID)
	if !ok {
		return
	}

	card := claude.CardContent{Front: item.Prompt, Back: item.Answer, Note: item.Note}
	reply, err := s.ai.ChatAboutQuestion(r.Context(), cfg, card, answer, toClaudeMessages(history))
	data := chatExchangeData{UserMessage: answer, ConversationID: convID}
	if err != nil {
		// A failed exchange leaves the stored transcript untouched.
		data.Assistant = aiErrorMessage(err)
		data.IsError = true
		s.fragment(w, http.StatusOK, "chat_exchange", data)
		return
	}

	userID := userFrom(r).ID
	exchange := []store.ChatMessage{
		{Role: "user", Content: answer},
		{Role: "assistant", Content: reply},
	}
	if convID == 0 {
		// Question generation failed earlier (or predates conversations):
		// open the conversation on the first successful exchange.
		convID, err = s.store.CreateConversation(r.Context(), userID, topicID, &item.ScheduleID, exchange)
	} else {
		err = s.store.AppendChatMessages(r.Context(), userID, convID, exchange)
	}
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	data.Assistant = reply
	data.ConversationID = convID
	s.fragment(w, http.StatusOK, "chat_exchange", data)
}

// gradeAreaData feeds grade_area.html in both ungraded and graded states.
type gradeAreaData struct {
	ScheduleID int64
	Version    int
	Params     studyParams
	Graded     bool
	Grade      string
	// Interval after grading (shown as "next review in N days").
	NewInterval int
}

// gradeConflictData feeds the 409 conflict fragment.
type gradeConflictData struct {
	Params studyParams
}

func (s *Server) handleStudyGrade(w http.ResponseWriter, r *http.Request) {
	scheduleID, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	userID := userFrom(r).ID
	r.ParseForm()
	topicID := int64(formInt(r, "topicId", 0))
	p := parseStudyParams(r.Form, topicID)

	grade := formStr(r, "grade")
	version := formInt(r, "version", -1)
	if !sm2.ValidGrade(grade) || version < 0 {
		http.Error(w, "Valid grade and version are required", http.StatusBadRequest)
		return
	}

	// A regrade rewinds the schedule to the state its last review started from
	// before applying the new grade, so changing an answer never compounds.
	record := s.store.RecordReview
	if formStr(r, "regrade") == "1" {
		record = s.store.RegradeReview
	}
	sched, err := record(r.Context(), userID, scheduleID, sm2.Grade(grade), version, time.Now())
	if err != nil {
		switch {
		// No snapshot means the client's view of the card is stale; the
		// conflict fragment already refetches it.
		case errors.Is(err, store.ErrVersionConflict), errors.Is(err, store.ErrNoPreviousGrade):
			// 409 whose body swaps in a self-refreshing grade area plus an
			// OOB notice in the chat (htmx-config allows 409 swaps).
			s.fragment(w, http.StatusConflict, "grade_conflict", gradeConflictData{Params: p})
		case errors.Is(err, store.ErrNotFound):
			http.NotFound(w, r)
		default:
			s.serverError(w, r, err)
		}
		return
	}

	s.fragment(w, http.StatusOK, "grade_area", gradeAreaData{
		ScheduleID:  scheduleID,
		Version:     sched.Version,
		Params:      p,
		Graded:      true,
		Grade:       grade,
		NewInterval: sched.Interval,
	})
}
