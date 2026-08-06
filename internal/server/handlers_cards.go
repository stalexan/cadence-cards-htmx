package server

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"cadence-cards/internal/sm2"
	"cadence-cards/internal/store"
)

const cardsPerPage = 25 // matches the Svelte cards page

// cardTableData feeds the shared card_table.html fragment (cards list page
// and deck detail).
type cardTableData struct {
	Cards      []store.Card
	Total      int
	Page       int
	TotalPages int
	Now        time.Time

	// Current filter state (echoed into the filter controls and pagination).
	Query    store.CardListParams
	DeckID   *int64 // deck-scoped table (deck detail page) when non-nil
	Topics   []store.Topic
	Decks    []store.Deck
	Tags     []string
	BasePath string // hx-get target: /cards/table or /decks/{id}/cards
	PushURL  string // hx-push-url base: /cards or /decks/{id}
}

// cardTable builds table data from query params. deckID non-nil scopes the
// table to one deck (deck detail page).
func (s *Server) cardTable(r *http.Request, deckID *int64) (cardTableData, error) {
	userID := userFrom(r).ID
	q := r.URL.Query()

	params := store.CardListParams{
		Search:   q.Get("q"),
		Priority: q.Get("priority"),
		Tag:      q.Get("tag"),
		Sort:     q.Get("sort"),
		Dir:      q.Get("dir"),
		Page:     1,
		PerPage:  cardsPerPage,
	}
	if p, err := strconv.Atoi(q.Get("page")); err == nil && p > 0 {
		params.Page = p
	}
	if deckID != nil {
		params.DeckID = deckID
	} else {
		params.TopicID = queryInt64Ptr(r, "topicId")
		params.DeckID = queryInt64Ptr(r, "deckId")
	}
	if params.Priority != "" && !sm2.ValidPriority(params.Priority) {
		params.Priority = ""
	}

	cards, total, err := s.store.ListCards(r.Context(), userID, params)
	if err != nil {
		return cardTableData{}, err
	}

	data := cardTableData{
		Cards: cards,
		Total: total,
		Page:  params.Page,
		Now:   time.Now(),
		Query: params,
	}
	data.TotalPages = (total + cardsPerPage - 1) / cardsPerPage
	if data.TotalPages == 0 {
		data.TotalPages = 1
	}

	if deckID != nil {
		data.DeckID = deckID
		data.BasePath = "/decks/" + itoa(*deckID) + "/cards"
		data.PushURL = "/decks/" + itoa(*deckID)
	} else {
		data.BasePath = "/cards/table"
		data.PushURL = "/cards"
		// Filter dropdown data (full cards page only).
		if data.Topics, err = s.store.ListTopics(r.Context(), userID); err != nil {
			return cardTableData{}, err
		}
		if data.Decks, err = s.store.ListDecks(r.Context(), userID, nil); err != nil {
			return cardTableData{}, err
		}
		if data.Tags, err = s.store.DistinctTags(r.Context(), userID); err != nil {
			return cardTableData{}, err
		}
	}
	return data, nil
}

// StartItem and EndItem are the 1-based bounds of the current page, for the
// "Showing X to Y of Z results" summary.
func (d cardTableData) StartItem() int {
	if d.Total == 0 {
		return 0
	}
	return (d.Page-1)*cardsPerPage + 1
}

func (d cardTableData) EndItem() int {
	return min(d.Page*cardsPerPage, d.Total)
}

// PageWindow returns the page buttons to render. A positive value is a page
// number; 0 means an ellipsis. Direct port of visiblePages() in the reference's
// ui/Pagination.svelte — keep the boundaries identical, they are load-bearing
// for which pages stay visible near the ends.
func (d cardTableData) PageWindow() []int {
	total, cur := d.TotalPages, d.Page
	if total <= 7 {
		pages := make([]int, 0, total)
		for i := 1; i <= total; i++ {
			pages = append(pages, i)
		}
		return pages
	}

	pages := []int{1}
	if cur > 3 {
		pages = append(pages, 0)
	}
	for i := max(2, cur-1); i <= min(total-1, cur+1); i++ {
		pages = append(pages, i)
	}
	if cur < total-2 {
		pages = append(pages, 0)
	}
	return append(pages, total)
}

