-- An announcement that goes to chat only, without ReSkate 2.0.2's card.
ALTER TABLE announcements ADD COLUMN chat_only INTEGER NOT NULL DEFAULT 0;
