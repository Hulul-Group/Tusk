package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// LastKnexMigration is the newest Kutt v3.2.6 migration (server/migrations).
const LastKnexMigration = "20250106070444_remove_cooldown.js"

// KnexMigrations lists all Kutt migrations in order; written into knex_migrations on a
// fresh DB so `knex migrate:latest` (Node Kutt) sees an up-to-date schema.
var KnexMigrations = []string{
	"20200211220920_constraints.js",
	"20200510140704_domains.js",
	"20200718124944_description.js",
	"20200730203154_expire_in.js",
	"20200810195255_change_email.js",
	"20241103083933_user-roles.js",
	"20241223062111_indexes.js",
	"20241223103044_visits_user_id.js",
	"20241223155527_visits_user_id_index.js",
	LastKnexMigration,
}

// ErrSchemaTooOld is returned when an existing Kutt DB lacks the latest knex migration.
var ErrSchemaTooOld = errors.New("sqlstore: Kutt database is not fully migrated: run Kutt v3.2.6 (`npm run migrate`) first; knex_migrations lacks " + LastKnexMigration)

// Migrate verifies or creates the schema (store.Store.Migrate).
//
//   - knex_migrations exists: require LastKnexMigration, never alter anything (C1).
//   - Empty DB: create the final Kutt schema exactly as knex 3.1 emits it after all 10
//     migrations (DDL captured from `knex migrate:latest` on each dialect), plus
//     knex_migrations / knex_migrations_lock rows.
//   - Kutt tables without knex_migrations: refuse (unknown state).
func (s *DB) Migrate(ctx context.Context) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	unlock, err := s.migrationLock(ctx, conn)
	if err != nil {
		return err
	}
	defer unlock()

	has, err := s.hasTable(ctx, conn, "knex_migrations")
	if err != nil {
		return err
	}
	if has {
		var n int
		if err := conn.QueryRowContext(ctx, s.q("SELECT COUNT(*) FROM knex_migrations WHERE name = ?"), LastKnexMigration).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return ErrSchemaTooOld
		}
		return nil
	}
	for _, t := range []string{"users", "links", "visits", "domains"} {
		ok, err := s.hasTable(ctx, conn, t)
		if err != nil {
			return err
		}
		if ok {
			return fmt.Errorf("sqlstore: table %q exists but knex_migrations does not; refusing to guess the schema version", t)
		}
	}
	return s.createSchema(ctx, conn)
}

func (s *DB) migrationLock(ctx context.Context, conn *sql.Conn) (func(), error) {
	switch s.d {
	case dPostgres:
		if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock(734200117)"); err != nil {
			return nil, err
		}
		return func() { conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock(734200117)") }, nil
	case dMySQL:
		var got sql.NullInt64
		if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK('tusk_migrate', 120)").Scan(&got); err != nil {
			return nil, err
		}
		if got.Int64 != 1 {
			return nil, errors.New("sqlstore: timed out waiting for migration lock")
		}
		return func() { conn.ExecContext(context.Background(), "SELECT RELEASE_LOCK('tusk_migrate')") }, nil
	}
	return func() {}, nil // sqlite: DDL runs in one IMMEDIATE transaction
}

func (s *DB) hasTable(ctx context.Context, x querier, name string) (bool, error) {
	var q string
	switch s.d {
	case dPostgres:
		q = "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = $1"
	case dMySQL:
		q = "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?"
	default:
		q = "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?"
	}
	var n int
	err := x.QueryRowContext(ctx, q, name).Scan(&n)
	return n > 0, err
}

func (s *DB) createSchema(ctx context.Context, conn *sql.Conn) error {
	var ddl []string
	switch s.d {
	case dPostgres:
		ddl = pgSchema
	case dMySQL:
		ddl = mysqlSchema
	default:
		ddl = sqliteSchema
	}
	// pg and sqlite have transactional DDL; mysql commits each statement implicitly.
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range ddl {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("sqlstore: create schema: %w\n%s", err, stmt)
		}
	}
	now := time.Now().UTC()
	var mt any = now
	if s.d == dSQLite {
		mt = now.UnixMilli() // what knex + better-sqlite3 store for migration_time
	}
	for _, name := range KnexMigrations {
		if _, err := tx.ExecContext(ctx, s.q("INSERT INTO knex_migrations (name, batch, migration_time) VALUES (?, 1, ?)"), name, mt); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, s.q("INSERT INTO knex_migrations_lock (is_locked) VALUES (0)")); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- DDL: captured from knex 3.1.0 `migrate:latest` of Kutt v3.2.6 on each dialect ----

