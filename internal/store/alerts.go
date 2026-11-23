package store

import (
	"context"
	"database/sql"
	"errors"
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
		var err error
		if id, err = insertAlertChannel(ctx, tx.ExecContext, c, nowMs); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "insert into monitor_channels (monitor_id, channel_id) select id, ? from monitors", id)
		return err
	})
	return id, err
}

// CreateUnassignedAlertChannel adds a channel without changing monitor assignments.
func (s *Store) CreateUnassignedAlertChannel(ctx context.Context, c AlertChannel, nowMs int64) (int64, error) {
	return insertAlertChannel(ctx, s.db.ExecContext, c, nowMs)
}

func insertAlertChannel(ctx context.Context, exec execSQL, c AlertChannel, nowMs int64) (int64, error) {
	res, err := exec(ctx, "insert into alert_channels (kind, url, created_at_ms) values (?, ?, ?)", c.Kind, c.URL, nowMs)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func scanAlertChannel(row rowScanner) (AlertChannel, error) {
	var c AlertChannel
	err := row.Scan(&c.ID, &c.Kind, &c.URL)
	if errors.Is(err, sql.ErrNoRows) {
		return AlertChannel{}, ErrNotFound
	}
	return c, err
}

const alertChannelByID = "select id, kind, url from alert_channels where id = ?"

func (s *Store) AlertChannel(ctx context.Context, id int64) (AlertChannel, error) {
	return scanAlertChannel(s.db.QueryRowContext(ctx, alertChannelByID, id))
}

// EditAlertChannel changes configuration without changing monitor assignments.
func (s *Store) EditAlertChannel(ctx context.Context, id int64, edit func(*AlertChannel) error) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		channel, err := scanAlertChannel(tx.QueryRowContext(ctx, alertChannelByID, id))
		if err != nil {
			return err
		}
		if err := edit(&channel); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "update alert_channels set kind = ?, url = ? where id = ?", channel.Kind, channel.URL, id)
		if err != nil {
			return err
		}
		return requireOneRow(res)
	})
}

func (s *Store) channels(ctx context.Context, query string, args ...any) ([]AlertChannel, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var channels []AlertChannel
	for rows.Next() {
		c, err := scanAlertChannel(rows)
		if err != nil {
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

// AlertChannelsAfter lists the next alert channels in ID order.
func (s *Store) AlertChannelsAfter(ctx context.Context, after int64, limit int) ([]AlertChannel, error) {
	return s.channels(ctx, "select id, kind, url from alert_channels where id > ? order by id limit ?", after, limit)
}

// MonitorChannels lists the channels a monitor alerts, oldest first.
func (s *Store) MonitorChannels(ctx context.Context, monitorID int64) ([]AlertChannel, error) {
	return s.channels(ctx, `
		select c.id, c.kind, c.url from alert_channels c
		join monitor_channels mc on mc.channel_id = c.id
		where mc.monitor_id = ? order by c.id`, monitorID)
}

// MonitorChannelIDs returns channel IDs without loading the private destinations.
func (s *Store) MonitorChannelIDs(ctx context.Context, monitorID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		select mc.channel_id from monitors m left join monitor_channels mc on mc.monitor_id = m.id
		where m.id = ? order by mc.channel_id`, monitorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, ErrNotFound
	}
	var ids []int64
	for {
		var id sql.NullInt64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id.Valid {
			ids = append(ids, id.Int64)
		}
		if !rows.Next() {
			return ids, rows.Err()
		}
	}
}

// SetMonitorChannels replaces the channels a monitor alerts. An id that is no channel is left out.
func (s *Store) SetMonitorChannels(ctx context.Context, monitorID int64, channelIDs []int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := setMonitorChannels(ctx, tx, monitorID, channelIDs)
		return err
	})
}

// ErrUnknownChannel means a requested channel reference did not match a channel.
var ErrUnknownChannel = errors.New("unknown alert channel")

// ReplaceMonitorChannels applies all requested assignments or leaves them intact.
func (s *Store) ReplaceMonitorChannels(ctx context.Context, monitorID int64, channelIDs []int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, "select exists (select 1 from monitors where id = ?)", monitorID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		count, err := setMonitorChannels(ctx, tx, monitorID, channelIDs)
		if err != nil {
			return err
		}
		if count != int64(len(channelIDs)) {
			return ErrUnknownChannel
		}
		return nil
	})
}

func setMonitorChannels(ctx context.Context, tx *sql.Tx, monitorID int64, channelIDs []int64) (int64, error) {
	if _, err := tx.ExecContext(ctx, "delete from monitor_channels where monitor_id = ?", monitorID); err != nil {
		return 0, err
	}
	var count int64
	for _, id := range channelIDs {
		result, err := tx.ExecContext(ctx, `
			insert into monitor_channels (monitor_id, channel_id)
			select ?, id from alert_channels where id = ?`, monitorID, id)
		if err != nil {
			return 0, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		count += n
	}
	return count, nil
}

// DeleteAlertChannel removes a channel.
func (s *Store) DeleteAlertChannel(ctx context.Context, channelID int64) error {
	res, err := s.db.ExecContext(ctx, "delete from alert_channels where id = ?", channelID)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}
