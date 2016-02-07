package main

import (
	"bytes"
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
	"sync"
	"time"

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
	// TODO iterate over positions
	//var lat, lon, alt, hdop, speed float64
	//var ts time.Time
	//var name string
	//for rows.Next() {
	//if err := rows.Scan(&ts, &name, &lat, &lon, &alt, &speed, &hdop); err != nil {
	//log.Fatal(err)
	//}
	//var f Feature
	//f.Type = "Feature"
	//f.Properties = make(map[string]string)
	//f.Properties["Time"] = ts.String()
	//f.Properties["User"] = name
	//f.Properties["Hdop"] = fmt.Sprint(hdop)
	//f.Geometry.Type = "Point"
	//f.Geometry.Coordinates = make([]float64, 2)
	//f.Geometry.Coordinates[0] = lon
	//f.Geometry.Coordinates[1] = lat
	//fc.Features = append(fc.Features, f)
	//}
	b, err := json.Marshal(fc)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	w.Write(b)
}

func runTemplate(w http.ResponseWriter, r *http.Request, name string) {
	buf := new(bytes.Buffer)
	T(name).Execute(buf, nil) // TODO add correct data here
	buf.WriteTo(w)
}

func serveLogin(w http.ResponseWriter, r *http.Request) {
	runTemplate(w, r, "signin.html")
}

func serveMap(w http.ResponseWriter, r *http.Request) {
	runTemplate(w, r, "bootleaf.html")
}

func postLogin(w http.ResponseWriter, r *http.Request) {
	err := r.ParseForm()
	if err != nil {
		http.Error(w, "Bad login request", 400)
		return
	}
	username := r.FormValue("username")
	password := r.FormValue("password")
	var encryptedPassword string
	_ = username

	// TODO check request

	log.Println("PASSWORD for testing: ", encryptedPassword, password)

	err = bcrypt.CompareHashAndPassword([]byte(encryptedPassword), []byte(password))
	if err == nil {
		// TODO save cookie
		http.Redirect(w, r, config.UrlBase+"/map", http.StatusSeeOther)
	} else {
		log.Println(err)
		// TODO error
		http.Redirect(w, r, config.UrlBase+"/", http.StatusSeeOther)
	}
}

func postLogout(w http.ResponseWriter, r *http.Request) {
	log.Println("Logging out")
	// TODO delete cookie
	http.Redirect(w, r, config.UrlBase+"/", http.StatusSeeOther)
}

func authCheck(exe func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	// TODO check authentication
	f := func(w http.ResponseWriter, r *http.Request) {
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

	// TODO make password persistent
}

func main() {
	var mux = http.DefaultServeMux
	if config.UrlBase != "" {
		// TODO STRIP PREFIXES
	}

	// default access
	//r.Path("/").HandlerFunc(serveLogin)
	//r.Path("/api/login").Methods("POST").HandlerFunc(postLogin)
	//r.PathPrefix("/static/default/").Handler(http.StripPrefix(config.UrlBase+"/static/default/", http.FileServer(http.Dir("static/default"))))

	//r.Path("/map").HandlerFunc(authCheck(serveMap))
	//r.Path("/api/logout").HandlerFunc(authCheck(postLogout))
	//r.Path("/api/points").HandlerFunc(authCheck(GetAllPoints))

	//base.NotFoundHandler = http.HandlerFunc(NotFound)

	var err error
	if config.LocalMode {
		s := fmt.Sprintf(":%d", config.LocalPort)
		err = http.ListenAndServe(s, mux)
	} else {
		err = fcgi.Serve(nil, mux)
	}
	if err != nil {
		fmt.Fprintln(logWriter, err)
	}
}
