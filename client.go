package mapmyride

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/sync/errgroup"
)

// WorkoutDistance is a point in time distance measurement
// for a workout, in meters.
//
// Note that Elapsed may not necessarily track wall clock
// time from the workout's start time due to pauses during
// the workout.
type WorkoutDistance struct {
	Elapsed time.Duration
	Total   float64 // meters
}

// WorkoutPosition is a point in time position record for
// a workout. Elevation is in meters.
//
// Note that Elapsed may not necessarily track wall clock
// time from the workout's start time due to pauses during
// the workout.
type WorkoutPosition struct {
	Elapsed   time.Duration
	Elevation float64 // meters
	Lat       float64
	Lng       float64
}

// WorkoutSpeed is a point in time speed measurement for
// a workout.
//
// Note that Elapsed may not necessarily track wall clock
// time from the workout's start time due to pauses during
// the workout.
type WorkoutSpeed struct {
	Elapsed         time.Duration
	MetersPerSecond float64
}

// WorkoutSpeed is a point in time step count measurement for
// a workout.
//
// Note that Elapsed may not necessarily track wall clock
// time from the workout's start time due to pauses during
// the workout.
type WorkoutStep struct {
	Elapsed       time.Duration
	StepsInPeriod float64
}

// Workout is a recorded workout.
type Workout struct {
	ID        int
	Name      string
	Kind      string
	Kcal      int
	Distance  float64 // meters
	Speed     float64 // meters per second
	Duration  time.Duration
	StepCount int
	Gain      int // meters
	StartedAt time.Time
	CreatedAt time.Time
	UpdatedAt time.Time

	Distances []WorkoutDistance
	Positions []WorkoutPosition
	Speeds    []WorkoutSpeed
	Steps     []WorkoutStep
}

// Token is a token used for authentication.
//
// In the future it may be expanded to support an expiry.
type Token struct {
	Token string
}

// TokenSource provides a Token.
type TokenSource interface {
	Token() (Token, error)
}

// StaticTokenSource is a TokenSource which always returns
// the underlying string.
type StaticTokenSource string

func (s StaticTokenSource) Token() (Token, error) {
	return Token{Token: string(s)}, nil
}

// Client is a client for the MapMyRide service.
type Client struct {
	// HTTPDo is used to make HTTP requests, if provided.
	// Otherwise, http.DefaultClient.Do is used.
	HTTPDo func(*http.Request) (*http.Response, error)

	tokenSource TokenSource
	baseURL     string
}

// NewClient returns a new Client using the given tokenSource.
func NewClient(tokenSource TokenSource) *Client {
	return &Client{tokenSource: tokenSource}
}

// GetWorkouts retrieves workouts with "started at" times between
// begin and end, inclusive.
func (c *Client) GetWorkouts(ctx context.Context, begin, end time.Time) ([]Workout, error) {
	beginDate, endDate := toDate(begin), toDate(end)

	var workouts []Workout
	for _, m := range months(begin, end) {
		mwks, err := c.getMonthWorkoutsForRange(ctx, m.Year(), int(m.Month()), beginDate, endDate)
		if err != nil {
			return nil, err
		}
		for _, wk := range mwks {
			wk := wk
			if err := c.fillWorkout(ctx, &wk); err != nil {
				return nil, err
			}
			if wk.StartedAt.Before(begin) || wk.StartedAt.After(end) {
				continue
			}
			workouts = append(workouts, wk)
		}
	}
	sort.Slice(workouts, func(i, j int) bool { return workouts[i].StartedAt.Before(workouts[j].StartedAt) })

	return workouts, nil
}

func (c *Client) getMonthWorkoutsForRange(ctx context.Context, year, month int, beginDate, endDate time.Time) ([]Workout, error) {
	req, err := c.newRequest(ctx, "GET", "/workouts/dashboard.json")
	if err != nil {
		return nil, err
	}

	q := make(url.Values)
	q.Set("year", strconv.Itoa(year))
	q.Set("month", strconv.Itoa(month))
	req.URL.RawQuery = q.Encode()

	resp, err := c.httpDo(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("got status %d", resp.StatusCode)
	}

	var rawresp struct {
		WorkoutData struct {
			Workouts map[string][]struct {
				Name              string
				Date              string
				ActivityShortName string `json:"activity_short_name"`
				Distance          float64
				Energy            int
				Speed             float64
				Steps             json.RawMessage
				Time              json.RawMessage
				ViewURL           string `json:"view_url"`
			}
		} `json:"workout_data"`
	}

	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(b, &rawresp); err != nil {
		return nil, err
	}

	var workouts []Workout
	for _, rws := range rawresp.WorkoutData.Workouts {
		for _, rw := range rws {
			dt, err := time.ParseInLocation("01/02/2006", rw.Date, time.UTC)
			if err != nil {
				return nil, fmt.Errorf("converting %q to date: %s", rw.Date, err)
			}

			// Fetching dashboard for a given year and month can return
			// results from previous or next month.
			if !(dt.Year() == year && int(dt.Month()) == month) {
				continue
			}

			if dt.Before(beginDate) || dt.After(endDate) {
				continue
			}

			viewURLParts := strings.Split(rw.ViewURL, "/")
			id, err := strconv.Atoi(viewURLParts[2])
			if err != nil {
				return nil, fmt.Errorf("converting %q to id: %s", rw.ViewURL, err)
			}

			wk := Workout{
				ID:       id,
				Name:     rw.Name,
				Kind:     rw.ActivityShortName,
				Kcal:     rw.Energy,
				Distance: rw.Distance * 1000,
				Speed:    rw.Speed,
			}

			if i, err := strconv.Atoi(string(rw.Steps)); err == nil {
				wk.StepCount = i
			}
			if i, err := strconv.Atoi(string(rw.Time)); err == nil {
				wk.Duration = time.Duration(i) * time.Second
			}

			workouts = append(workouts, wk)
		}
	}

	return workouts, nil
}