var sqliteSchema = []string{
	"CREATE TABLE `knex_migrations` (`id` integer not null primary key autoincrement, `name` varchar(255), `batch` integer, `migration_time` datetime)",
	"CREATE TABLE `knex_migrations_lock` (`index` integer not null primary key autoincrement, `is_locked` integer)",
	"CREATE TABLE `users` (`id` integer not null primary key autoincrement, `apikey` varchar(255), `banned` boolean not null default '0', `banned_by_id` integer, `email` varchar(255) not null, `role` text check (`role` in ('USER', 'ADMIN')) not null default 'USER', `password` varchar(255) not null, `reset_password_expires` datetime, `reset_password_token` varchar(255), `change_email_expires` datetime, `change_email_token` varchar(255), `change_email_address` varchar(255), `verification_expires` datetime, `verification_token` varchar(255), `verified` boolean not null default '0', `created_at` datetime not null default CURRENT_TIMESTAMP, `updated_at` datetime not null default CURRENT_TIMESTAMP, foreign key(`banned_by_id`) references `users`(`id`))",
	"CREATE UNIQUE INDEX `users_email_unique` on `users` (`email`)",
	"CREATE TABLE `ips` (`id` integer not null primary key autoincrement, `ip` varchar(255) not null, `created_at` datetime not null default CURRENT_TIMESTAMP, `updated_at` datetime not null default CURRENT_TIMESTAMP)",
	"CREATE UNIQUE INDEX `ips_ip_unique` on `ips` (`ip`)",
	"CREATE TABLE `domains` (`id` integer not null primary key autoincrement, `banned` boolean not null default '0', `banned_by_id` integer, `address` varchar(255) not null, `homepage` varchar(255) null, `user_id` integer, `uuid` char(36) not null default (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab',abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))), `created_at` datetime not null default CURRENT_TIMESTAMP, `updated_at` datetime not null default CURRENT_TIMESTAMP, foreign key(`banned_by_id`) references `users`(`id`), constraint `domains_user_id_foreign` foreign key(`user_id`) references `users`(`id`) on delete SET NULL)",
	"CREATE UNIQUE INDEX `domains_address_unique` on `domains` (`address`)",
	"CREATE TABLE `hosts` (`id` integer not null primary key autoincrement, `address` varchar(255) not null, `banned` boolean not null default '0', `banned_by_id` integer, `created_at` datetime not null default CURRENT_TIMESTAMP, `updated_at` datetime not null default CURRENT_TIMESTAMP, foreign key(`banned_by_id`) references `users`(`id`))",
	"CREATE UNIQUE INDEX `hosts_address_unique` on `hosts` (`address`)",
	"CREATE TABLE `links` (`id` integer not null primary key autoincrement, `address` varchar(255) not null, `description` varchar(255), `banned` boolean not null default '0', `banned_by_id` integer, `domain_id` integer, `password` varchar(255), `expire_in` datetime, `target` varchar(2040) not null, `user_id` integer, `visit_count` integer not null default '0', `uuid` char(36) not null default (lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4' || substr(lower(hex(randomblob(2))),2) || '-' || substr('89ab',abs(random()) % 4 + 1, 1) || substr(lower(hex(randomblob(2))),2) || '-' || lower(hex(randomblob(6)))), `created_at` datetime not null default CURRENT_TIMESTAMP, `updated_at` datetime not null default CURRENT_TIMESTAMP, foreign key(`banned_by_id`) references `users`(`id`), foreign key(`domain_id`) references `domains`(`id`), constraint `links_user_id_foreign` foreign key(`user_id`) references `users`(`id`) on delete CASCADE)",
	"CREATE TABLE `visits` (`id` integer not null primary key autoincrement, `countries` json, `created_at` datetime not null default CURRENT_TIMESTAMP, `updated_at` datetime default CURRENT_TIMESTAMP, `link_id` integer, `user_id` integer, `referrers` json, `total` integer not null default '0', `br_chrome` integer not null default '0', `br_edge` integer not null default '0', `br_firefox` integer not null default '0', `br_ie` integer not null default '0', `br_opera` integer not null default '0', `br_other` integer not null default '0', `br_safari` integer not null default '0', `os_android` integer not null default '0', `os_ios` integer not null default '0', `os_linux` integer not null default '0', `os_macos` integer not null default '0', `os_other` integer not null default '0', `os_windows` integer not null default '0', constraint `visits_link_id_foreign` foreign key(`link_id`) references `links`(`id`) on delete CASCADE, constraint `visits_user_id_foreign` foreign key(`user_id`) references `users`(`id`) on delete CASCADE)",
	"CREATE UNIQUE INDEX `users_apikey_unique` on `users` (`apikey`)",
	"CREATE INDEX links_domain_id_index ON links (domain_id)",
	"CREATE INDEX links_user_id_index ON links (user_id)",
	"CREATE INDEX links_address_index ON links (address)",
	"CREATE INDEX links_expire_in_index ON links (expire_in)",
	"CREATE INDEX domains_address_index ON domains (address)",
	"CREATE INDEX domains_user_id_index ON domains (user_id)",
	"CREATE INDEX hosts_address_index ON hosts (address)",
	"CREATE INDEX visits_link_id_index ON visits (link_id)",
	"CREATE INDEX visits_user_id_index ON visits (user_id)",
}