// QueryString rebuilds the current filter query with an overridden key
// (pagination links, sort toggles).
func (d cardTableData) QueryString(overrides ...string) string {
	v := url.Values{}
	set := func(key, val string) {
		if val != "" {
			v.Set(key, val)
		}
	}
	set("q", d.Query.Search)
	if d.DeckID == nil {
		if d.Query.TopicID != nil {
			set("topicId", itoa(*d.Query.TopicID))
		}
		if d.Query.DeckID != nil {
			set("deckId", itoa(*d.Query.DeckID))
		}
	}
	set("priority", d.Query.Priority)
	set("tag", d.Query.Tag)
	set("sort", d.Query.Sort)
	set("dir", d.Query.Dir)
	if d.Page > 1 {
		set("page", strconv.Itoa(d.Page))
	}
	for i := 0; i+1 < len(overrides); i += 2 {
		if overrides[i+1] == "" {
			v.Del(overrides[i])
		} else {
			v.Set(overrides[i], overrides[i+1])
		}
	}
	if enc := v.Encode(); enc != "" {
		return "?" + enc
	}
	return ""
}

// SortLink returns the query string that sorts by col, toggling direction
// when already active.
func (d cardTableData) SortLink(col string) string {
	dir := "asc"
	if d.Query.Sort == col && d.Query.Dir != "desc" {
		dir = "desc"
	}
	return d.QueryString("sort", col, "dir", dir, "page", "")
}

func (s *Server) handleCardsList(w http.ResponseWriter, r *http.Request) {
	table, err := s.cardTable(r, nil)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "cards_list", table)
}

func (s *Server) handleCardsTableFragment(w http.ResponseWriter, r *http.Request) {
	table, err := s.cardTable(r, nil)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.fragment(w, http.StatusOK, "card_table", table)
}

func (s *Server) handleDeckCardsFragment(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	// Ownership check before rendering the deck-scoped table.
	if _, err := s.store.GetDeck(r.Context(), userFrom(r).ID, id); err != nil {
		s.storeError(w, r, err)
		return
	}
	table, err := s.cardTable(r, &id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.fragment(w, http.StatusOK, "card_table", table)
}

func cardParamsFromForm(r *http.Request) store.CardParams {
	deckID, _ := strconv.ParseInt(formStr(r, "deckId"), 10, 64)
	return store.CardParams{
		DeckID:   deckID,
		Front:    formStr(r, "front"),
		Back:     formStr(r, "back"),
		Note:     formStrPtr(r, "note"),
		Priority: sm2.Priority(formStr(r, "priority")),
		Tags:     parseTags(r.FormValue("tags")),
	}
}

// cardFormData feeds cards_form.html (create) and cards_show.html (edit).
type cardFormData struct {
	Card            *store.Card
	Topics          []store.Topic
	Decks           []store.Deck
	Params          store.CardParams
	Error           string
	VersionConflict bool
	Now             time.Time
	// Editing switches cards_show from the read-only view to the edit form.
	// The reference toggles this in place; here it is a plain ?edit=1
	// navigation, so the two modes stay separately linkable.
	Editing bool
}

// SelectedTopicID is the topic owning the currently selected deck, so the
// topic select can be pre-set when the form re-renders after a validation
// error. 0 when nothing is selected yet.
func (d cardFormData) SelectedTopicID() int64 {
	for _, deck := range d.Decks {
		if deck.ID == d.Params.DeckID {
			return deck.TopicID
		}
	}
	return 0
}

// DecksForSelectedTopic narrows the deck list to the selected topic. With no
// topic chosen the deck select starts empty, matching the reference.
func (d cardFormData) DecksForSelectedTopic() []store.Deck {
	topicID := d.SelectedTopicID()
	if topicID == 0 {
		return nil
	}
	var out []store.Deck
	for _, deck := range d.Decks {
		if deck.TopicID == topicID {
			out = append(out, deck)
		}
	}
	return out
}

// handleCardDeckOptions re-renders just the deck <select> options when the
// topic select changes. The reference filters this list client-side; doing it
// server-side keeps the page free of custom JS under the strict CSP.
func (s *Server) handleCardDeckOptions(w http.ResponseWriter, r *http.Request) {
	userID := userFrom(r).ID
	decks, err := s.store.ListDecks(r.Context(), userID, queryInt64Ptr(r, "topicId"))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.fragment(w, http.StatusOK, "deck_options", deckOptionsData{
		Decks:    decks,
		Selected: r.URL.Query().Get("deckId"),
	})
}

// deckOptionsData feeds the deck_options partial.
type deckOptionsData struct {
	Decks    []store.Deck
	Selected string
}

func (s *Server) cardFormData(r *http.Request, card *store.Card, params store.CardParams, errMsg string) (cardFormData, error) {
	userID := userFrom(r).ID
	topics, err := s.store.ListTopics(r.Context(), userID)
	if err != nil {
		return cardFormData{}, err
	}
	decks, err := s.store.ListDecks(r.Context(), userID, nil)
	if err != nil {
		return cardFormData{}, err
	}
	return cardFormData{Card: card, Topics: topics, Decks: decks, Params: params, Error: errMsg, Now: time.Now()}, nil
}

func (s *Server) handleCardNewPage(w http.ResponseWriter, r *http.Request) {
	params := store.CardParams{Priority: sm2.PriorityB}
	if did := queryInt64Ptr(r, "deckId"); did != nil {
		params.DeckID = *did
	}
	data, err := s.cardFormData(r, nil, params, "")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "cards_form", data)
}

