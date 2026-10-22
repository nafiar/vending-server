-- Schema v3 from the PRD data dictionary: eight tables, plus sessions for logins.
-- Money is a whole number of rupiah; every time is DATETIME(3) in UTC.
-- Rule for this file: every statement ends with a semicolon at the end of a line.

-- Staff accounts: admins and operators. Customers have no accounts.
CREATE TABLE users (
  id            BIGINT UNSIGNED  NOT NULL AUTO_INCREMENT,
  name          VARCHAR(100)     NOT NULL,
  username      VARCHAR(50)      NOT NULL,
  password_hash VARCHAR(255)     NOT NULL COMMENT 'Argon2id, PHC string format',
  role          ENUM('ADMIN','OPERATOR') NOT NULL,
  is_active     BOOLEAN          NOT NULL DEFAULT TRUE,
  failed_logins TINYINT UNSIGNED NOT NULL DEFAULT 0,
  locked_until  DATETIME(3)      NULL COMMENT 'set for 5 minutes after 5 wrong passwords (FR-13)',
  created_at    DATETIME(3)      NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_users_username (username)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- One row per vending machine in the fleet.
CREATE TABLE machines (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  code            VARCHAR(20)     NOT NULL,
  name            VARCHAR(100)    NOT NULL,
  location        VARCHAR(150)    NULL,
  api_key_hash    CHAR(64) CHARACTER SET ascii NOT NULL COMMENT 'SHA-256 of the machine key; the key is shown once',
  catalog_version INT UNSIGNED    NOT NULL DEFAULT 1 COMMENT 'raised whenever what the machine shows changes (BR-09)',
  is_active       BOOLEAN         NOT NULL DEFAULT TRUE,
  last_seen_at    DATETIME(3)     NULL,
  created_at      DATETIME(3)     NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_machines_code (code),
  UNIQUE KEY uq_machines_api_key_hash (api_key_hash)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Products; one price for every machine. Archived, never deleted (BR-10).
CREATE TABLE products (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  name        VARCHAR(100)    NOT NULL,
  description TEXT            NULL,
  price       INT UNSIGNED    NOT NULL,
  image_url   VARCHAR(255)    NULL,
  is_active   BOOLEAN         NOT NULL DEFAULT TRUE,
  created_at  DATETIME(3)     NOT NULL,
  updated_at  DATETIME(3)     NOT NULL,
  PRIMARY KEY (id),
  CONSTRAINT ck_products_price CHECK (price > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- One row per physical coil, created when the machine is registered.
CREATE TABLE slots (
  id              BIGINT UNSIGNED  NOT NULL AUTO_INCREMENT,
  machine_id      BIGINT UNSIGNED  NOT NULL,
  slot_number     TINYINT UNSIGNED NOT NULL,
  product_id      BIGINT UNSIGNED  NULL COMMENT 'the product in the coil now',
  next_product_id BIGINT UNSIGNED  NULL COMMENT 'planned product, applied at the next visit (D6)',
  capacity        TINYINT UNSIGNED NOT NULL,
  qty             TINYINT UNSIGNED NOT NULL DEFAULT 0,
  needs_check     BOOLEAN          NOT NULL DEFAULT FALSE COMMENT 'set by a FAILED dispense, cleared by the next visit',
  PRIMARY KEY (id),
  UNIQUE KEY uq_slots_machine_number (machine_id, slot_number),
  KEY idx_slots_product (product_id),
  KEY idx_slots_next_product (next_product_id),
  CONSTRAINT fk_slots_machine FOREIGN KEY (machine_id) REFERENCES machines (id),
  CONSTRAINT fk_slots_product FOREIGN KEY (product_id) REFERENCES products (id),
  CONSTRAINT fk_slots_next_product FOREIGN KEY (next_product_id) REFERENCES products (id),
  CONSTRAINT ck_slots_number CHECK (slot_number BETWEEN 1 AND 60),
  CONSTRAINT ck_slots_capacity CHECK (capacity BETWEEN 1 AND 30),
  CONSTRAINT ck_slots_qty CHECK (qty <= capacity)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- One row per purchase attempt; append-only (BR-11). The id is the kiosk's UUID (NFR-03).
CREATE TABLE transactions (
  id                   CHAR(36) CHARACTER SET ascii NOT NULL,
  machine_id           BIGINT UNSIGNED NOT NULL,
  slot_id              BIGINT UNSIGNED NOT NULL,
  product_id           BIGINT UNSIGNED NOT NULL,
  price                INT UNSIGNED    NOT NULL COMMENT 'snapshot at creation (BR-05)',
  status               ENUM('PENDING','PAID','DISPENSED','FAILED','CANCELLED','EXPIRED') NOT NULL,
  failure_reason       VARCHAR(100)    NULL,
  customer_reported_at DATETIME(3)     NULL,
  created_at           DATETIME(3)     NOT NULL,
  paid_at              DATETIME(3)     NULL,
  finished_at          DATETIME(3)     NULL,
  PRIMARY KEY (id),
  KEY idx_tx_status_created (status, created_at),
  KEY idx_tx_status_machine_created (status, machine_id, created_at),
  KEY idx_tx_machine_created (machine_id, created_at),
  KEY idx_tx_slot_status (slot_id, status),
  KEY idx_tx_created (created_at),
  KEY idx_tx_product (product_id),
  CONSTRAINT fk_tx_machine FOREIGN KEY (machine_id) REFERENCES machines (id),
  CONSTRAINT fk_tx_slot FOREIGN KEY (slot_id) REFERENCES slots (id),
  CONSTRAINT fk_tx_product FOREIGN KEY (product_id) REFERENCES products (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- One row per operator visit; append-only. client_id is the kiosk's UUID, so a resend is not saved twice.
CREATE TABLE restock_sessions (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  client_id   CHAR(36) CHARACTER SET ascii NOT NULL,
  machine_id  BIGINT UNSIGNED NOT NULL,
  user_id     BIGINT UNSIGNED NOT NULL,
  started_at  DATETIME(3)     NOT NULL,
  finished_at DATETIME(3)     NOT NULL,
  note        VARCHAR(255)    NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_restock_client (client_id),
  KEY idx_restock_machine_started (machine_id, started_at),
  KEY idx_restock_user_started (user_id, started_at),
  KEY idx_restock_started (started_at),
  CONSTRAINT fk_restock_machine FOREIGN KEY (machine_id) REFERENCES machines (id),
  CONSTRAINT fk_restock_user FOREIGN KEY (user_id) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- One row per slot in a visit; append-only.
CREATE TABLE restock_items (
  id              BIGINT UNSIGNED  NOT NULL AUTO_INCREMENT,
  session_id      BIGINT UNSIGNED  NOT NULL,
  slot_id         BIGINT UNSIGNED  NOT NULL,
  product_id      BIGINT UNSIGNED  NULL COMMENT 'the slot product after the visit',
  qty_before      TINYINT UNSIGNED NOT NULL COMMENT 'the system count before the visit',
  qty_after       TINYINT UNSIGNED NOT NULL COMMENT 'the count the operator set',
  product_changed BOOLEAN          NOT NULL DEFAULT FALSE,
  PRIMARY KEY (id),
  KEY idx_restock_items_session (session_id),
  KEY idx_restock_items_slot (slot_id),
  CONSTRAINT fk_restock_items_session FOREIGN KEY (session_id) REFERENCES restock_sessions (id),
  CONSTRAINT fk_restock_items_slot FOREIGN KEY (slot_id) REFERENCES slots (id),
  CONSTRAINT fk_restock_items_product FOREIGN KEY (product_id) REFERENCES products (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- The change log; append-only. entity_label keeps the name as it was when the change happened.
CREATE TABLE audit_logs (
  id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  user_id      BIGINT UNSIGNED NOT NULL,
  action       ENUM('CREATE','UPDATE','ARCHIVE','DEACTIVATE') NOT NULL,
  entity_type  ENUM('PRODUCT','MACHINE','SLOT','USER') NOT NULL,
  entity_id    BIGINT UNSIGNED NOT NULL,
  entity_label VARCHAR(150)    NOT NULL,
  before_json  JSON            NULL,
  after_json   JSON            NULL,
  created_at   DATETIME(3)     NOT NULL,
  PRIMARY KEY (id),
  KEY idx_audit_entity (entity_type, entity_id),
  KEY idx_audit_created (created_at),
  CONSTRAINT fk_audit_user FOREIGN KEY (user_id) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Login sessions for the admin web and for operator mode on a kiosk. Only a hash of the token is stored.
CREATE TABLE sessions (
  id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  token_hash   CHAR(64) CHARACTER SET ascii NOT NULL,
  user_id      BIGINT UNSIGNED NOT NULL,
  kind         ENUM('ADMIN','OPERATOR') NOT NULL,
  machine_id   BIGINT UNSIGNED NULL COMMENT 'operator sessions are bound to one machine',
  created_at   DATETIME(3)     NOT NULL,
  last_seen_at DATETIME(3)     NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_sessions_token (token_hash),
  KEY idx_sessions_user (user_id),
  KEY idx_sessions_last_seen (last_seen_at),
  CONSTRAINT fk_sessions_user FOREIGN KEY (user_id) REFERENCES users (id),
  CONSTRAINT fk_sessions_machine FOREIGN KEY (machine_id) REFERENCES machines (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
