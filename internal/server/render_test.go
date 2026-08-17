package server

import (
	"io"
	"testing"
)

// Every partial rendered from Go code executes against a zero value of its
// typed data struct. This catches missing/renamed fields (which html/template
// only reports at execution time) in tests instead of at request time.
func TestPartialsExecuteAgainstZeroValues(t *testing.T) {
	_, partials, err := buildTemplates()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		data any
	}{
		{"study_card", studyCardData{}},
		{"study_progress", progressData{}},
		{"study_params_hidden", studyParams{}},
		{"grade_area", gradeAreaData{}},
		{"grade_conflict", gradeConflictData{}},
		{"session_complete", sessionCompleteData{}},
		{"study_question", studyQuestionData{}},
		{"chat_exchange", chatExchangeData{}},
		{"chat_composer", chatComposerData{}},
		{"import_result", importResultData{}},
		{"import_detect", importDetectData{}},
		{"card_preview", cardPreviewData{}},
		{"topic_form_fields", topicFieldsData{}},
		{"card_content_fields", cardContentData{}},
	}
	for _, c := range cases {
		if err := partials.ExecuteTemplate(io.Discard, c.name, c.data); err != nil {
			t.Errorf("partial %q does not execute against %T: %v", c.name, c.data, err)
		}
	}
}

func TestNewProgressData(t *testing.T) {
	cases := []struct {
		done, total, percent int
		tone                 string
	}{
		{0, 0, 0, "low"},
		{0, 10, 0, "low"},
		{4, 10, 40, "mid"},
		{7, 10, 70, "high"},
		{12, 10, 100, "high"}, // over-complete clamps
	}
	for _, c := range cases {
		p := newProgressData(c.done, c.total)
		if p.Percent != c.percent || p.Tone != c.tone {
			t.Errorf("newProgressData(%d, %d) = %d%% %q, want %d%% %q",
				c.done, c.total, p.Percent, p.Tone, c.percent, c.tone)
		}
	}
}
