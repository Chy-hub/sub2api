-- Keep the latest benchmark run observable across page reloads and workers.
ALTER TABLE ustc_speed_tasks ADD COLUMN IF NOT EXISTS progress JSONB;
