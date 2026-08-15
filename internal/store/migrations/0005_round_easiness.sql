-- Easiness is now kept to two decimals (sm2.RoundEasiness): the SM-2 increments
-- are binary-inexact, so rows graded before this migration hold values like
-- 2.8000000000000003. Round the stored history to match what new reviews write.
-- Two decimals is finer than the smallest increment the formula produces (0.02),
-- so no schedule changes its due date.
UPDATE schedules SET easiness = ROUND(easiness, 2)
 WHERE easiness <> ROUND(easiness, 2);

UPDATE schedules SET prev_easiness = ROUND(prev_easiness, 2)
 WHERE prev_easiness IS NOT NULL AND prev_easiness <> ROUND(prev_easiness, 2);
