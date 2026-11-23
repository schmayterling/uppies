package store

import (
	"context"
	"database/sql"
	"errors"
)

// Image kinds a project can have.
const (
	ImageLogo    = "logo"
	ImageFavicon = "favicon"
)

// MaxImageBytes is the largest image the schema accepts.
const MaxImageBytes = 512 << 10

// SetProjectImage stores or replaces one of a project's images.
func (s *Store) SetProjectImage(ctx context.Context, projectID int64, kind, contentType string, data []byte, nowMs int64) error {
	_, err := s.db.ExecContext(ctx, `
		insert into project_images (project_id, kind, content_type, data, updated_at_ms)
		values (?, ?, ?, ?, ?)
		on conflict (project_id, kind) do update set
			content_type = excluded.content_type, data = excluded.data, updated_at_ms = excluded.updated_at_ms`,
		projectID, kind, contentType, data, nowMs)
	return err
}

// DeleteProjectImage removes an image. Removing one that does not exist is not an error.
func (s *Store) DeleteProjectImage(ctx context.Context, projectID int64, kind string) error {
	_, err := s.db.ExecContext(ctx, "delete from project_images where project_id = ? and kind = ?", projectID, kind)
	return err
}

// ProjectImage returns an image's content type and bytes, or ErrNotFound.
func (s *Store) ProjectImage(ctx context.Context, projectID int64, kind string) (string, []byte, error) {
	var contentType string
	var data []byte
	err := s.db.QueryRowContext(ctx,
		"select content_type, data from project_images where project_id = ? and kind = ?", projectID, kind).
		Scan(&contentType, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	return contentType, data, err
}

// ProjectImageVersions returns, for each image kind the project has, the time
// it was last changed. Pages put it in the image URL so browsers refetch a new upload.
func (s *Store) ProjectImageVersions(ctx context.Context, projectID int64) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, "select kind, updated_at_ms from project_images where project_id = ?", projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	versions := make(map[string]int64)
	for rows.Next() {
		var kind string
		var updatedAtMs int64
		if err := rows.Scan(&kind, &updatedAtMs); err != nil {
			return nil, err
		}
		versions[kind] = updatedAtMs
	}
	return versions, rows.Err()
}
