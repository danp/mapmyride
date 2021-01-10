package mapmyride

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestClientGetWorkouts(t *testing.T) {
	refTime := time.Date(2020, 3, 10, 7, 32, 56, 0, time.Local)

	cases := []struct {
		name       string
		begin, end time.Time
		tws        []testWorkout
		want       []int // indices of tws
	}{
		{
			name:  "ReturnsBetweenBeginEnd",
			begin: refTime,
			end:   refTime.Add(time.Hour),
			tws: []testWorkout{
				{
					id:        1,
					name:      "before begin",
					kind:      "ride",
					startedAt: refTime.Add(-time.Minute),
				},
				{
					id:        2,
					name:      "ride between begin and end",
					kind:      "ride",
					startedAt: refTime.Add(time.Minute),
				},
				{
					id:        3,
					name:      "after end",
					kind:      "ride",
					startedAt: refTime.Add(time.Hour + time.Minute),
				},
			},
			want: []int{1},
		},
		{
			name:  "UsesStartedAtNotCreatedAt",
			begin: refTime,
			end:   refTime.Add(time.Hour),
			tws: []testWorkout{
				{
					id:        1,
					name:      "ride started and created before range",
					kind:      "ride",
					startedAt: refTime.Add(-time.Hour),
					createdAt: refTime.Add(-time.Hour),
				},
				{
					id:        2,
					name:      "ride started in range but created before range",
					kind:      "ride",
					startedAt: refTime.Add(time.Minute),
					createdAt: refTime.Add(-time.Hour),
				},
				{
					id:        3,
					name:      "ride started and created in range",
					kind:      "ride",
					startedAt: refTime.Add(2 * time.Minute),
					createdAt: refTime.Add(time.Minute),
				},
				{
					id:        4,
					name:      "ride created in range but started after range",
					kind:      "ride",
					startedAt: refTime.Add(time.Hour + time.Minute),
					createdAt: refTime.Add(time.Minute),
				},
				{
					id:        5,
					name:      "ride started and created after range",
					kind:      "ride",
					startedAt: refTime.Add(time.Hour + time.Minute),
					createdAt: refTime.Add(time.Hour + time.Minute),
				},
			},
			want: []int{1, 2},
		},
		{
			name:  "SpansMonths",
			begin: refTime,
			end:   refTime.AddDate(0, 2, 0).Add(time.Hour),
			tws: []testWorkout{
				{
					id:        1,
					name:      "first ride",
					kind:      "ride",
					startedAt: refTime,
				},
				{
					id:        2,
					name:      "second ride",
					kind:      "ride",
					startedAt: refTime.AddDate(0, 1, 0),
				},
				{
					id:        3,
					name:      "third ride",
					kind:      "ride",
					startedAt: refTime.AddDate(0, 2, 0),
				},
			},
			want: []int{0, 1, 2},
		},
		{
			name:  "PullsPositions",
			begin: refTime,
			end:   refTime.Add(time.Hour),
			tws: []testWorkout{
				{
					id:        1,
					name:      "first ride",
					kind:      "ride",
					startedAt: refTime,
					positions: []testWorkoutPosition{
						{
							elapsed:   1024 * time.Millisecond,
							elevation: 10,
							lat:       44.999999,
							lng:       -75.12345,
						},
						{
							elapsed:   8096 * time.Millisecond,
							elevation: 7,
							lat:       44.999998,
							lng:       -75.12344,
						},
						{
							elapsed:   16384 * time.Millisecond,
							elevation: 6,
							lat:       44.999997,
							lng:       -75.12343,
						},
					},
				},
			},
			want: []int{0},
		},
		{
			name:  "PullsDistances",
			begin: refTime,
			end:   refTime.Add(time.Hour),
			tws: []testWorkout{
				{
					id:        1,
					name:      "first ride",
					kind:      "ride",
					startedAt: refTime,
					distances: []testWorkoutDistance{
						{
							elapsed: 1024 * time.Millisecond,
							total:   5.12,
						},
						{
							elapsed: 8096 * time.Millisecond,
							total:   6.12,
						},
						{
							elapsed: 16384 * time.Millisecond,
							total:   7.12,
						},
					},
				},
			},
			want: []int{0},
		},
		{
			name:  "PullsSpeeds",
			begin: refTime,
			end:   refTime.Add(time.Hour),
			tws: []testWorkout{
				{
					id:        1,
					name:      "first ride",
					kind:      "ride",
					startedAt: refTime,
					speeds: []testWorkoutSpeed{
						{
							elapsed:         1024 * time.Millisecond,
							metersPerSecond: 5,
						},
						{
							elapsed:         8096 * time.Millisecond,
							metersPerSecond: 7,
						},
						{
							elapsed:         16384 * time.Millisecond,
							metersPerSecond: 9,
						},
					},
				},
			},
			want: []int{0},
		},
		{
			name:  "PullsSteps",
			begin: refTime,
			end:   refTime.Add(time.Hour),
			tws: []testWorkout{
				{
					id:        1,
					name:      "first walk",
					kind:      "walk",
					startedAt: refTime,
					steps: []testWorkoutStep{
						{
							elapsed:       1024 * time.Millisecond,
							stepsInPeriod: 5.12,
						},
						{
							elapsed:       8096 * time.Millisecond,
							stepsInPeriod: 6.12,
						},
						{
							elapsed:       16384 * time.Millisecond,
							stepsInPeriod: 7.12,
						},
					},
				},
			},
			want: []int{0},
		},
		{
			name:  "PullsGain",
			begin: refTime,
			end:   refTime.Add(time.Hour),
			tws: []testWorkout{
				{
					id:        1,
					name:      "gainful ride",
					kind:      "ride",
					startedAt: refTime,
					gain:      10,
				},
			},
			want: []int{0},
		},
		{
			name:  "SkipsGainIfBlank",
			begin: refTime,
			end:   refTime.Add(time.Hour),
			tws: []testWorkout{
				{
					id:        1,
					name:      "not so gainful ride",
					kind:      "ride",
					gainValue: " ",
					startedAt: refTime,
				},
			},
			want: []int{0},
		},
		{
			name:  "SkipsGainIfDashes",
			begin: refTime,
			end:   refTime.Add(time.Hour),
			tws: []testWorkout{
				{
					id:        1,
					name:      "not so gainful ride",
					kind:      "ride",
					gainValue: "--",
					startedAt: refTime,
				},
			},
			want: []int{0},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wsrv := newWorkoutServer()
			for _, tw := range tc.tws {
				wsrv.addWorkout(tw)
			}

			srv := httptest.NewServer(wsrv)
			defer srv.Close()

			c := NewClient(StaticTokenSource("secret"))
			c.baseURL = srv.URL

			got, err := c.GetWorkouts(context.Background(), tc.begin, tc.end)
			if err != nil {
				t.Fatal(err)
			}

			want := make([]Workout, 0, len(tc.want))
			for _, w := range tc.want {
				want = append(want, tc.tws[w].toWorkout())
			}

			if d := cmp.Diff(want, got); d != "" {
				t.Errorf("workouts mismatch (-want +got):\n%s", d)
			}
		})
	}
}

