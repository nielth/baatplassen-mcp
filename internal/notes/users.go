package notes

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
)

// User is someone the admin has given their own secret, and so their own notes.
type User struct {
	ID        int64
	Name      string
	CreatedAt string
}

// hashSecret is what is stored instead of the secret, so a copy of the database doesn't let
// anyone in. The secrets are long and random, so a plain hash is enough.
func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// CreateUser adds a user and returns their secret. It is not stored, so it can only be
// shown now.
func CreateUser(conn *sql.DB, name string) (User, string, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return User{}, "", err
	}
	secret := hex.EncodeToString(random)
	user := User{Name: name, CreatedAt: now()}
	result, err := conn.Exec(
		"INSERT INTO users (name, secret_hash, created_at) VALUES (?, ?, ?)",
		name, hashSecret(secret), user.CreatedAt,
	)
	if err != nil {
		return User{}, "", err
	}
	user.ID, err = result.LastInsertId()
	return user, secret, err
}

// UserBySecret returns the id of the user with this secret, or false if there is none or
// they have lost access.
func UserBySecret(conn *sql.DB, secret string) (int64, bool, error) {
	var id int64
	err := conn.QueryRow(
		"SELECT id FROM users WHERE secret_hash = ? AND revoked_at IS NULL", hashSecret(secret),
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

// Users returns the users who have access, oldest first.
func Users(conn *sql.DB) ([]User, error) {
	rows, err := conn.Query("SELECT id, name, created_at FROM users WHERE revoked_at IS NULL ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		var user User
		if err := rows.Scan(&user.ID, &user.Name, &user.CreatedAt); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

// RevokeUser takes away a user's access. Their notes are kept.
func RevokeUser(conn *sql.DB, id int64) error {
	_, err := conn.Exec("UPDATE users SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL", now(), id)
	return err
}
