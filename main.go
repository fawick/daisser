package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/gorilla/mux"
	"github.com/gorilla/sessions"
	_ "github.com/mattn/go-sqlite3"
	"io"
	"log"
	"net/http"
	"net/http/fcgi"
	"os"
	"strconv"
	"time"
)

const configFile = "config.json"

var config struct {
	UrlBase   string
	DbFile    string
	LocalMode bool
	LocalPort int
}

func writeConfig() error {
	j, err := json.MarshalIndent(&config, "", "\t")
	if err != nil {
		return err
	}
	outFile, err := os.Create(configFile)
	b := bytes.NewBuffer(j)
	if _, err := b.WriteTo(outFile); err != nil {
		return err
	}
	return nil
}

var store sessions.Store

func readConfig() error {
	config.UrlBase = ""
	config.DbFile = "positions.db"
	config.LocalMode = false
	config.LocalPort = 8080
	inFile, err := os.Open(configFile)
	if err != nil {
		return writeConfig()
	}
	dec := json.NewDecoder(inFile)
	err = dec.Decode(&config)
	if err != nil {
		return err
	}
	// add backwards-compatible defaults for new config file entries when
	// reading the config
	return writeConfig()
}

var db *sql.DB
var startTime time.Time
var logWriter io.Writer

var localPortFlag = flag.Int("local", 0, "Listen on local port instead of using FastCGI")

func init() {
	l, err := os.Create("daisser.log")
	if err != nil {
		panic(err)
	}
	logWriter = l

	err = readConfig()
	if err != nil {
		fmt.Fprintln(logWriter, err)
		panic(err)
	}
	flag.Parse()
	if *localPortFlag != 0 {
		config.LocalMode = true
		config.LocalPort = *localPortFlag
	}

	startTime = time.Now()
	db, err = sql.Open("sqlite3", config.DbFile)
	if err != nil {
		fmt.Fprintln(logWriter, err)
		panic(err)
	}
	queries := []string{
		"PRAGMA journal_mode = OFF",
		"CREATE TABLE IF NOT EXISTS positions(ts DATETIME DEFAULT CURRENT_TIMESTAMP, person TEXT, lat REAL, lon REAL, alt REAL, speed REAL, hdop REAL)",
	}
	for _, query := range queries {
		_, err = db.Exec(query)
		if err != nil {
			fmt.Fprintln(logWriter, err)
			panic(err)
		}
	}

	store = sessions.NewCookieStore([]byte("keykeykey"))
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

func NotFound(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(404)
	fmt.Fprintln(w, "404 for ", r)
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

type Feature struct {
	Type       string            `json:"type"`
	Properties map[string]string `json:"properties"`
	Geometry   struct {
		Type        string    `json:"type"`
		Coordinates []float64 `json:"coordinates"`
	} `json:"geometry"`
}

type FeatureCollection struct {
	Type     string    `json:"type"`
	Features []Feature `json:"features"`
}

func GetAllPoints(w http.ResponseWriter, r *http.Request) {
	var fc FeatureCollection
	fc.Type = "FeatureCollection"
	rows, err := db.Query("SELECT * FROM positions")
	if err != nil {
		http.Error(w, err.Error(), 500)
	}
	var lat, lon, alt, hdop, speed float64
	var ts time.Time
	var name string
	for rows.Next() {
		if err := rows.Scan(&ts, &name, &lat, &lon, &alt, &speed, &hdop); err != nil {
			log.Fatal(err)
		}
		var f Feature
		f.Type = "Feature"
		f.Properties = make(map[string]string)
		f.Properties["Time"] = ts.String()
		f.Properties["User"] = name
		f.Properties["Hdop"] = fmt.Sprint(hdop)
		f.Geometry.Type = "Point"
		f.Geometry.Coordinates = make([]float64, 2)
		f.Geometry.Coordinates[0] = lon
		f.Geometry.Coordinates[1] = lat
		fc.Features = append(fc.Features, f)

	}
	if err := rows.Err(); err != nil {
		http.Error(w, err.Error(), 500)
	}
	b, err := json.Marshal(fc)
	if err != nil {
		http.Error(w, err.Error(), 500)
	}
	w.Write(b)
}

func serveRoot(w http.ResponseWriter, r *http.Request) {
	sess, err := store.Get(r, "daissersession")
	fmt.Println("sess=", sess)
	if err != nil {
		http.Error(w, "Server Error", 500)
	}
	sess.Save(r, w)
	http.ServeFile(w, r, "static/signin.html")
}

func postLogin(w http.ResponseWriter, r *http.Request) {
	sess, err := store.Get(r, "daissersession")
	if err != nil {
		http.Error(w, "Server Error", 500)
	}
	err = r.ParseForm()
	if err != nil {
		http.Error(w, "Bad login request", 400)
	}
	log.Println(r.Form)
	if r.FormValue("user") == "test" && r.FormValue("password") == "test" {
		sess, err := store.New(r, "daissersession")
		if err != nil {
			http.Error(w, "Server Error", 500)
		}
		sess.Values["user"] = "fabian"
		sess.Save(r, w)
		http.Redirect(w, r, config.UrlBase+"/static/bootleaf.html", 302)
	} else {
		sess.AddFlash("Invalid username/password")
		http.Redirect(w, r, config.UrlBase+"/", 302)
	}
}

func postLogout(w http.ResponseWriter, r *http.Request) {
	sess, err := store.Get(r, "daissersession")
	if err != nil {
		http.Error(w, "Server Error", 500)
	}
	delete(sess.Values, "user")
	sess.Values["user"] = nil
	http.Redirect(w, r, config.UrlBase+"/", 302)
}

func authCheck(exe func(http.ResponseWriter, *http.Request)) func(http.ResponseWriter, *http.Request) {
	f := func(w http.ResponseWriter, r *http.Request) {
		sess, err := store.Get(r, "daissersession")
		if err != nil {
			http.Error(w, "Server Error", 500)
		}

		if _, ok := sess.Values["user"]; !ok {
			http.Redirect(w, r, config.UrlBase+"/", 302)
			return
		}
		exe(w, r)
	}
	return f
}

func main() {
	var r *mux.Router
	base := mux.NewRouter()
	if config.UrlBase == "" {
		r = base
	} else {
		r = base.PathPrefix(config.UrlBase).Subrouter()
	}
	r.Path("/").HandlerFunc(serveRoot)
	r.Path("/api/login").Methods("POST").HandlerFunc(postLogin)
	r.Path("/api/logout").Methods("POST").HandlerFunc(postLogout)
	r.Path("/insert").HandlerFunc(NewPositionOsmand)
	r.Path("/points").HandlerFunc(GetAllPoints)
	r.PathPrefix("/static").Handler(http.StripPrefix(config.UrlBase+"/static", http.FileServer(http.Dir("./static"))))
	base.NotFoundHandler = http.HandlerFunc(NotFound)
	var err error
	if config.LocalMode {
		s := fmt.Sprintf(":%d", config.LocalPort)
		err = http.ListenAndServe(s, base)
	} else {
		err = fcgi.Serve(nil, base)
	}
	if err != nil {
		fmt.Fprintln(logWriter, err)
	}
}
