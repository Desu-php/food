-- PostgreSQL rejects rollback if new notification kinds exist, preserving data.
ALTER TABLE notifications DROP CONSTRAINT notifications_kind_check;
ALTER TABLE notifications ADD CONSTRAINT notifications_kind_check CHECK(kind IN ('OrderCreated','OrderCancelled'));
