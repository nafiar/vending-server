package core

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"vending/internal/security"
)

// OperatorInput is the body of POST /admin/operators.
type OperatorInput struct {
	Name     string `json:"name"`
	Username string `json:"username"`
	Password string `json:"password"`
}

var usernamePattern = regexp.MustCompile(`^[a-z0-9._]{3,32}$`)

func userLabel(name, username string) string { return name + " (" + username + ")" }

// ListOperators returns every staff account with its visit figures (UC-12).
// Admins are listed too: they can restock, so they appear in the restock filter.
func (s *Service) ListOperators(ctx context.Context) ([]OperatorView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, username, role, is_active, created_at FROM users ORDER BY role, name, id`)
	if err != nil {
		return nil, err
	}
	var out []OperatorView
	index := map[int64]int{}
	for rows.Next() {
		var v OperatorView
		var created sql.NullTime
		if err := rows.Scan(&v.ID, &v.Name, &v.Username, &v.Role, &v.IsActive, &created); err != nil {
			rows.Close()
			return nil, err
		}
		v.CreatedAt = ms(created.Time)
		index[v.ID] = len(out)
		out = append(out, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	since := startOfDay(s.now().In(s.loc)).AddDate(0, 0, -29).UTC()
	rows, err = s.db.QueryContext(ctx, `SELECT user_id, COUNT(*) FROM restock_sessions WHERE started_at >= ? GROUP BY user_id`, since)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, n int64
		if err := rows.Scan(&id, &n); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := index[id]; ok {
			out[i].Visits30 = n
		}
	}
	rows.Close()

	rows, err = s.db.QueryContext(ctx, `
		SELECT rs.user_id, rs.started_at, m.code
		FROM restock_sessions rs
		JOIN (SELECT user_id, MAX(id) AS last_id FROM restock_sessions GROUP BY user_id) x ON x.last_id = rs.id
		JOIN machines m ON m.id = rs.machine_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var at sql.NullTime
		var code string
		if err := rows.Scan(&id, &at, &code); err != nil {
			return nil, err
		}
		if i, ok := index[id]; ok {
			out[i].LastVisitAt = nullMs(at)
			out[i].LastVisitMachine = &code
		}
	}
	return out, rows.Err()
}

// CreateOperator adds an operator account.
func (s *Service) CreateOperator(ctx context.Context, actor Principal, in OperatorInput) (map[string]int64, error) {
	fe := fieldErrors{}
	name := strings.TrimSpace(in.Name)
	username := strings.ToLower(strings.TrimSpace(in.Username))
	switch n := utf8.RuneCountInString(name); {
	case n == 0:
		fe.add("name", "Nama wajib diisi.")
	case n > 100:
		fe.add("name", "Nama paling panjang 100 karakter.")
	}
	if !usernamePattern.MatchString(username) {
		fe.add("username", "3–32 karakter: huruf kecil, angka, titik atau garis bawah.")
	}
	switch n := utf8.RuneCountInString(in.Password); {
	case n < 6:
		fe.add("password", "Paling sedikit 6 karakter.")
	case n > 128:
		fe.add("password", "Paling panjang 128 karakter.")
	}
	if err := fe.err(); err != nil {
		return nil, err
	}
	hash, err := security.HashPassword(in.Password)
	if err != nil {
		return nil, err
	}
	var id int64
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE username = ?`, username).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			return fieldErrors{"username": "Username sudah dipakai."}.err()
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO users (name, username, password_hash, role, is_active, created_at) VALUES (?, ?, ?, 'OPERATOR', TRUE, ?)`,
			name, username, hash, s.now())
		if err != nil {
			if isMySQLError(err, mysqlDuplicateKey) {
				return fieldErrors{"username": "Username sudah dipakai."}.err()
			}
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		return s.writeAudit(ctx, tx, auditEntry{
			actorID: actor.UserID, action: actionCreate, entityType: entityUser, entityID: id, label: userLabel(name, username),
			after: map[string]any{"name": name, "username": username, "role": roleOperator},
		})
	})
	if err != nil {
		return nil, err
	}
	return map[string]int64{"id": id}, nil
}

