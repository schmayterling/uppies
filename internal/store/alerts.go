package store

import "context"

// AlertChannelKinds are the payload formats an alert can be sent in.
var AlertChannelKinds = []string{"webhook", "discord", "slack", "email"}

// AlertChannel is a place that is told when a project's monitors go down or recover.
type AlertChannel struct {
	ID        int64
	ProjectID int64
	Kind      string // one of AlertChannelKinds
	URL       string // the address to post to, or the recipient for the email kind
}

// CreateAlertChannel adds a channel to a project and returns its id.
func (s *Store) CreateAlertChannel(ctx context.Context, c AlertChannel, nowMs int64) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		"insert into alert_channels (project_id, kind, url, created_at_ms) values (?, ?, ?, ?)",
		c.ProjectID, c.Kind, c.URL, nowMs)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// AlertChannels lists a project's channels, oldest first.
func (s *Store) AlertChannels(ctx context.Context, projectID int64) ([]AlertChannel, error) {
	rows, err := s.db.QueryContext(ctx,
		"select id, project_id, kind, url from alert_channels where project_id = ? order by id", projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var channels []AlertChannel
	for rows.Next() {
		var c AlertChannel
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.Kind, &c.URL); err != nil {
			return nil, err
		}
		channels = append(channels, c)
	}
	return channels, rows.Err()
}

// DeleteAlertChannel removes one channel of a project.
func (s *Store) DeleteAlertChannel(ctx context.Context, projectID, channelID int64) error {
	res, err := s.db.ExecContext(ctx,
		"delete from alert_channels where id = ? and project_id = ?", channelID, projectID)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}
