package server

import (
	"errors"
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
}

func parseStudyParams(r *http.Request, topicID int64) studyParams {
	q := r.URL.Query()
	p := studyParams{
		TopicID:    topicID,
		DeckIDs:    queryInt64s(r, "deckIds"),
		Priority:   q.Get("priority"),
		IncludeNew: q.Get("includeNew") != "false",
	}
	if !sm2.ValidPriority(p.Priority) {
		p.Priority = ""
	}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
		p.Limit = n
	}
	if n, err := strconv.Atoi(q.Get("total")); err == nil && n >= 0 {
		p.Total = n
	}
	if n, err := strconv.Atoi(q.Get("completed")); err == nil && n >= 0 {
		p.Completed = n
	}
	return p
}

func (p studyParams) filter() store.StudyFilter {
	return store.StudyFilter{DeckIDs: p.DeckIDs, Priority: p.Priority, IncludeNew: p.IncludeNew}
}

// query encodes the session parameters (with an explicit completed value).
func (p studyParams) query(completed int) string {
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
	return v.Encode()
}

// NextURL is the fragment URL that fetches the next card.
func (p studyParams) NextURL(completed int) string {
	return "/study/" + itoa(p.TopicID) + "/next?" + p.query(completed)
}

// SessionURL is the full-page session URL with the given progress. The Next
// Card/Skip buttons push it into the address bar, so a refresh restores the
// pinned total and completed count instead of restarting the progress bar.
func (p studyParams) SessionURL(completed int) string {
	return "/study/" + itoa(p.TopicID) + "?" + p.query(completed)
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
	Topic  store.Topic
	Params studyParams
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
	p := parseStudyParams(r, topicID)
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
	s.render(w, r, http.StatusOK, "study_session", studySessionData{Topic: topic, Params: p})
}

// studyCardData feeds the study_card.html fragment.
type studyCardData struct {
	Item        store.StudyItem
	Params      studyParams
	HistoryJSON string
}

// sessionCompleteData feeds session_complete.html.
type sessionCompleteData struct {
	Completed int
	TopicID   int64
}

// progressData feeds the OOB progress-bar update.
type progressData struct {
	Done  int
	Total int
}

func (s *Server) handleStudyNext(w http.ResponseWriter, r *http.Request) {
	topicID, ok := pathID(r, "topicId")
	if !ok {
		http.NotFound(w, r)
		return
	}
	userID := userFrom(r).ID
	p := parseStudyParams(r, topicID)

	complete := func() {
		s.fragment(w, http.StatusOK, "session_complete", sessionCompleteData{Completed: p.Completed, TopicID: topicID})
	}
	// Card-limit reached -> session complete.
	if p.Limit > 0 && p.Completed >= p.Limit {
		complete()
		return
	}
	item, err := s.store.NextDue(r.Context(), userID, topicID, p.filter(), time.Now())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	if item == nil {
		// Nothing due (replaces the source's 404-means-done convention).
		complete()
		return
	}
	s.fragment(w, http.StatusOK, "study_card", studyCardData{Item: *item, Params: p, HistoryJSON: "[]"})
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
// history update) that replaces the "Claude is preparing a question" stub.
type studyQuestionData struct {
	Question    string
	IsError     bool
	HistoryJSON string
}

func (s *Server) handleStudyQuestion(w http.ResponseWriter, r *http.Request) {
	item, cfg, ok := s.studyItemFor(w, r)
	if !ok {
		return
	}
	// Direction-aware: Claude sees prompt/answer as front/back.
	card := claude.CardContent{Front: item.Prompt, Back: item.Answer, Note: item.Note}
	question, err := s.ai.GenerateQuestion(r.Context(), cfg, card)
	data := studyQuestionData{}
	if err != nil {
		// Distinct copy per failure class, and the card's own prompt so the
		// user can keep studying without the assistant (same fallback shape
		// the Svelte UI used).
		data.Question = aiErrorMessage(err) + " You can practice with the following prompt: " + item.Prompt
		data.IsError = true
		data.HistoryJSON = "[]"
	} else {
		data.Question = question
		data.HistoryJSON = historyJSON([]claude.Message{{Role: "assistant", Content: question}})
	}
	s.fragment(w, http.StatusOK, "study_question", data)
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
	history := parseHistory(r.FormValue("history"))

	card := claude.CardContent{Front: item.Prompt, Back: item.Answer, Note: item.Note}
	reply, err := s.ai.ChatAboutQuestion(r.Context(), cfg, card, answer, history)
	data := chatExchangeData{UserMessage: answer}
	if err != nil {
		data.Assistant = aiErrorMessage(err)
		data.IsError = true
		data.HistoryJSON = historyJSON(history)
	} else {
		data.Assistant = reply
		data.HistoryJSON = historyJSON(append(history,
			claude.Message{Role: "user", Content: answer},
			claude.Message{Role: "assistant", Content: reply}))
	}
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
	topicID := int64(formInt(r, "topicId", 0))
	p := parseStudyParamsFromForm(r, topicID)

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

// parseStudyParamsFromForm reads session params from POSTed form values
// (the grade form carries them as hidden inputs).
func parseStudyParamsFromForm(r *http.Request, topicID int64) studyParams {
	r.ParseForm()
	p := studyParams{TopicID: topicID, IncludeNew: r.Form.Get("includeNew") != "false"}
	for _, v := range r.Form["deckIds"] {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			p.DeckIDs = append(p.DeckIDs, n)
		}
	}
	if pr := r.Form.Get("priority"); sm2.ValidPriority(pr) {
		p.Priority = pr
	}
	if n, err := strconv.Atoi(r.Form.Get("limit")); err == nil && n > 0 {
		p.Limit = n
	}
	if n, err := strconv.Atoi(r.Form.Get("total")); err == nil && n >= 0 {
		p.Total = n
	}
	if n, err := strconv.Atoi(r.Form.Get("completed")); err == nil && n >= 0 {
		p.Completed = n
	}
	return p
}