func (c *Client) fillWorkout(ctx context.Context, wk *Workout) error {
	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		return c.fillMainData(ctx, wk)
	})

	g.Go(func() error {
		return c.fillGainData(ctx, wk)
	})

	return g.Wait()
}

func (c *Client) fillMainData(ctx context.Context, wk *Workout) error {
	req, err := c.newRequest(ctx, "GET", "/vxproxy/v7.0/workout/"+strconv.Itoa(wk.ID)+"/")
	if err != nil {
		return err
	}

	q := make(url.Values)
	q.Set("field_set", "time_series")
	req.URL.RawQuery = q.Encode()

	resp, err := c.httpDo(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("got status %d", resp.StatusCode)
	}

	var rawresp struct {
		CreatedAt  time.Time                  `json:"created_datetime"`
		StartedAt  time.Time                  `json:"start_datetime"`
		UpdatedAt  time.Time                  `json:"updated_datetime"`
		Timeseries map[string]json.RawMessage `json:"time_series"`
	}

	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(b, &rawresp); err != nil {
		return err
	}

	wk.CreatedAt = rawresp.CreatedAt
	wk.StartedAt = rawresp.StartedAt
	wk.UpdatedAt = rawresp.UpdatedAt

	for k, v := range rawresp.Timeseries {
		switch k {
		case "distance":
			var rawDistances [][2]float64

			if err := json.Unmarshal(v, &rawDistances); err != nil {
				return err
			}

			for _, rd := range rawDistances {
				wk.Distances = append(wk.Distances, WorkoutDistance{
					Elapsed: time.Duration(rd[0]*1000) * time.Millisecond,
					Total:   rd[1],
				})
			}
		case "position":
			var rawPositions [][2]json.RawMessage

			if err := json.Unmarshal(v, &rawPositions); err != nil {
				return err
			}

			for _, rp := range rawPositions {
				var pos WorkoutPosition

				if err := json.Unmarshal(rp[1], &pos); err != nil {
					return err
				}

				var el float64
				if err := json.Unmarshal(rp[0], &el); err != nil {
					return err
				}
				pos.Elapsed = time.Duration(el*1000) * time.Millisecond

				wk.Positions = append(wk.Positions, pos)
			}
		case "speed":
			var rawSpeeds [][2]float64

			if err := json.Unmarshal(v, &rawSpeeds); err != nil {
				return err
			}

			for _, rs := range rawSpeeds {
				wk.Speeds = append(wk.Speeds, WorkoutSpeed{
					Elapsed:         time.Duration(rs[0]*1000) * time.Millisecond,
					MetersPerSecond: rs[1],
				})
			}
		case "steps":
			var rawSteps [][2]float64

			if err := json.Unmarshal(v, &rawSteps); err != nil {
				return err
			}

			for _, rs := range rawSteps {
				wk.Steps = append(wk.Steps, WorkoutStep{
					Elapsed:       time.Duration(rs[0]*1000) * time.Millisecond,
					StepsInPeriod: rs[1],
				})
			}
		}
	}

	return nil
}

func (c *Client) fillGainData(ctx context.Context, wk *Workout) error {
	req, err := c.newRequest(ctx, "GET", "/workout/"+strconv.Itoa(wk.ID))
	if err != nil {
		return err
	}

	resp, err := c.httpDo(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("got status %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return fmt.Errorf("creating query document: %w", err)
	}

	elem := doc.Find("#workout_elevation_data > tbody:nth-child(2) > tr:nth-child(1)")
	if elem.Length() == 0 {
		return nil
	}

	if elem.Find("th").First().Text() != "Gain" {
		return fmt.Errorf("unable to detect gain for workout %d", wk.ID)
	}

	gains := strings.TrimSpace(elem.Find("td > span").Eq(0).Text())
	if gains == "" || gains == "--" {
		return nil
	}

	gain, err := strconv.Atoi(gains)
	if err != nil {
		return err
	}

	wk.Gain = gain
	return nil
}

func (c *Client) newRequest(ctx context.Context, method, path string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.url(path), nil)
	if err != nil {
		return nil, err
	}

	tok, err := c.tokenSource.Token()
	if err != nil {
		return nil, err
	}

	req.Header.Set("user-agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 11_1) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/87.0.4280.88 Safari/537.36")
	req.Header.Set("cookie", "auth-token="+tok.Token)

	return req, nil
}

func (c *Client) url(path string) string {
	base := c.baseURL
	if base == "" {
		base = "https://www.mapmyride.com"
	}
	return base + path
}

func (c *Client) httpDo(req *http.Request) (*http.Response, error) {
	if c.HTTPDo != nil {
		return c.HTTPDo(req)
	}
	return http.DefaultClient.Do(req)
}

func months(begin, end time.Time) []time.Time {
	norm := func(t time.Time) time.Time {
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	cur := norm(begin)
	end = norm(end)

	out := []time.Time{cur}
	for cur.Before(end) {
		cur = cur.AddDate(0, 1, 0)
		out = append(out, cur)
	}
	return out
}

func toDate(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
