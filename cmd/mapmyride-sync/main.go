package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/danp/mapmyride"
	"github.com/peterbourgon/ff"
	_ "modernc.org/sqlite"
)

func main() {
	fs := flag.NewFlagSet("mapmyride-sync", flag.ExitOnError)
	var (
		databaseFile = fs.String("database-file", "data.db", "data file path")
		username     = fs.String("username", "", "username to attribute workouts to")
		beginDay     = fs.String("begin-day", "", "beginning day to sync, in 2006-01-02 format")
		endDay       = fs.String("end-day", "", "ending day to sync, in 2006-01-02 format")
	)
	ff.Parse(fs, os.Args[1:])

	if *username == "" {
		log.Fatal("need -username")
	}

	authToken := os.Getenv("AUTH_TOKEN")
	if authToken == "" {
		log.Fatal("need AUTH_TOKEN, which can be acquired by logging in to https://www.mapmyride.com/ and grabbing the value of the auth-token cookie")
	}

	db, err := newDB(*databaseFile)
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	var begin time.Time
	if *beginDay == "" {
		latest, err := db.latestWorkoutStartedAt(ctx, *username)
		if err != nil {
			log.Fatal(err)
		}
		if !latest.IsZero() {
			// Re-sync things from 14 days before latest to account for
			// possible edits.
			begin = latest.AddDate(0, 0, -14)
		}
	} else {
		begin, err = time.Parse("2006-01-02", *beginDay)
		if err != nil {
			log.Fatal(err)
		}
	}

	end := time.Now()
	if *endDay != "" {
		end, err = time.Parse("2006-01-02", *endDay)
		if err != nil {
			log.Fatal(err)
		}
	}

	log.Println("syncing for", *username, "from", begin.Format(time.RFC3339), "to", end.Format(time.RFC3339))

	client := mapmyride.NewClient(mapmyride.StaticTokenSource(authToken))

	// TODO: break the rest of this up into more manageable chunks so
	// it's easier to, say, sync a whole year at once.
	workouts, err := client.GetWorkouts(ctx, begin, end)
	if err != nil {
		log.Fatal(err)
	}

	for _, w := range workouts {
		if err := db.sync(ctx, *username, w); err != nil {
			log.Fatal(err)
		}
	}

	if err := db.removeExtra(ctx, *username, begin, end, workouts); err != nil {
		log.Fatal(err)
	}
}

type DB struct {
	db *sql.DB
}

func newDB(filename string) (*DB, error) {
	db, err := sql.Open("sqlite", filename)
	if err != nil {
		return nil, fmt.Errorf("opening database file %q: %w", filename, err)
	}

	st := &DB{db: db}
	if err := st.init(); err != nil {
		db.Close()
		return nil, err
	}

	return st, nil
}

func (s *DB) init() error {
	for _, q := range []string{
		"create table if not exists workouts (id integer primary key, user_name text not null, name text not null, kind text not null, activity_type text, kcal integer, distance_m numeric, speed_mps numeric, duration_s integer, step_count bigint, gain_m numeric, started_at datetime, created_at datetime, updated_at datetime)",
		"create table if not exists workout_distances (workout_id integer references workouts (id), elapsed_seconds numeric, total_meters numeric)",
		"create table if not exists workout_positions (workout_id integer references workouts (id), elapsed_seconds numeric, elevation numeric, lat numeric, lng numeric)",
		"create table if not exists workout_speeds (workout_id integer references workouts (id), elapsed_seconds numeric, meters_per_second numeric)",
		"create table if not exists workout_steps (workout_id integer references workouts (id), elapsed_seconds numeric, steps numeric)",
	} {
		_, err := s.db.Exec(q)
		if err != nil {
			return err
		}
	}

	return nil
}

func (d *DB) latestWorkoutStartedAt(ctx context.Context, userName string) (time.Time, error) {
	row := d.db.QueryRowContext(ctx, "select date(max(started_at)) from workouts where user_name=?", userName)
	var latests string
	if err := row.Scan(&latests); err != nil {
		return time.Time{}, err
	}
	return time.Parse("2006-01-02", latests)
}

const timeFormat = "2006-01-02 15:04:05.999999999-07:00"

func (d *DB) sync(ctx context.Context, userName string, w mapmyride.Workout) error {
	log.Println("sync", userName, "workout started", w.StartedAt.Format(time.RFC3339), "named", w.Name)

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, t := range []string{"workout_steps", "workout_speeds", "workout_positions", "workout_distances"} {
		_, err := tx.ExecContext(ctx, "delete from "+t+" where workout_id=$1", w.ID)
		if err != nil {
			return err
		}
	}

	_, err = tx.ExecContext(ctx, "delete from workouts where id=$1", w.ID)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(
		ctx,
		"insert into workouts (id, user_name, name, kind, activity_type, kcal, distance_m, speed_mps, duration_s, step_count, gain_m, started_at, created_at, updated_at) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)",
		w.ID, userName, w.Name, w.Kind, w.ActivityType, w.Kcal, w.Distance, w.Speed,
		int(w.Duration.Seconds()), w.StepCount, w.Gain,
		w.StartedAt.Format(timeFormat), w.CreatedAt.Format(timeFormat), w.UpdatedAt.Format(timeFormat),
	)
	if err != nil {
		return err
	}

	for _, d := range w.Distances {
		_, err := tx.ExecContext(
			ctx,
			"insert into workout_distances (workout_id, elapsed_seconds, total_meters) values ($1, $2, $3)",
			w.ID, d.Elapsed.Seconds(), d.Total,
		)
		if err != nil {
			return err
		}
	}

	for _, p := range w.Positions {
		_, err := tx.ExecContext(
			ctx,
			"insert into workout_positions (workout_id, elapsed_seconds, elevation, lat, lng) values ($1, $2, $3, $4, $5)",
			w.ID, p.Elapsed.Seconds(), p.Elevation, p.Lat, p.Lng,
		)
		if err != nil {
			return err
		}
	}

	for _, s := range w.Speeds {
		_, err := tx.ExecContext(
			ctx,
			"insert into workout_speeds (workout_id, elapsed_seconds, meters_per_second) values ($1, $2, $3)",
			w.ID, s.Elapsed.Seconds(), s.MetersPerSecond,
		)
		if err != nil {
			return err
		}
	}

	for _, s := range w.Steps {
		_, err := tx.ExecContext(
			ctx,
			"insert into workout_steps (workout_id, elapsed_seconds, steps) values ($1, $2, $3)",
			w.ID, s.Elapsed.Seconds(), s.StepsInPeriod,
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (d *DB) removeExtra(ctx context.Context, userName string, begin, end time.Time, workouts []mapmyride.Workout) error {
	ids := make([]string, 0, len(workouts))
	for _, w := range workouts {
		ids = append(ids, strconv.Itoa(w.ID))
	}
	idss := strings.Join(ids, ",")

	res, err := d.db.ExecContext(ctx, "delete from workouts where started_at >= $1 and started_at <= $2 and user_name=$3 and id not in ("+idss+")", begin, end, userName)
	if err != nil {
		return err
	}
	ra, err := res.RowsAffected()
	if err != nil {
		return err
	}

	log.Println("removeExtra removed", ra, "extra workouts for", userName, "started_at between", begin, "and", end, "and not ids", idss)

	return nil
}
