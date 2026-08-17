-- Provenance for a topic: who wrote it, under what terms it may be reused, and
-- where the original lives. Populated from the Provenance block of a topic YAML
-- file (internal/yamlio/topic.go) and re-emitted on export, so attribution
-- survives a round trip through the app instead of being dropped at import.
--
-- Deliberately outside the prompt-config columns above them: nothing here ever
-- reaches internal/claude. All three are free text and nullable — an
-- unattributed topic is the normal case.
ALTER TABLE topics ADD COLUMN author  TEXT;
ALTER TABLE topics ADD COLUMN license TEXT;
ALTER TABLE topics ADD COLUMN source  TEXT;