func TestMonths(t *testing.T) {
	pd := func(s string) time.Time {
		pt, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Errorf("parsing time %q as day: %s", s, err)
		}
		return pt
	}
	pm := func(s string) time.Time {
		pt, err := time.Parse("2006-01", s)
		if err != nil {
			t.Errorf("parsing time %q as month: %s", s, err)
		}
		return pt
	}

	for _, tc := range []struct {
		begin, end string
		want       []string
	}{
		{
			begin: "2010-03-05",
			end:   "2010-03-06",
			want:  []string{"2010-03"},
		},
		{
			begin: "2010-03-05",
			end:   "2010-03-05",
			want:  []string{"2010-03"},
		},
		{
			begin: "2010-03-05",
			end:   "2010-04-01",
			want:  []string{"2010-03", "2010-04"},
		},
		{
			begin: "2010-11-05",
			end:   "2011-04-01",
			want:  []string{"2010-11", "2010-12", "2011-01", "2011-02", "2011-03", "2011-04"},
		},
	} {
		var got []string
		for _, g := range months(pd(tc.begin), pd(tc.end)) {
			got = append(got, g.Format("2006-01"))
		}
		want := make([]string, 0, len(tc.want))
		for _, w := range tc.want {
			want = append(want, pm(w).Format("2006-01"))
		}

		if d := cmp.Diff(want, got); d != "" {
			t.Errorf("months(%q, %q) mismatch (-want +got):\n%s", tc.begin, tc.end, d)
		}
	}
}

