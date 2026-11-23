package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"modernc.org/sqlite"
)

// InstanceSettings are the settings of the whole install, as opposed to one project.
type InstanceSettings struct {
	// Name is shown in the admin sidebar and on the public list of status pages.
	Name string
	// Theme and AccentHue style the admin pages: "auto", "light" or "dark", and a hue from 0 to 360.
	Theme     string
	AccentHue int
	// PublicList decides whether the front page lists the status pages to anyone.
	PublicList bool
	// LoginLayout places the sign-in form: "split" puts it in a column on the
	// left with the background beside it, "centered" puts it in a card over the background.
	LoginLayout string
	// LoginBackground is what the sign-in page shows behind or beside the
	// form: "plain", "gradient" or "image", the uploaded one.
	LoginBackground string
}

// Sign-in page choices.
var (
	LoginLayouts     = []string{"split", "centered"}
	LoginBackgrounds = []string{"plain", "gradient", "image"}
)

// ImageLoginBackground is the instance image shown on the sign-in page.
const ImageLoginBackground = "login_background"

// MaxBackgroundBytes is the largest instance image the schema accepts.
const MaxBackgroundBytes = 2 << 20

// defaultInstance is what a fresh install uses until something is saved.
var defaultInstance = InstanceSettings{
	Name: "uppies", Theme: "auto", AccentHue: 200, PublicList: true,
	LoginLayout: "split", LoginBackground: "gradient",
}

// Instance returns the instance settings, with defaults for what was never saved.
func (s *Store) Instance(ctx context.Context) (InstanceSettings, error) {
	settings := defaultInstance
	rows, err := s.db.QueryContext(ctx, "select key, value from instance_settings")
	if err != nil {
		return settings, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return settings, err
		}
		switch key {
		case "name":
			settings.Name = value
		case "theme":
			settings.Theme = value
		case "accent_hue":
			// A value that is not a number keeps the default.
			if hue, err := strconv.Atoi(value); err == nil {
				settings.AccentHue = hue
			}
		case "public_list":
			settings.PublicList = value == "1"
		case "login_layout":
			settings.LoginLayout = value
		case "login_background":
			settings.LoginBackground = value
		}
	}
	return settings, rows.Err()
}

// SaveInstance stores all instance settings.
func (s *Store) SaveInstance(ctx context.Context, settings InstanceSettings) error {
	publicList := "0"
	if settings.PublicList {
		publicList = "1"
	}
	values := map[string]string{
		"name": settings.Name, "theme": settings.Theme,
		"accent_hue": strconv.Itoa(settings.AccentHue), "public_list": publicList,
		"login_layout": settings.LoginLayout, "login_background": settings.LoginBackground,
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		for key, value := range values {
			if _, err := tx.ExecContext(ctx, `
				insert into instance_settings (key, value) values (?, ?)
				on conflict (key) do update set value = excluded.value`, key, value); err != nil {
				return err
			}
		}
		return nil
	})
}

// User is an admin account.
type User struct {
	ID          int64
	Username    string
	CreatedAtMs int64
}

// ErrUsernameTaken is returned when a username is already in use.
var ErrUsernameTaken = errors.New("username already in use")

// ErrLastUser is returned when deleting the only account, which would lock everyone out.
var ErrLastUser = errors.New("the last account cannot be deleted")

// Users lists the admin accounts, oldest first.
func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, "select id, username, created_at_ms from users order by id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.CreatedAtMs); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// CreateUser adds an admin account, or returns ErrUsernameTaken.
func (s *Store) CreateUser(ctx context.Context, username, passwordHash string, nowMs int64) error {
	_, err := s.db.ExecContext(ctx,
		"insert into users (username, password_hash, created_at_ms) values (?, ?, ?)", username, passwordHash, nowMs)
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code() == sqliteConstraintUnique {
		return ErrUsernameTaken
	}
	return err
}

// DeleteUser removes an account and its sessions. It returns ErrLastUser
// when it is the only account, and ErrNotFound when it does not exist.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, "select count(*) from users").Scan(&count); err != nil {
			return err
		}
		if count <= 1 {
			return ErrLastUser
		}
		res, err := tx.ExecContext(ctx, "delete from users where id = ?", id)
		if err != nil {
			return err
		}
		return requireOneRow(res)
	})
}

// SetPassword replaces an account's password hash and ends its other sessions,
// keeping the one that made the change.
func (s *Store) SetPassword(ctx context.Context, userID int64, passwordHash, keepTokenHash string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "update users set password_hash = ? where id = ?", passwordHash, userID)
		if err != nil {
			return err
		}
		if err := requireOneRow(res); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "delete from sessions where user_id = ? and token_hash <> ?", userID, keepTokenHash)
		return err
	})
}

// PasswordHashByID returns the password hash of an account, or ErrNotFound.
func (s *Store) PasswordHashByID(ctx context.Context, userID int64) (string, error) {
	var hash string
	err := s.db.QueryRowContext(ctx, "select password_hash from users where id = ?", userID).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return hash, err
}

// LogoVersions returns, for each project that has a logo, when it was last changed.
func (s *Store) LogoVersions(ctx context.Context) (map[int64]int64, error) {
	rows, err := s.db.QueryContext(ctx, "select project_id, updated_at_ms from project_images where kind = ?", ImageLogo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	versions := make(map[int64]int64)
	for rows.Next() {
		var projectID, updatedAtMs int64
		if err := rows.Scan(&projectID, &updatedAtMs); err != nil {
			return nil, err
		}
		versions[projectID] = updatedAtMs
	}
	return versions, rows.Err()
}

// SetInstanceImage stores or replaces one of the install's images.
func (s *Store) SetInstanceImage(ctx context.Context, kind, contentType string, data []byte, nowMs int64) error {
	_, err := s.db.ExecContext(ctx, `
		insert into instance_images (kind, content_type, data, updated_at_ms) values (?, ?, ?, ?)
		on conflict (kind) do update set
			content_type = excluded.content_type, data = excluded.data, updated_at_ms = excluded.updated_at_ms`,
		kind, contentType, data, nowMs)
	return err
}

// DeleteInstanceImage removes an image. Removing one that does not exist is not an error.
func (s *Store) DeleteInstanceImage(ctx context.Context, kind string) error {
	_, err := s.db.ExecContext(ctx, "delete from instance_images where kind = ?", kind)
	return err
}

// InstanceImage returns an image's content type and bytes, or ErrNotFound.
func (s *Store) InstanceImage(ctx context.Context, kind string) (string, []byte, error) {
	var contentType string
	var data []byte
	err := s.db.QueryRowContext(ctx, "select content_type, data from instance_images where kind = ?", kind).
		Scan(&contentType, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	return contentType, data, err
}

// InstanceImageVersion returns when an image was last changed, or 0 when there is none.
func (s *Store) InstanceImageVersion(ctx context.Context, kind string) (int64, error) {
	var updatedAtMs int64
	err := s.db.QueryRowContext(ctx, "select updated_at_ms from instance_images where kind = ?", kind).Scan(&updatedAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return updatedAtMs, err
}
