-- The tribute posts carry no link: X charges more than ten times as much for a
-- post that has one, and the bot belongs in the account's profile anyway. The
-- "no store or website link in the text" warning is right for an announcement
-- and wrong for those, so the project says which it is.
ALTER TABLE projects ADD COLUMN link_in_profile BOOLEAN NOT NULL DEFAULT 0;