// lockOperator loads a user for a change and refuses admin accounts, which are
// managed from the command line instead (create-admin).
func lockOperator(ctx context.Context, tx *sql.Tx, id int64) (name, username string, active bool, err error) {
	var role string
	err = tx.QueryRowContext(ctx, `SELECT name, username, role, is_active FROM users WHERE id = ? FOR UPDATE`, id).Scan(&name, &username, &role, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, errNotFound("Akun tidak ditemukan.")
	}
	if err == nil && role == roleAdmin {
		err = errConflict("CONFLICT", "Akun admin tidak bisa diubah dari sini.", nil)
	}
	return name, username, active, err
}

// SetOperatorActive deactivates or reactivates an operator. Deactivating ends
// their sessions at once.
func (s *Service) SetOperatorActive(ctx context.Context, actor Principal, id int64, active bool) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		name, username, cur, err := lockOperator(ctx, tx, id)
		if err != nil || cur == active {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET is_active = ? WHERE id = ?`, active, id); err != nil {
			return err
		}
		if !active {
			if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
				return err
			}
		}
		action := actionUpdate
		if !active {
			action = actionDeactivate
		}
		return s.writeAudit(ctx, tx, auditEntry{
			actorID: actor.UserID, action: action, entityType: entityUser, entityID: id, label: userLabel(name, username),
			before: map[string]any{"isActive": !active}, after: map[string]any{"isActive": active},
		})
	})
}

// ResetOperatorPassword sets a random temporary password, returned once, and
// clears the lockout and every session of that account.
func (s *Service) ResetOperatorPassword(ctx context.Context, actor Principal, id int64) (string, error) {
	pw := security.TemporaryPassword(10)
	hash, err := security.HashPassword(pw)
	if err != nil {
		return "", err
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		name, username, _, err := lockOperator(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, failed_logins = 0, locked_until = NULL WHERE id = ?`, hash, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
			return err
		}
		return s.writeAudit(ctx, tx, auditEntry{
			actorID: actor.UserID, action: actionUpdate, entityType: entityUser, entityID: id, label: userLabel(name, username),
			before: map[string]any{"password": "••••"}, after: map[string]any{"password": "(diatur ulang)"},
		})
	})
	return pw, err
}

// CreateAdmin adds an admin account; it is used by the create-admin command,
// never by the API, so there is no actor and no change-log row.
func (s *Service) CreateAdmin(ctx context.Context, name, username, password string) (int64, error) {
	fe := fieldErrors{}
	name = strings.TrimSpace(name)
	username = strings.ToLower(strings.TrimSpace(username))
	if name == "" || utf8.RuneCountInString(name) > 100 {
		fe.add("name", "name is required (at most 100 characters)")
	}
	if !usernamePattern.MatchString(username) {
		fe.add("username", "3-32 characters: lowercase letters, digits, dot or underscore")
	}
	if n := utf8.RuneCountInString(password); n < 8 || n > 128 {
		fe.add("password", "8 to 128 characters")
	}
	if err := fe.err(); err != nil {
		return 0, err
	}
	hash, err := security.HashPassword(password)
	if err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO users (name, username, password_hash, role, is_active, created_at) VALUES (?, ?, ?, 'ADMIN', TRUE, ?)`,
		name, username, hash, s.now())
	if isMySQLError(err, mysqlDuplicateKey) {
		return 0, fieldErrors{"username": "username " + username + " already exists"}.err()
	}
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetPassword changes any account's password; used by the create-admin command
// to reset an admin who forgot theirs.
func (s *Service) SetPassword(ctx context.Context, username, password string) error {
	if n := utf8.RuneCountInString(password); n < 8 || n > 128 {
		return fieldErrors{"password": "8 to 128 characters"}.err()
	}
	hash, err := security.HashPassword(password)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash = ?, failed_logins = 0, locked_until = NULL, is_active = TRUE WHERE username = ?`,
		hash, strings.ToLower(strings.TrimSpace(username)))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound("no account named " + username)
	}
	return nil
}
