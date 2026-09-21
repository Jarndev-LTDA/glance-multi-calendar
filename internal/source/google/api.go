package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the Calendar API root; tests point it at a fake server.
const DefaultBaseURL = "https://www.googleapis.com/calendar/v3"

// api is a thin client for the two endpoints we need. The official library
// would add ~15 MB of dependencies for the same two GETs.
type api struct {
	base string
	http *http.Client
}

// APIError is a non-2xx answer from Google.
type APIError struct {
	Status int
	Reason string // errors[0].reason, e.g. rateLimitExceeded
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("google api: http %d %s", e.Status, e.Reason)
}

// IsRateLimit reports a 429 or a 403 with a quota reason: back off.
func (e *APIError) IsRateLimit() bool {
	if e.Status == http.StatusTooManyRequests {
		return true
	}
	if e.Status != http.StatusForbidden {
		return false
	}
	switch e.Reason {
	case "rateLimitExceeded", "userRateLimitExceeded", "quotaExceeded", "dailyLimitExceeded":
		return true
	}
	return false
}

// IsUnauthorized reports a 401: the access token is not accepted.
func (e *APIError) IsUnauthorized() bool { return e.Status == http.StatusUnauthorized }

type calendarEntry struct {
	ID              string `json:"id"`
	Summary         string `json:"summary"`
	SummaryOverride string `json:"summaryOverride"`
	Primary         bool   `json:"primary"`
	Selected        *bool  `json:"selected"`
	Hidden          bool   `json:"hidden"`
	Deleted         bool   `json:"deleted"`
}

func (c calendarEntry) name() string {
	if c.SummaryOverride != "" {
		return c.SummaryOverride
	}
	return c.Summary
}

// visible mirrors what the Google UI shows: selected and not hidden/deleted.
// `selected` absent means unchecked in the UI.
func (c calendarEntry) visible() bool {
	return !c.Hidden && !c.Deleted && c.Selected != nil && *c.Selected
}

type eventTime struct {
	Date     string `json:"date"`     // all-day: YYYY-MM-DD
	DateTime string `json:"dateTime"` // RFC3339 with offset
	TimeZone string `json:"timeZone"`
}

type eventEntry struct {
	ID          string    `json:"id"`
	Status      string    `json:"status"`
	HTMLLink    string    `json:"htmlLink"`
	Summary     string    `json:"summary"`
	Description string    `json:"description"`
	Location    string    `json:"location"`
	ICalUID     string    `json:"iCalUID"`
	EventType   string    `json:"eventType"`
	Start       eventTime `json:"start"`
	End         eventTime `json:"end"`
	Attendees   []struct {
		Self           bool   `json:"self"`
		ResponseStatus string `json:"responseStatus"`
	} `json:"attendees"`
}

// declined reports an invitation the user turned down; Google hides those.
func (e eventEntry) declined() bool {
	for _, a := range e.Attendees {
		if a.Self && a.ResponseStatus == "declined" {
			return true
		}
	}
	return false
}

func (a *api) get(ctx context.Context, path string, q url.Values, out any) error {
	u := a.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return parseAPIError(resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("google api: decode %s: %w", path, err)
	}
	return nil
}

func parseAPIError(status int, body []byte) *APIError {
	e := &APIError{Status: status, Body: string(body)}
	var env struct {
		Error struct {
			Errors []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
			Status string `json:"status"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &env) == nil && len(env.Error.Errors) > 0 {
		e.Reason = env.Error.Errors[0].Reason
	}
	return e
}

// listCalendars returns every entry of the account's calendar list.
func (a *api) listCalendars(ctx context.Context) ([]calendarEntry, error) {
	var out []calendarEntry
	q := url.Values{"maxResults": {"250"}, "showHidden": {"true"},
		"fields": {"nextPageToken,items(id,summary,summaryOverride,primary,selected,hidden,deleted)"}}
	for {
		var page struct {
			Items         []calendarEntry `json:"items"`
			NextPageToken string          `json:"nextPageToken"`
		}
		if err := a.get(ctx, "/users/me/calendarList", q, &page); err != nil {
			return nil, err
		}
		out = append(out, page.Items...)
		if page.NextPageToken == "" {
			return out, nil
		}
		q.Set("pageToken", page.NextPageToken)
	}
}

// listEvents returns the expanded occurrences of one calendar in [from, to).
func (a *api) listEvents(ctx context.Context, calendarID string, from, to time.Time) ([]eventEntry, error) {
	var out []eventEntry
	q := url.Values{
		"singleEvents": {"true"}, "orderBy": {"startTime"}, "maxResults": {"2500"},
		"timeMin": {from.Format(time.RFC3339)}, "timeMax": {to.Format(time.RFC3339)},
		"fields": {"nextPageToken,items(id,status,htmlLink,summary,description,location,iCalUID,eventType,start,end,attendees(self,responseStatus))"},
	}
	path := "/calendars/" + url.PathEscape(calendarID) + "/events"
	for {
		var page struct {
			Items         []eventEntry `json:"items"`
			NextPageToken string       `json:"nextPageToken"`
		}
		if err := a.get(ctx, path, q, &page); err != nil {
			return nil, err
		}
		out = append(out, page.Items...)
		if page.NextPageToken == "" {
			return out, nil
		}
		q.Set("pageToken", page.NextPageToken)
	}
}

// parseEventTime converts a Google start/end into an instant. All-day dates
// are midnight in loc (the calendar's own zone is irrelevant for a date).
func parseEventTime(t eventTime, loc *time.Location) (time.Time, bool, error) {
	switch {
	case t.DateTime != "":
		v, err := time.Parse(time.RFC3339, t.DateTime)
		if err != nil {
			return time.Time{}, false, err
		}
		return v.In(loc), false, nil
	case t.Date != "":
		v, err := time.ParseInLocation("2006-01-02", t.Date, loc)
		if err != nil {
			return time.Time{}, false, err
		}
		return v, true, nil
	}
	return time.Time{}, false, errors.New("event time has neither date nor dateTime")
}

// untitled is used for events without a summary (private events on shared
// calendars come through that way).
const untitled = "(sem título)"

func titleOf(e eventEntry) string {
	if s := strings.TrimSpace(e.Summary); s != "" {
		return s
	}
	return untitled
}