type testWorkoutDistance struct {
	elapsed time.Duration
	total   float64
}

// [elapsed, distance]
func (t testWorkoutDistance) MarshalJSON() ([]byte, error) {
	out := [2]float64{t.elapsed.Seconds(), t.total}
	return json.Marshal(out)
}

type testWorkoutPosition struct {
	elapsed   time.Duration
	elevation float64
	lat, lng  float64
}

// [elapsed, { "elevation": elevation, "lat": lat, "lng": lng }]
func (t testWorkoutPosition) MarshalJSON() ([]byte, error) {
	obj := map[string]float64{
		"elevation": t.elevation,
		"lat":       t.lat,
		"lng":       t.lng,
	}
	objb, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}

	out := []json.RawMessage{
		[]byte(strconv.FormatFloat(t.elapsed.Seconds(), 'f', -1, 64)),
		objb,
	}
	return json.Marshal(out)
}

type testWorkoutSpeed struct {
	elapsed         time.Duration
	metersPerSecond float64
}

// [elapsed, mps]
func (t testWorkoutSpeed) MarshalJSON() ([]byte, error) {
	out := [2]float64{t.elapsed.Seconds(), t.metersPerSecond}
	return json.Marshal(out)
}

type testWorkoutStep struct {
	elapsed       time.Duration
	stepsInPeriod float64
}

// [elapsed, steps in period]
func (t testWorkoutStep) MarshalJSON() ([]byte, error) {
	out := [2]float64{t.elapsed.Seconds(), t.stepsInPeriod}
	return json.Marshal(out)
}

type testWorkout struct {
	id        int
	name      string
	kind      string
	kcal      int
	gain      int
	gainValue string
	distance  float64
	speed     float64
	stepCount int
	duration  time.Duration
	startedAt time.Time
	createdAt time.Time
	updatedAt time.Time

	distances []testWorkoutDistance
	positions []testWorkoutPosition
	speeds    []testWorkoutSpeed
	steps     []testWorkoutStep
}

func (w testWorkout) toWorkout() Workout {
	wk := Workout{
		ID:        w.id,
		Name:      w.name,
		Kind:      w.kind,
		Kcal:      w.kcal,
		Distance:  w.distance,
		Speed:     w.speed,
		Gain:      w.gain,
		Duration:  w.duration,
		StartedAt: w.startedAt,
		CreatedAt: w.createdAt,
		UpdatedAt: w.updatedAt,
	}

	for _, p := range w.positions {
		wk.Positions = append(wk.Positions, WorkoutPosition{
			Elapsed:   p.elapsed,
			Elevation: p.elevation,
			Lat:       p.lat,
			Lng:       p.lng,
		})
	}

	for _, d := range w.distances {
		wk.Distances = append(wk.Distances, WorkoutDistance{
			Elapsed: d.elapsed,
			Total:   d.total,
		})
	}

	for _, s := range w.speeds {
		wk.Speeds = append(wk.Speeds, WorkoutSpeed{
			Elapsed:         s.elapsed,
			MetersPerSecond: s.metersPerSecond,
		})
	}

	for _, s := range w.steps {
		wk.Steps = append(wk.Steps, WorkoutStep{
			Elapsed:       s.elapsed,
			StepsInPeriod: s.stepsInPeriod,
		})
	}

	return wk
}

type workoutServer struct {
	workouts map[int]testWorkout
	mux      *http.ServeMux
}

func newWorkoutServer() *workoutServer {
	w := &workoutServer{
		workouts: make(map[int]testWorkout),
		mux:      http.NewServeMux(),
	}

	w.mux.HandleFunc("/workouts/dashboard.json", w.dashboardHandler)
	w.mux.HandleFunc("/vxproxy/v7.0/workout/", w.apiWorkoutHandler)
	w.mux.HandleFunc("/workout/", w.uiWorkoutHandler)
	return w
}

func (w *workoutServer) addWorkout(wo testWorkout) {
	w.workouts[wo.id] = wo
}

