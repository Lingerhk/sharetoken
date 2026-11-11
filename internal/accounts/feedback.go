package accounts

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Feedback categories, sources and states.
const (
	FeedbackNew  = "new"
	FeedbackDone = "done"

	FeedbackMaxRunes = 4000
	FeedbackPerHour  = 10 // per user
)

// ErrTooMuchFeedback means the user reached FeedbackPerHour.
var ErrTooMuchFeedback = errors.New("accounts: too much feedback; try again later")

// Feedback is one message a user sent from the portal or the desktop client.
type Feedback struct {
	ID         int64  `json:"id"`
	UserID     int64  `json:"user_id"`
	Username   string `json:"username,omitempty"`
	Category   string `json:"category"` // bug, idea or other
	Message    string `json:"message"`
	Source     string `json:"source"` // portal or client
	AppVersion string `json:"app_version,omitempty"`
	OS         string `json:"os,omitempty"`
	Lang       string `json:"lang,omitempty"`
	Status     string `json:"status"`
	CreatedAt  int64  `json:"created_at"` // unix nanos
	HandledAt  int64  `json:"handled_at,omitempty"`
	HandledBy  string `json:"handled_by,omitempty"`
}

var (
	feedbackLangRE = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{1,8})*$`)
	feedbackMetaRE = regexp.MustCompile(`^[A-Za-z0-9._+/ -]*$`)
)

// AddFeedback stores f for userID after validating it. Only the fields a user
// may set are read from f.
func (s *Store) AddFeedback(userID int64, f Feedback) (*Feedback, error) {
	f.Message = strings.TrimSpace(f.Message)
	switch {
	case f.Message == "":
		return nil, fmt.Errorf("%w: the message is empty", ErrInvalid)
	case !utf8.ValidString(f.Message) || utf8.RuneCountInString(f.Message) > FeedbackMaxRunes:
		return nil, fmt.Errorf("%w: the message must be valid text of at most %d characters", ErrInvalid, FeedbackMaxRunes)
	case f.Category != "bug" && f.Category != "idea" && f.Category != "other":
		return nil, fmt.Errorf("%w: category must be bug, idea or other", ErrInvalid)
	case f.Source != "portal" && f.Source != "client":
		return nil, fmt.Errorf("%w: source must be portal or client", ErrInvalid)
	case len(f.AppVersion) > 64 || len(f.OS) > 64 || !feedbackMetaRE.MatchString(f.AppVersion) || !feedbackMetaRE.MatchString(f.OS):
		return nil, fmt.Errorf("%w: bad app_version or os", ErrInvalid)
	case f.Lang != "" && (len(f.Lang) > 16 || !feedbackLangRE.MatchString(f.Lang)):
		return nil, fmt.Errorf("%w: bad lang", ErrInvalid)
	}
	now := s.now()
	f.UserID, f.Status, f.CreatedAt, f.Username, f.HandledAt, f.HandledBy = userID, FeedbackNew, now.UnixNano(), "", 0, ""
	err := s.tx(func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM feedback WHERE user_id = ? AND created_at > ?`, userID, now.Add(-time.Hour).UnixNano()).Scan(&n); err != nil {
			return err
		}
		if n >= FeedbackPerHour {
			return ErrTooMuchFeedback
		}
		res, err := tx.Exec(`INSERT INTO feedback (user_id, category, message, source, app_version, os, lang, status, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			userID, f.Category, f.Message, f.Source, f.AppVersion, f.OS, f.Lang, f.Status, f.CreatedAt)
		if err != nil {
			return err
		}
		f.ID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return nil, err
	}
	return &f, nil
}

const feedbackColumns = `f.id, f.user_id, u.username, f.category, f.message, f.source, f.app_version, f.os, f.lang, f.status, f.created_at, f.handled_at, f.handled_by`

// FeedbackQuery narrows and pages the feedback listing, newest first.
type FeedbackQuery struct {
	UserID   int64  // 0: everyone
	Status   string // "": any
	From, To int64  // created_at in unix nanos: From inclusive, To exclusive, 0 unbounded
	Offset   int
	Limit    int
}

func (f FeedbackQuery) where() (string, []any) {
	to := f.To
	if to <= 0 {
		to = 1<<63 - 1
	}
	q, args := `f.created_at >= ? AND f.created_at < ?`, []any{f.From, to}
	if f.UserID != 0 {
		q, args = q+` AND f.user_id = ?`, append(args, f.UserID)
	}
	if f.Status != "" {
		q, args = q+` AND f.status = ?`, append(args, f.Status)
	}
	return q, args
}

// CountFeedback counts what ListFeedback pages through.
func (s *Store) CountFeedback(f FeedbackQuery) (int64, error) {
	where, args := f.where()
	var n int64
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM feedback f WHERE `+where, args...).Scan(&n)
	return n, err
}

// ListFeedback lists the feedback f selects, newest first.
func (s *Store) ListFeedback(f FeedbackQuery) ([]Feedback, error) {
	where, args := f.where()
	rows, err := s.DB.Query(`SELECT `+feedbackColumns+` FROM feedback f JOIN users u ON u.id = f.user_id WHERE `+where+` ORDER BY f.id DESC LIMIT ? OFFSET ?`,
		append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Feedback{}
	for rows.Next() {
		var f Feedback
		if err := rows.Scan(&f.ID, &f.UserID, &f.Username, &f.Category, &f.Message, &f.Source, &f.AppVersion, &f.OS, &f.Lang, &f.Status, &f.CreatedAt, &f.HandledAt, &f.HandledBy); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// NewFeedbackCount is how many messages wait for an admin.
func (s *Store) NewFeedbackCount() (int64, error) {
	var n int64
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM feedback WHERE status = ?`, FeedbackNew).Scan(&n)
	return n, err
}

// SetFeedbackStatus marks a message handled (by whom and when) or reopens it.
func (s *Store) SetFeedbackStatus(id int64, status, by string) error {
	var at int64
	switch status {
	case FeedbackDone:
		at = s.now().UnixNano()
	case FeedbackNew:
		by = ""
	default:
		return fmt.Errorf("%w: status must be new or done", ErrInvalid)
	}
	return s.tx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE feedback SET status = ?, handled_at = ?, handled_by = ? WHERE id = ?`, status, at, by, id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}
