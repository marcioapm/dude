-- 272_auto_task_preview.sql — automatic task previews.
--
-- projects.auto_task_preview: when dude opens (or a person reopens) a
-- pull request dude opened for one of the project's tasks, dude prepares
-- that task's preview, asleep until someone opens its URL. Off unless a
-- project's editors turn it on.
--
-- tasks.auto_task_preview: the task's own choice; NULL follows the
-- project's. It governs only that automatic preparation: a preview a
-- person or the conductor asks for starts whatever it says.
ALTER TABLE projects ADD COLUMN auto_task_preview boolean NOT NULL DEFAULT false;
ALTER TABLE tasks ADD COLUMN auto_task_preview boolean;
