package main

import (
	"database/sql"
	"flag"
	"fmt"
	"github.com/gorilla/mux"
	_ "github.com/mattn/go-sqlite3"
	"net/http"
	"net/http/fcgi"
	"os"
	"strconv"
	"time"
)

var db *sql.DB
var startTime time.Time

func init() {
	startTime = time.Now()
	var err error
	db, err = sql.Open("sqlite3", "positions.db")
	if err != nil {
		fmt.Println("Error opening db: ", err)
		panic("init")
	}
	queries := []string{
		"PRAGMA journal_mode = OFF",
		"CREATE TABLE IF NOT EXISTS positions(ts DATETIME DEFAULT CURRENT_TIMESTAMP, person TEXT, lat REAL, lon REAL, alt REAL, speed REAL, hdop REAL)",
	}
	for _, query := range queries {
		_, err = db.Exec(query)
		if err != nil {
			fmt.Println("Error setting up db: ", err)
			panic("init")
		}
	}
}

func NewPositionOsmand(w http.ResponseWriter, r *http.Request) {
	err := r.ParseForm()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	var lat, lon, alt, hdop, speed float64
	if d, ok := r.Form["lat"]; ok {
		lat, err = strconv.ParseFloat(d[0], 64)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
	} else {
		http.Error(w, "Required argument 'lat' not supplied", 400)
		return
	}
	if d, ok := r.Form["lon"]; ok {
		lon, err = strconv.ParseFloat(d[0], 64)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
	} else {
		http.Error(w, "Required argument 'lon' not supplied", 400)
		return
	}
	if d, ok := r.Form["altitude"]; ok {
		alt, err = strconv.ParseFloat(d[0], 64)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
	}
	if d, ok := r.Form["hdop"]; ok {
		hdop, err = strconv.ParseFloat(d[0], 64)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
	}
	if d, ok := r.Form["speed"]; ok {
		speed, err = strconv.ParseFloat(d[0], 64)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
	}
	if _, err := db.Exec("INSERT INTO positions(ts, person, lat, lon, alt, speed, hdop) VALUES(?,'fabian',?,?,?,?,?)", time.Now().Unix(), lat, lon, alt, speed, hdop); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	fmt.Fprintf(w, "ok")
}

var localPort = flag.Int("local", 0, "Listen on local port instead of using FastCGI")

func NotFound(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, "404 for ", r)
	fmt.Fprintln(w, "Environment:", os.Environ())
	fmt.Fprintln(w, "Arguments:", os.Args)
	fmt.Fprintln(w, "Started at", startTime.String(), "\t Running for", time.Since(startTime))
	pwd, _ := os.Getwd()
	fmt.Fprintln(w, "cwd: ", pwd)
	if _, err := os.Stat("killfile"); !os.IsNotExist(err) {
		fmt.Fprintln(w, "Quitting now")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		os.Remove("killfile")
		os.Exit(0)
	}
}

func main() {
	flag.Parse()
	r := mux.NewRouter()
	r.Path("/insert").HandlerFunc(NewPositionOsmand)
	r.NotFoundHandler = http.HandlerFunc(NotFound)
	var err error
	if *localPort != 0 {
		s := fmt.Sprintf(":%d", *localPort)
		err = http.ListenAndServe(s, r)
	} else {
		err = fcgi.Serve(nil, r)
	}
	if err != nil {
		fmt.Println(err)
	}
}
