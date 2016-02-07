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
	"owntracks"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const configFile = "config.json"

var config struct {
	MQTTHost     string
	MQTTPort     uint16
	MQTTUser     string
	MQTTPassword string
	UrlBase      string
	DbFile       string
	Listen       string
}

var cachedTemplates = map[string]*template.Template{}
var cachedMutex sync.Mutex

var logger *log.Logger

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
	config.Listen = "fastcgi"
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
	listenFlag := flag.String("listen", "", "Where to listen, either 'fastcgi' or a http.Listen string (':8080')")
	logFlag := flag.String("log", "daisser.log", "file to log to, or '-' for stderr")
	flag.Parse()
	var w io.Writer = os.Stderr
	if *logFlag != "-" {
		var err error
		w, err = os.OpenFile("daisser.log", os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
		if err != nil {
			panic(err)
		}
	}
	logger = log.New(w, "", log.Ldate|log.Ltime|log.Lshortfile)

	err := readConfig()
	if err != nil {
		logger.Println(err)
		panic(err)
	}

	if *listenFlag != "" {
		config.Listen = *listenFlag
	}
}

// T returns a html/template. All compiled templates are cached.
// The template must compile, otherwise this method will panic.
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

func (s *Server) Positions(w http.ResponseWriter, r *http.Request) {
	var fc FeatureCollection
	fc.Type = "FeatureCollection"
	s.posMutex.RLock()
	for _, v := range s.positions {
		var f Feature
		f.Type = "Feature"
		f.Properties = make(map[string]string)
		f.Properties["Time"] = v.T.String()
		f.Properties["User"] = v.User
		f.Properties["Client"] = v.ClientID
		f.Properties["Tracker"] = v.TrackerID
		f.Properties["Accuracy"] = strconv.Itoa(v.Accuracy)
		f.Properties["Description"] = v.Description
		f.Geometry.Type = "Point"
		f.Geometry.Coordinates = make([]float64, 2)
		f.Geometry.Coordinates[0] = v.Longitude
		f.Geometry.Coordinates[1] = v.Latitude
		fc.Features = append(fc.Features, f)
	}
	s.posMutex.RUnlock()
	fmt.Println(fc)
	b, err := json.Marshal(fc)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	w.Write(b)
}

// runTemplate executes the template named name on w.
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

func logout(w http.ResponseWriter, r *http.Request) {
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

type PositionSet map[string]owntracks.LocationUpdate

// Server is the primary datastructure for daisser. Internally it combines a
// HTTP server or FastCGI process with an Owntracks listener
type Server struct {
	mux       *http.ServeMux
	done      chan struct{}
	startTime time.Time
	listener  owntracks.Listener
	positions PositionSet
	posMutex  sync.RWMutex
}

func (s *Server) NotFound(w http.ResponseWriter, r *http.Request) {
	logger.Printf("404 Not found: %s %s", r.Method, r.URL.Path)
	w.WriteHeader(http.StatusNotFound)
	fmt.Fprintf(w, "404 Not Found (%s %s)\n", r.Method, r.URL.Path)
	fmt.Fprintf(w, "Started at %s \t Running for %s", s.startTime.String(), time.Since(s.startTime))
	pwd, _ := os.Getwd()
	fmt.Fprintln(w, "cwd: ", pwd)
}

func (s *Server) DefaultHandle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		serveMap(w, r)
	} else {
		s.NotFound(w, r)
	}
	if _, err := os.Stat("killfile"); !os.IsNotExist(err) {
		logger.Println("found fillfile, quitting now")
		os.Remove("killfile")
		close(s.done)
	}
}

func (s *Server) Listen() error {
	s.listener = owntracks.Listener{
		Hostname: config.MQTTHost,
		Port:     config.MQTTPort,
		Username: config.MQTTUser,
		Password: config.MQTTPassword,
		UseTLS:   true,
		ClientID: "daisser-server",
	}
	msgs, err := s.listener.Connect()
	if err != nil {
		return err
	}
	parser := owntracks.RunMessageParser(msgs, s.done)
	logger.Printf("Connected to MQTT server at %s", s.listener.BrokerAddress())
	go func() {
		for {
			select {
			case <-s.done:
			case l := <-parser.L:
				fmt.Println(l)
				s.addPositionUpdate(l)
			case m := <-parser.O:
				logger.Printf("Received other message: %s %s", m.Topic, m.Payload)
			}
		}
		if err := s.listener.Disconnect(); err != nil {
			logger.Printf("Error during owntracks.Listener.Disconnect: %v", err)
		}
	}()
	return nil
}

func (s *Server) addPositionUpdate(lu owntracks.LocationUpdate) {
	k := lu.User + lu.TrackerID
	s.posMutex.Lock()
	s.positions[k] = lu
	defer s.posMutex.Unlock()
}

func RunServer(listen string) error {
	s := &Server{
		mux:       http.NewServeMux(),
		done:      make(chan struct{}),
		startTime: time.Now(),
		positions: make(PositionSet),
	}
	// default access
	s.mux.HandleFunc("/", s.DefaultHandle)
	s.mux.HandleFunc("/api/positions", s.Positions)
	s.mux.HandleFunc("/logout", logout)
	s.mux.Handle("/assets/", http.FileServer(http.Dir("static")))

	//r.Path("/").HandlerFunc(serveLogin)
	//r.Path("/api/login").Methods("POST").HandlerFunc(postLogin)
	//r.PathPrefix("/static/default/").Handler(http.StripPrefix(config.UrlBase+"/static/default/", http.FileServer(http.Dir("static/default"))))

	//base.NotFoundHandler = http.HandlerFunc(NotFound)

	if err := s.Listen(); err != nil {
		return err
	}

	errc := make(chan error)
	go func() error {
		if listen != "fastcgi" {
			return http.ListenAndServe(listen, s.mux)
		} else {
			return fcgi.Serve(nil, s.mux)
		}
	}()
	select {
	case <-s.done:
		return nil
	case err := <-errc:
		return err
	}
}

func main() {
	logger.Println("Started")
	defer logger.Println("Exited")
	if err := RunServer(config.Listen); err != nil {
		logger.Println(err)
	}
}
