#!/bin/bash
# Runs once on an empty MySQL data directory, as root.
set -euo pipefail

mysql -uroot -p"$MYSQL_ROOT_PASSWORD" "$MYSQL_DATABASE" <<SQL
CREATE TABLE jobs (
    id         bigint PRIMARY KEY AUTO_INCREMENT,
    state      varchar(32) NOT NULL,
    updated_at timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX jobs_state (state)
);

INSERT INTO jobs (state) VALUES
    ('queued'), ('queued'), ('running'), ('failed');

CREATE USER 'reader'@'%' IDENTIFIED BY '${APP_READER_PASSWORD}';
GRANT SELECT ON ${MYSQL_DATABASE}.* TO 'reader'@'%';
FLUSH PRIVILEGES;
SQL
