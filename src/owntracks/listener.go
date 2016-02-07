package owntracks

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"strings"
	"time"

	mqtt "git.eclipse.org/gitroot/paho/org.eclipse.paho.mqtt.golang.git"
)

const DefaultTimeout time.Duration = 10 * time.Second
const DefaultClientId = "daisser"
const DefaultTopic = "owntracks/#"
const DefaultPort = 1883

type UpdateEventTrigger int

const (
	PingEvent UpdateEventTrigger = iota
	CircularRegionEvent
	BeaconRegionEvent
	ReportLocationResponse
	ManualLocationUpdate
	TimerBasedUpdate
	AutoLocationUpdate
	UnknownTrigger
)

type locationMessage struct {
	Type     string  `json:"_type"`
	Lat      float64 `json:"lat"`  // WGS-84 latitude in degrees
	Lon      float64 `json:"lon"`  // WGS-85 longitude in degrees
	Epoch    int64   `json:"tst"`  // epoch time
	Accuracy int     `json:"acc"`  // in [m]
	Battery  int     `json:"batt"` // in percent
	Desc     string  `json:"desc"` // Description of a waypoint

	// "p" ping, issued randomly by background task. Note, that the tst in a ping is that of the last location
	// "c" circular region enter/leave event
	// "b" beacon region enter/leave event
	// "r" response to a "reportLocation" request
	// "u" manual publish requested by the user
	// "t" timer based publish in move move
	// "a" or missing t indicates automatic location update
	Trigger   string `json:"t"`
	TrackerID string `json:"tid"`
}

// LocationUpdate is the primary message than can be received from owntracks. It
// is the location of a tracker, that belongs to a certain user.
type LocationUpdate struct {
	T           time.Time
	Trigger     UpdateEventTrigger
	User        string
	ClientID    string
	TrackerID   string
	Accuracy    int
	Battery     int
	Latitude    float64
	Longitude   float64
	Description string
}

// Listener implements a MQTT client that listens for owntracks messages.
type Listener struct {
	Hostname string
	Port     uint16
	Username string
	Password string
	UseTLS   bool
	Timeout  time.Duration
	ClientID string

	messages chan Message
	client   *mqtt.Client
}

// MessageParser is a colletion of receive channels from which updates and messages
// can be retrieved.
type MessageParser struct {
	L <-chan LocationUpdate
	O <-chan Message
}

// RunMessageParsers sets up a parsing goroutine that reads from msgs and
// filters it for owntracks messages. Messages with _type set to
//
//     - "location" are parsed into LocationUpdates and sent over MessageParser.L
//     - everything else is sent as a Message to MessageParser.O
//
// The send operations to the channels will not block, so any potential receiver
// is responsible to triggering the receive operations in time. The method returns a
// new MessageParser with all the channels set up correctly.
func RunMessageParser(msgs <-chan Message, done <-chan struct{}) MessageParser {
	clu := make(chan LocationUpdate)
	co := make(chan Message)
	go func() {
		defer close(clu)
		defer close(co)
		for {
			select {
			case <-done:
				return
			case msg := <-msgs:
				m := make(map[string]interface{})
				if err := json.Unmarshal(msg.Payload, &m); err != nil {
					continue
				}
				switch m["_type"] {
				case "location":
					lu := msg.ParseLocationUpdate()
					if !lu.T.IsZero() {
						clu <- lu
					}
				default:
					co <- msg
				}
			}
		}
	}()
	return MessageParser{L: clu, O: co}
}

// BrokerAddress returns the contact point of the MQTT client in l as a string.
func (l Listener) BrokerAddress() string {
	s := fmt.Sprintf("://%s:%d", l.Hostname, l.Port)
	if l.UseTLS {
		return "ssl" + s
	}
	return "tcp" + s
}