var pgSchema = []string{
	`CREATE TABLE knex_migrations (id serial PRIMARY KEY, name varchar(255), batch integer, migration_time timestamptz)`,
	`CREATE TABLE knex_migrations_lock (index serial PRIMARY KEY, is_locked integer)`,
	`CREATE TABLE users (
  id serial PRIMARY KEY,
  apikey varchar(255),
  banned boolean NOT NULL DEFAULT false,
  banned_by_id integer,
  email varchar(255) NOT NULL,
  role text NOT NULL DEFAULT 'USER' CONSTRAINT users_role_check CHECK (role IN ('USER', 'ADMIN')),
  password varchar(255) NOT NULL,
  reset_password_expires timestamptz,
  reset_password_token varchar(255),
  change_email_expires timestamptz,
  change_email_token varchar(255),
  change_email_address varchar(255),
  verification_expires timestamptz,
  verification_token varchar(255),
  verified boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT users_email_unique UNIQUE (email),
  CONSTRAINT users_banned_by_id_foreign FOREIGN KEY (banned_by_id) REFERENCES users(id))`,
	`CREATE TABLE ips (
  id serial PRIMARY KEY,
  ip varchar(255) NOT NULL,
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT ips_ip_unique UNIQUE (ip))`,
	`CREATE TABLE domains (
  id serial PRIMARY KEY,
  banned boolean NOT NULL DEFAULT false,
  banned_by_id integer,
  address varchar(255) NOT NULL,
  homepage varchar(255),
  user_id integer,
  uuid uuid NOT NULL DEFAULT gen_random_uuid(),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT domains_address_unique UNIQUE (address),
  CONSTRAINT domains_banned_by_id_foreign FOREIGN KEY (banned_by_id) REFERENCES users(id),
  CONSTRAINT domains_user_id_foreign FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL)`,
	`CREATE TABLE hosts (
  id serial PRIMARY KEY,
  address varchar(255) NOT NULL,
  banned boolean NOT NULL DEFAULT false,
  banned_by_id integer,
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT hosts_address_unique UNIQUE (address),
  CONSTRAINT hosts_banned_by_id_foreign FOREIGN KEY (banned_by_id) REFERENCES users(id))`,
	`CREATE TABLE links (
  id serial PRIMARY KEY,
  address varchar(255) NOT NULL,
  description varchar(255),
  banned boolean NOT NULL DEFAULT false,
  banned_by_id integer,
  domain_id integer,
  password varchar(255),
  expire_in timestamptz,
  target varchar(2040) NOT NULL,
  user_id integer,
  visit_count integer NOT NULL DEFAULT 0,
  uuid uuid NOT NULL DEFAULT gen_random_uuid(),
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT links_banned_by_id_foreign FOREIGN KEY (banned_by_id) REFERENCES users(id),
  CONSTRAINT links_domain_id_foreign FOREIGN KEY (domain_id) REFERENCES domains(id),
  CONSTRAINT links_user_id_foreign FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE)`,
	`CREATE TABLE visits (
  id serial PRIMARY KEY,
  countries jsonb,
  created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at timestamptz DEFAULT CURRENT_TIMESTAMP,
  link_id integer,
  user_id integer,
  referrers jsonb,
  total integer NOT NULL DEFAULT 0,
  br_chrome integer NOT NULL DEFAULT 0,
  br_edge integer NOT NULL DEFAULT 0,
  br_firefox integer NOT NULL DEFAULT 0,
  br_ie integer NOT NULL DEFAULT 0,
  br_opera integer NOT NULL DEFAULT 0,
  br_other integer NOT NULL DEFAULT 0,
  br_safari integer NOT NULL DEFAULT 0,
  os_android integer NOT NULL DEFAULT 0,
  os_ios integer NOT NULL DEFAULT 0,
  os_linux integer NOT NULL DEFAULT 0,
  os_macos integer NOT NULL DEFAULT 0,
  os_other integer NOT NULL DEFAULT 0,
  os_windows integer NOT NULL DEFAULT 0,
  CONSTRAINT visits_link_id_foreign FOREIGN KEY (link_id) REFERENCES links(id) ON DELETE CASCADE,
  CONSTRAINT visits_user_id_foreign FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE)`,
	`ALTER TABLE users ADD CONSTRAINT users_apikey_unique UNIQUE (apikey)`,
	`CREATE INDEX links_domain_id_index ON links (domain_id)`,
	`CREATE INDEX links_user_id_index ON links (user_id)`,
	`CREATE INDEX links_address_index ON links (address)`,
	`CREATE INDEX links_expire_in_index ON links (expire_in)`,
	`CREATE INDEX domains_address_index ON domains (address)`,
	`CREATE INDEX domains_user_id_index ON domains (user_id)`,
	`CREATE INDEX hosts_address_index ON hosts (address)`,
	`CREATE INDEX visits_link_id_index ON visits (link_id)`,
	`CREATE INDEX visits_user_id_index ON visits (user_id)`,
}

