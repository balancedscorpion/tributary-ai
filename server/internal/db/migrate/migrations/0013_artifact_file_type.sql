-- File artifacts are first-class records for uploaded files and connector
-- downloads. Older installs created the enum before "file" existed.

ALTER TABLE artifacts
  MODIFY COLUMN type ENUM('data','code','image','file') NOT NULL;