func (w *workoutServer) dashboardHandler(wr http.ResponseWriter, req *http.Request) {
	year, err := strconv.Atoi(req.URL.Query().Get("year"))
	if err != nil {
		panic(err)
	}
	month, err := strconv.Atoi(req.URL.Query().Get("month"))
	if err != nil {
		panic(err)
	}

	out := make(map[string][]map[string]interface{})
	for _, wk := range w.workouts {
		if wk.startedAt.Year() == year && int(wk.startedAt.Month()) == month {
			key := wk.startedAt.Format("2006-01-02")
			rwk := map[string]interface{}{
				"activity_short_name": wk.kind,
				"date":                wk.startedAt.Format("01/02/2006"),
				"distance":            wk.distance / 1000.0, // kilometers
				"energy":              wk.kcal,
				"name":                wk.name,
				"speed":               wk.speed,
				"view_url":            "/workout/" + strconv.Itoa(wk.id),
			}

			if wk.stepCount > 0 {
				rwk["steps"] = wk.stepCount
			} else {
				rwk["steps"] = ""
			}
			if wk.duration > 0 {
				rwk["time"] = int(wk.duration.Seconds())
			} else {
				rwk["time"] = ""
			}

			out[key] = append(out[key], rwk)
		}
	}

	var resp struct {
		WorkoutData struct {
			Workouts map[string][]map[string]interface{} `json:"workouts"`
		} `json:"workout_data"`
	}
	resp.WorkoutData.Workouts = out

	json.NewEncoder(wr).Encode(&resp)
}

func (w *workoutServer) apiWorkoutHandler(wr http.ResponseWriter, req *http.Request) {
	if req.URL.Query().Get("field_set") != "time_series" {
		wr.WriteHeader(500)
		return
	}

	path := req.URL.Path
	if path[len(path)-1] != '/' {
		wr.WriteHeader(500)
		return
	}
	path = path[:len(path)-1]

	id, err := strconv.Atoi(path[strings.LastIndex(path, "/")+1:])
	if err != nil {
		wr.WriteHeader(500)
		return
	}

	wk, ok := w.workouts[id]
	if !ok {
		wr.WriteHeader(404)
		return
	}

	var rawresp struct {
		CreatedAt  time.Time              `json:"created_datetime"`
		StartedAt  time.Time              `json:"start_datetime"`
		UpdatedAt  time.Time              `json:"updated_datetime"`
		Timeseries map[string]interface{} `json:"time_series"`
	}

	rawresp.CreatedAt = wk.createdAt
	rawresp.StartedAt = wk.startedAt
	rawresp.UpdatedAt = wk.updatedAt

	ts := make(map[string]interface{})

	if len(wk.positions) > 0 {
		ts["position"] = wk.positions
	}

	if len(wk.distances) > 0 {
		ts["distance"] = wk.distances
	}

	if len(wk.speeds) > 0 {
		ts["speed"] = wk.speeds
	}

	if len(wk.steps) > 0 {
		ts["steps"] = wk.steps
	}

	if len(ts) > 0 {
		rawresp.Timeseries = ts
	}

	json.NewEncoder(wr).Encode(&rawresp)
}

func (w *workoutServer) uiWorkoutHandler(wr http.ResponseWriter, req *http.Request) {
	path := req.URL.Path

	id, err := strconv.Atoi(path[strings.LastIndex(path, "/")+1:])
	if err != nil {
		wr.WriteHeader(500)
		return
	}

	wk, ok := w.workouts[id]
	if !ok {
		wr.WriteHeader(404)
		return
	}

	if wk.gain == 0 {
		fmt.Fprintln(wr, `<p>hello</p>`)
		return
	}

	gain := wk.gainValue
	if gain == "" {
		gain = strconv.Itoa(wk.gain)
	}

	fmt.Fprintln(wr, `
<table id="workout_elevation_data" class="mmf_workout_table">
                <thead>
                    <tr>
                        <th colspan="2" scope="col">Elevation</th>
                    </tr>
                </thead>
                <tbody>
                    <tr>
                        <th scope="row">Gain</th>
                        <td>
                                <span class="notranslate">   `+gain+`      <!-- ensure space trimming --></span>
                                <span class="unit">m</span>
                        </td>
                    </tr>
                    <tr>
                        <th scope="row">Start</th>
                        <td>
                                <span class="notranslate">61</span>
                                <span class="unit">m</span>
                        </td>
                    </tr>
                    <tr>
                        <th scope="row">Max</th>
                        <td>
                                <span class="notranslate">61</span>
                                <span class="unit">m</span>
                        </td>
                    </tr>
                </tbody>
            </table>`)
}

func (w *workoutServer) ServeHTTP(wr http.ResponseWriter, req *http.Request) {
	w.mux.ServeHTTP(wr, req)
}