var mysqlSchema = []string{
	"CREATE TABLE `knex_migrations` (`id` int unsigned NOT NULL AUTO_INCREMENT PRIMARY KEY, `name` varchar(255), `batch` int, `migration_time` timestamp NULL DEFAULT NULL)",
	"CREATE TABLE `knex_migrations_lock` (`index` int unsigned NOT NULL AUTO_INCREMENT PRIMARY KEY, `is_locked` int)",
	"CREATE TABLE `users` (`id` int unsigned NOT NULL AUTO_INCREMENT PRIMARY KEY, `apikey` varchar(255), `banned` boolean NOT NULL DEFAULT 0, `banned_by_id` int unsigned, `email` varchar(255) NOT NULL, `role` enum('USER','ADMIN') NOT NULL DEFAULT 'USER', `password` varchar(255) NOT NULL, `reset_password_expires` datetime, `reset_password_token` varchar(255), `change_email_expires` datetime, `change_email_token` varchar(255), `change_email_address` varchar(255), `verification_expires` datetime, `verification_token` varchar(255), `verified` boolean NOT NULL DEFAULT 0, `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE KEY `users_email_unique` (`email`), CONSTRAINT `users_banned_by_id_foreign` FOREIGN KEY (`banned_by_id`) REFERENCES `users` (`id`)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
	"CREATE TABLE `ips` (`id` int unsigned NOT NULL AUTO_INCREMENT PRIMARY KEY, `ip` varchar(255) NOT NULL, `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE KEY `ips_ip_unique` (`ip`)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
	"CREATE TABLE `domains` (`id` int unsigned NOT NULL AUTO_INCREMENT PRIMARY KEY, `banned` boolean NOT NULL DEFAULT 0, `banned_by_id` int unsigned, `address` varchar(255) NOT NULL, `homepage` varchar(255) NULL, `user_id` int unsigned, `uuid` char(36) NOT NULL DEFAULT (uuid()), `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE KEY `domains_address_unique` (`address`), CONSTRAINT `domains_banned_by_id_foreign` FOREIGN KEY (`banned_by_id`) REFERENCES `users` (`id`), CONSTRAINT `domains_user_id_foreign` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON DELETE SET NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
	"CREATE TABLE `hosts` (`id` int unsigned NOT NULL AUTO_INCREMENT PRIMARY KEY, `address` varchar(255) NOT NULL, `banned` boolean NOT NULL DEFAULT 0, `banned_by_id` int unsigned, `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE KEY `hosts_address_unique` (`address`), CONSTRAINT `hosts_banned_by_id_foreign` FOREIGN KEY (`banned_by_id`) REFERENCES `users` (`id`)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
	"CREATE TABLE `links` (`id` int unsigned NOT NULL AUTO_INCREMENT PRIMARY KEY, `address` varchar(255) NOT NULL, `description` varchar(255), `banned` boolean NOT NULL DEFAULT 0, `banned_by_id` int unsigned, `domain_id` int unsigned, `password` varchar(255), `expire_in` datetime, `target` varchar(2040) NOT NULL, `user_id` int unsigned, `visit_count` int NOT NULL DEFAULT 0, `uuid` char(36) NOT NULL DEFAULT (uuid()), `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, CONSTRAINT `links_banned_by_id_foreign` FOREIGN KEY (`banned_by_id`) REFERENCES `users` (`id`), CONSTRAINT `links_domain_id_foreign` FOREIGN KEY (`domain_id`) REFERENCES `domains` (`id`), CONSTRAINT `links_user_id_foreign` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
	"CREATE TABLE `visits` (`id` int unsigned NOT NULL AUTO_INCREMENT PRIMARY KEY, `countries` json, `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP, `updated_at` datetime DEFAULT CURRENT_TIMESTAMP, `link_id` int unsigned, `user_id` int unsigned, `referrers` json, `total` int NOT NULL DEFAULT 0, `br_chrome` int NOT NULL DEFAULT 0, `br_edge` int NOT NULL DEFAULT 0, `br_firefox` int NOT NULL DEFAULT 0, `br_ie` int NOT NULL DEFAULT 0, `br_opera` int NOT NULL DEFAULT 0, `br_other` int NOT NULL DEFAULT 0, `br_safari` int NOT NULL DEFAULT 0, `os_android` int NOT NULL DEFAULT 0, `os_ios` int NOT NULL DEFAULT 0, `os_linux` int NOT NULL DEFAULT 0, `os_macos` int NOT NULL DEFAULT 0, `os_other` int NOT NULL DEFAULT 0, `os_windows` int NOT NULL DEFAULT 0, CONSTRAINT `visits_link_id_foreign` FOREIGN KEY (`link_id`) REFERENCES `links` (`id`) ON DELETE CASCADE, CONSTRAINT `visits_user_id_foreign` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON DELETE CASCADE) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
	"ALTER TABLE `users` ADD UNIQUE `users_apikey_unique` (`apikey`)",
	"CREATE INDEX links_domain_id_index ON links (domain_id)",
	"CREATE INDEX links_user_id_index ON links (user_id)",
	"CREATE INDEX links_address_index ON links (address)",
	"CREATE INDEX links_expire_in_index ON links (expire_in)",
	"CREATE INDEX domains_address_index ON domains (address)",
	"CREATE INDEX domains_user_id_index ON domains (user_id)",
	"CREATE INDEX hosts_address_index ON hosts (address)",
	"CREATE INDEX visits_link_id_index ON visits (link_id)",
	"CREATE INDEX visits_user_id_index ON visits (user_id)",
}
