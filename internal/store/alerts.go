package store

import (
	"context"
	"database/sql"
)

// AlertChannelKinds are the payload formats an alert can be sent in.
var AlertChannelKinds = []string{"webhook", "discord", "slack", "email"}

// AlertChannel is a place that can be told when a monitor goes down or
// recovers. Channels belong to the install, and each monitor names the ones it alerts.
type AlertChannel struct {
	ID   int64
	Kind string // one of AlertChannelKinds
	URL  string // the address to post to, or the recipient for the email kind
}

// CreateAlertChannel adds a channel and returns its id. Every monitor there
// is starts alerting it, so that adding a channel never leaves a monitor silent by accident.
func (s *Store) CreateAlertChannel(ctx context.Context, c AlertChannel, nowMs int64) (int64, error) {
	var id int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			"insert into alert_channels (kind, url, created_at_ms) values (?, ?, ?)", c.Kind, c.URL, nowMs)
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "insert into monitor_channels (monitor_id, channel_id) select id, ? from monitors", id)
		return err
	})
	return id, err
}

func (s *Store) channels(ctx context.Context, query string, args ...any) ([]AlertChannel, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var channels []AlertChannel
	for rows.Next() {
		var c AlertChannel
		if err := rows.Scan(&c.ID, &c.Kind, &c.URL); err != nil {
			return nil, err
		}
		channels = append(channels, c)
	}
	return channels, rows.Err()
}

// AlertChannels lists every channel, oldest first.
func (s *Store) AlertChannels(ctx context.Context) ([]AlertChannel, error) {
	return s.channels(ctx, "select id, kind, url from alert_channels order by id")
}

// MonitorChannels lists the channels a monitor alerts, oldest first.
func (s *Store) MonitorChannels(ctx context.Context, monitorID int64) ([]AlertChannel, error) {
	return s.channels(ctx, `
		select c.id, c.kind, c.url from alert_channels c
		join monitor_channels mc on mc.channel_id = c.id
		where mc.monitor_id = ? order by c.id`, monitorID)
}

// SetMonitorChannels replaces the channels a monitor alerts. An id that is no channel is left out.
func (s *Store) SetMonitorChannels(ctx context.Context, monitorID int64, channelIDs []int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "delete from monitor_channels where monitor_id = ?", monitorID); err != nil {
			return err
		}
		for _, id := range channelIDs {
			if _, err := tx.ExecContext(ctx, `
				insert into monitor_channels (monitor_id, channel_id)
				select ?, id from alert_channels where id = ?`, monitorID, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteAlertChannel removes a channel.
func (s *Store) DeleteAlertChannel(ctx context.Context, channelID int64) error {
	res, err := s.db.ExecContext(ctx, "delete from alert_channels where id = ?", channelID)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}