// Connect establishes the connection to the MQTT Broker and subscribes to the
// owntracks topics. It returns a channel over which any received messages are
// sent or the first error that was encountered.
func (l *Listener) Connect() (<-chan Message, error) {
	if l.client != nil && l.client.IsConnected() {
		return nil, errors.New("Listener.Connect: already connected")
	}
	if l.Timeout == 0 {
		l.Timeout = DefaultTimeout
	}
	if l.Port == 0 {
		l.Port = DefaultPort
	}
	if l.ClientID == "" {
		l.ClientID = DefaultClientId
	}

	l.messages = make(chan Message)

	// create a ClientOptions struct setting the broker address, clientid, turn
	// off trace output and set the default message handler
	opts := mqtt.NewClientOptions()
	broker := fmt.Sprintf("%s:%d", l.Hostname, l.Port)
	if l.UseTLS {
		roots := x509.NewCertPool()
		cacrt, _ := ioutil.ReadFile("/home/fabian/owntracks-ca.crt")
		if ok := roots.AppendCertsFromPEM(cacrt); !ok {
			return nil, errors.New("Listener.Connect: Could not read CA certificates")
		}
		opts.SetTLSConfig(&tls.Config{RootCAs: roots})
		opts.AddBroker("ssl://" + broker)
	} else {
		opts.AddBroker("tcp://" + broker)
	}
	opts.SetUsername("fabian")
	opts.SetPassword("fabian")
	opts.SetClientID(l.ClientID)
	opts.SetDefaultPublishHandler(l.handleMessage)

	l.client = mqtt.NewClient(opts)
	t := l.client.Connect()
	if !t.WaitTimeout(l.Timeout) {
		return nil, errors.New("Listener.Connect: timeout")
	}
	if t.Error() != nil {
		return nil, fmt.Errorf("Listener.Connect: %v", t.Error())
	}

	//subscribe to the owntrack topics and request messages to be delivered
	//at a maximum qos of one, wait for the receipt to confirm the subscription
	t = l.client.Subscribe(DefaultTopic, 1, nil)
	if !t.WaitTimeout(l.Timeout) {
		return nil, errors.New("Listener.Connect: timeout during subscription")
	}
	if t.Error() != nil {
		return nil, fmt.Errorf("Listener.Connect: %v", t.Error())
	}
	return l.messages, nil
}

// HandleMessage sends msq over l.messages
func (l *Listener) handleMessage(client *mqtt.Client, msg mqtt.Message) {
	l.messages <- Message{Topic: msg.Topic(), Payload: msg.Payload()}
}

// Disconnect closes the connection to the MQTT broker that was serving the owntracks info.
func (l *Listener) Disconnect() error {
	var err error
	t := l.client.Unsubscribe(DefaultTopic)
	if !t.WaitTimeout(l.Timeout) {
		err = errors.New("Listener.Disconnect: timeout")
	} else {
		err = t.Error()
	}
	l.client.Disconnect(250)
	close(l.messages)
	return err
}

// Message is the default type for an Owntracks MQTT message
type Message struct {
	Topic   string
	Payload []byte
}

// ParseLocationUpdate tries to interpret m as a location update.
func (m Message) ParseLocationUpdate() LocationUpdate {
	var lm locationMessage
	if err := json.Unmarshal(m.Payload, &lm); err != nil || lm.Type != "location" {
		return LocationUpdate{}
	}
	const p = "owntracks/"
	if !strings.HasPrefix(m.Topic, p) {
		return LocationUpdate{}
	}
	uc := strings.Split(m.Topic[len(p):], "/")
	if len(uc) != 2 {
		return LocationUpdate{}
	}
	return LocationUpdate{
		T:           time.Unix(lm.Epoch, 0),
		Trigger:     UnknownTrigger,
		User:        uc[0],
		ClientID:    uc[1],
		TrackerID:   lm.TrackerID,
		Accuracy:    lm.Accuracy,
		Battery:     lm.Battery,
		Latitude:    lm.Lat,
		Longitude:   lm.Lon,
		Description: lm.Desc,
	}
}
