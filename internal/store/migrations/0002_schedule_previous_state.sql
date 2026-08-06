-- Snapshot of the SM-2 state a schedule had *before* its most recent review,
-- so a grade can be changed while the card is still on screen: the new grade is
-- applied to this baseline rather than compounding on the graded result.
-- All nullable; prev_easiness IS NOT NULL is the "a baseline exists" marker
-- (the other four are legitimately NULL/zero for a first-ever review).
ALTER TABLE schedules ADD COLUMN prev_easiness  REAL;
ALTER TABLE schedules ADD COLUMN prev_interval  INTEGER;
ALTER TABLE schedules ADD COLUMN prev_rep_count INTEGER;
ALTER TABLE schedules ADD COLUMN prev_grade     TEXT;
ALTER TABLE schedules ADD COLUMN prev_last_seen TEXT;
