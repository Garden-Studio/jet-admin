package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	pg "github.com/go-jet/jet/v2/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"

	"github.com/google/uuid"
)

// The admin users, for the host's command line: create the first one,
// reset a password, lock someone out.

var (
	// ErrUserNotFound is a username no admin user has.
	ErrUserNotFound = errors.New("no admin user has that username")
	// ErrUsernameTaken is a username another admin user has.
	ErrUsernameTaken = errors.New("another admin user has that username")
)

// MinPasswordLength is the shortest password CreateUser and SetPassword
// accept.
const MinPasswordLength = 8

// CheckPassword reports what is wrong with a new password, or nil.
func CheckPassword(password string) error {
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return fmt.Errorf("the password needs at least %d characters", MinPasswordLength)
	}
	// bcrypt reads only the first 72 bytes; a longer password would be
	// accepted with any ending.
	if len(password) > 72 {
		return errors.New("the password may have at most 72 bytes")
	}
	return nil
}

// HashPassword is password's bcrypt hash, as admin_users stores it.
func HashPassword(password string) (string, error) {
	if err := CheckPassword(password); err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

// CreateUser adds an active admin user.
func CreateUser(ctx context.Context, db DB, username, password string) (User, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return User{}, errors.New("the username is empty")
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}
	stmt := users.INSERT(users.Username, users.PasswordHash).
		VALUES(pg.String(username), pg.String(hash)).
		RETURNING(users.ID)
	query, args := stmt.Sql()
	u := User{Username: username}
	if err := db.QueryRow(ctx, query, args...).Scan(&u.ID); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return User{}, ErrUsernameTaken
		}
		return User{}, fmt.Errorf("create admin user %q: %w", username, err)
	}
	return u, nil
}

// SetPassword replaces the password of the user, and makes them active.
func SetPassword(ctx context.Context, db DB, username, password string) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	stmt := users.UPDATE(users.PasswordHash, users.IsActive).
		SET(pg.String(hash), pg.Bool(true)).
		WHERE(users.Username.EQ(pg.String(username)))
	return execOne(ctx, db, stmt, "set the password of "+username)
}

// Deactivate stops the user signing in and ends their sessions.
func Deactivate(ctx context.Context, db DB, username string) error {
	stmt := users.UPDATE(users.IsActive).
		SET(pg.Bool(false)).
		WHERE(users.Username.EQ(pg.String(username)))
	if err := execOne(ctx, db, stmt, "deactivate "+username); err != nil {
		return err
	}
	end := sessions.DELETE().WHERE(sessions.UserID.IN(
		pg.SELECT(users.ID).FROM(users).WHERE(users.Username.EQ(pg.String(username))),
	))
	query, args := end.Sql()
	if _, err := db.Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("end the sessions of %s: %w", username, err)
	}
	return nil
}

// LookupUser is the admin user of username, active or not.
func LookupUser(ctx context.Context, db DB, username string) (User, bool, error) {
	c, err := findCredentials(ctx, db, username)
	if errors.Is(err, ErrUserNotFound) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, err
	}
	return User{ID: c.id, Username: c.username}, c.active, nil
}

func execOne(ctx context.Context, db DB, stmt pg.Statement, what string) error {
	query, args := stmt.Sql()
	tag, err := db.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	return nil
}

type credentials struct {
	id       uuid.UUID
	username string
	hash     string
	active   bool
}

func findCredentials(ctx context.Context, db DB, username string) (credentials, error) {
	stmt := pg.SELECT(users.ID, users.Username, users.PasswordHash, users.IsActive).
		FROM(users).
		WHERE(users.Username.EQ(pg.String(username)))
	query, args := stmt.Sql()
	var c credentials
	err := db.QueryRow(ctx, query, args...).Scan(&c.id, &c.username, &c.hash, &c.active)
	if errors.Is(err, pgx.ErrNoRows) {
		return credentials{}, ErrUserNotFound
	}
	return c, err
}

// dummyHash is compared against when the username is unknown, so a wrong
// username takes as long to refuse as a wrong password.
var dummyHash = sync.OnceValue(func() []byte {
	hash, _ := bcrypt.GenerateFromPassword([]byte("jet-admin dummy password"), bcrypt.DefaultCost)
	return hash
})

// verify is the active user username whose password is password.
func verify(ctx context.Context, db DB, username, password string) (User, bool, error) {
	c, err := findCredentials(ctx, db, username)
	if errors.Is(err, ErrUserNotFound) {
		_ = bcrypt.CompareHashAndPassword(dummyHash(), []byte(password))
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, err
	}
	if bcrypt.CompareHashAndPassword([]byte(c.hash), []byte(password)) != nil || !c.active {
		return User{}, false, nil
	}
	return User{ID: c.id, Username: c.username}, true, nil
}

func touchLastLogin(ctx context.Context, db DB, id uuid.UUID) error {
	stmt := users.UPDATE(users.LastLoginAt).SET(pg.NOW()).WHERE(users.ID.EQ(pg.UUID(id)))
	query, args := stmt.Sql()
	_, err := db.Exec(ctx, query, args...)
	return err
}
