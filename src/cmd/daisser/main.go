package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/http/fcgi"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/sessions"
	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/bcrypt"
)

const configFile = "config.json"

var config struct {
	UrlBase   string
	DbFile    string
	LocalMode bool
	LocalPort int
}

var cachedTemplates = map[string]*template.Template{}
var cachedMutex sync.Mutex

var store sessions.Store
var db *sql.DB
var startTime time.Time
var logWriter io.Writer

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

	localPortFlag := flag.Int("local", 0, "Listen on local port instead of using FastCGI")
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
		"CREATE TABLE IF NOT EXISTS credentials(username TEXT PRIMARY KEY NOT NULL, password TEXT NOT NULL)",
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

func T(name string) *template.Template {
	cachedMutex.Lock()
	defer cachedMutex.Unlock()

	if t, ok := cachedTemplates[name]; ok {
		return t
	}

	t := template.Must(template.ParseFiles(
		filepath.Join("static", name),
	))
	cachedTemplates[name] = t

	return t
}

func NewPositionOsmand(w http.ResponseWriter, r *http.Request) {
	err := r.ParseForm()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var lat, lon, alt, hdop, speed float64
	if d, ok := r.Form["lat"]; ok {
		lat, err = strconv.ParseFloat(d[0], 64)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		http.Error(w, "Required argument 'lat' not supplied", http.StatusBadRequest)
		return
	}
	if d, ok := r.Form["lon"]; ok {
		lon, err = strconv.ParseFloat(d[0], 64)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		http.Error(w, "Required argument 'lon' not supplied", http.StatusBadRequest)
		return
	}
	if d, ok := r.Form["altitude"]; ok {
		alt, err = strconv.ParseFloat(d[0], 64)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if d, ok := r.Form["hdop"]; ok {
		hdop, err = strconv.ParseFloat(d[0], 64)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if d, ok := r.Form["speed"]; ok {
		speed, err = strconv.ParseFloat(d[0], 64)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if _, err := db.Exec("INSERT INTO positions(ts, person, lat, lon, alt, speed, hdop) VALUES(?,'fabian',?,?,?,?,?)", time.Now().Unix(), lat, lon, alt, speed, hdop); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, "ok")
}

func NotFound(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotFound)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	b, err := json.Marshal(fc)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	w.Write(b)
}

func runTemplate(w http.ResponseWriter, r *http.Request, name string) {
	sess, err := store.Get(r, "daissersession")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	buf := new(bytes.Buffer)
	T(name).Execute(buf, sess)
	sess.Save(r, w)
	buf.WriteTo(w)
}

func serveLogin(w http.ResponseWriter, r *http.Request) {
	runTemplate(w, r, "signin.html")
}

func serveMap(w http.ResponseWriter, r *http.Request) {
	runTemplate(w, r, "bootleaf.html")
}

func postLogin(w http.ResponseWriter, r *http.Request) {
	sess, err := store.Get(r, "daissersession")
	if err != nil {
		http.Error(w, "Server Error", http.StatusInternalServerError)
		return
	}
	err = r.ParseForm()
	if err != nil {
		http.Error(w, "Bad login request", 400)
		return
	}
	username := r.FormValue("username")
	password := r.FormValue("password")
	var encryptedPassword string

	err = db.QueryRow("SELECT password FROM credentials WHERE username=?", username).Scan(&encryptedPassword)
	log.Println(err)

	if err != nil && err != sql.ErrNoRows {
		http.Error(w, "Server Error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	log.Println(encryptedPassword, password)

	err = bcrypt.CompareHashAndPassword([]byte(encryptedPassword), []byte(password))
	if err == nil {
		sess, err := store.New(r, "daissersession")
		if err != nil {
			http.Error(w, "Server Error", http.StatusInternalServerError)
		}
		sess.Values["user"] = username
		sess.Save(r, w)
		http.Redirect(w, r, config.UrlBase+"/map", http.StatusSeeOther)
	} else {
		log.Println(err)
		delete(sess.Values, "user")
		sess.AddFlash("Invalid username/password")
		sess.Save(r, w)
		http.Redirect(w, r, config.UrlBase+"/", http.StatusSeeOther)
	}
}

func postLogout(w http.ResponseWriter, r *http.Request) {
	sess, err := store.Get(r, "daissersession")
	if err != nil {
		http.Error(w, "Server Error", http.StatusInternalServerError)
	}
	log.Println("Logging out", sess.Values["user"])
	delete(sess.Values, "user")
	sess.Save(r, w)
	http.Redirect(w, r, config.UrlBase+"/", http.StatusSeeOther)
}

func authCheck(exe func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	f := func(w http.ResponseWriter, r *http.Request) {
		sess, err := store.Get(r, "daissersession")
		if err != nil {
			http.Error(w, "Server Error", http.StatusInternalServerError)
		}
		fmt.Println(sess)
		if _, ok := sess.Values["user"]; !ok { // TODO handle case that user is not in DB
			http.Redirect(w, r, config.UrlBase+"/", http.StatusSeeOther)
			return
		}
		exe(w, r)
	}
	return f
}

func setPassword(username, password string) {
	hpass, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		panic(err) //this is a panic because bcrypt errors on invalid costs
	}
	log.Println(string(hpass))

	if _, err = db.Exec("REPLACE INTO credentials VALUES(?,?)", username, string(hpass)); err != nil {
		log.Println("error during insert", err)
		return
	}
}

func main() {
	var r *mux.Router
	base := mux.NewRouter()
	if config.UrlBase == "" {
		r = base
	} else {
		r = base.PathPrefix(config.UrlBase).Subrouter()
	}

	// default access
	r.Path("/").HandlerFunc(serveLogin)
	r.Path("/api/login").Methods("POST").HandlerFunc(postLogin)
	r.Path("/api/insertOsmand").HandlerFunc(NewPositionOsmand)
	r.PathPrefix("/static/default/").Handler(http.StripPrefix(config.UrlBase+"/static/default/", http.FileServer(http.Dir("static/default"))))

	r.Path("/map").HandlerFunc(authCheck(serveMap))
	r.Path("/api/logout").HandlerFunc(authCheck(postLogout))
	r.Path("/api/points").HandlerFunc(authCheck(GetAllPoints))

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