func (s *Server) handleCardCreate(w http.ResponseWriter, r *http.Request) {
	p := cardParamsFromForm(r)
	renderErr := func(status int, msg string) {
		data, err := s.cardFormData(r, nil, p, msg)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		s.render(w, r, status, "cards_form", data)
	}
	if p.Front == "" || p.Back == "" || p.DeckID == 0 {
		renderErr(http.StatusBadRequest, "Front, back, and deck are required.")
		return
	}
	if !sm2.ValidPriority(string(p.Priority)) {
		renderErr(http.StatusBadRequest, "Priority must be A, B, or C.")
		return
	}
	card, err := s.store.CreateCard(r.Context(), userFrom(r).ID, p)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			renderErr(http.StatusBadRequest, "Deck not found.")
			return
		}
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/cards/"+itoa(card.ID), http.StatusSeeOther)
}

func (s *Server) handleCardShow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	card, err := s.store.GetCard(r.Context(), userFrom(r).ID, id)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	data, err := s.cardFormData(r, &card, store.CardParams{}, "")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data.Editing = r.URL.Query().Get("edit") != ""
	s.render(w, r, http.StatusOK, "cards_show", data)
}

func (s *Server) handleCardUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	userID := userFrom(r).ID
	p := cardParamsFromForm(r)
	version := formInt(r, "version", -1)

	renderForm := func(status int, msg string, conflict bool) {
		card, err := s.store.GetCard(r.Context(), userID, id)
		if err != nil {
			s.storeError(w, r, err)
			return
		}
		data, err := s.cardFormData(r, &card, p, msg)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		data.VersionConflict = conflict
		s.render(w, r, status, "cards_show", data)
	}

	if p.Front == "" || p.Back == "" || p.DeckID == 0 || !sm2.ValidPriority(string(p.Priority)) || version < 0 {
		renderForm(http.StatusBadRequest, "Front, back, deck, and priority are required.", false)
		return
	}
	if _, err := s.store.UpdateCard(r.Context(), userID, id, version, p); err != nil {
		switch {
		case errors.Is(err, store.ErrVersionConflict):
			// Re-render with fresh data and a conflict banner.
			renderForm(http.StatusConflict, "", true)
		case errors.Is(err, store.ErrNotFound):
			http.NotFound(w, r)
		default:
			s.serverError(w, r, err)
		}
		return
	}
	http.Redirect(w, r, "/cards/"+itoa(id), http.StatusSeeOther)
}

func (s *Server) handleCardDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.store.DeleteCard(r.Context(), userFrom(r).ID, id); err != nil {
		s.storeError(w, r, err)
		return
	}
	// Return where the delete came from (deck page or cards list).
	dest := r.FormValue("redirect")
	if dest == "" || dest[0] != '/' {
		dest = "/cards"
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

func (s *Server) handleScheduleReset(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	sched, err := s.store.ResetProgress(r.Context(), userFrom(r).ID, id)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	http.Redirect(w, r, "/cards/"+itoa(sched.CardID), http.StatusSeeOther)
}
