-- Separate the running schedule snapshot from edits to future task settings.
ALTER TABLE ustc_speed_tasks ADD COLUMN IF NOT EXISTS lease_schedule JSONB;
