// controller_service.go
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Event represents an event to be sent to the provider
type Event struct {
	ID      string `json:"id"`
	Payload string `json:"payload"`
}

// RateLimiter controls the event sending rate
type RateLimiter struct {
	mu         sync.Mutex
	tokens     int
	maxAllowed int
	ticker     *time.Ticker
}

func NewRateLimiter(max int, interval time.Duration) *RateLimiter {
	rl := &RateLimiter{
		tokens:     max,
		maxAllowed: max,
		ticker:     time.NewTicker(interval),
	}
	go rl.refill()
	return rl
}

func (rl *RateLimiter) refill() {
	for range rl.ticker.C {
		rl.mu.Lock()
		rl.tokens = rl.maxAllowed
		rl.mu.Unlock()
	}
}

func (rl *RateLimiter) Allow() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if rl.tokens > 0 {
		rl.tokens--
		return true
	}
	return false
}

// Controller holds state for managing connections and events
type Controller struct {
	appConn       *websocket.Conn
	providerMain  *websocket.Conn
	providerBackup *websocket.Conn
	events        []Event
	ratelimiter   *RateLimiter
	startBackup   chan struct{}
	mu            sync.Mutex
}

func NewController() *Controller {
	return &Controller{
		ratelimiter: NewRateLimiter(100, time.Hour),
		events:      make([]Event, 0),
		startBackup: make(chan struct{}),
	}
}

func (c *Controller) handleAppConnection(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Fatal("Failed to upgrade app connection:", err)
	}
	c.appConn = conn
	log.Println("App connected")
	go c.readEventsFromApp()
}

func (c *Controller) readEventsFromApp() {
	for {
		_, msg, err := c.appConn.ReadMessage()
		if err != nil {
			log.Println("App connection closed:", err)
			return
		}
		var event Event
		err = json.Unmarshal(msg, &event)
		if err != nil {
			log.Println("Invalid event format:", err)
			continue
		}
		c.mu.Lock()
		c.events = append(c.events, event)
		c.mu.Unlock()
	}
}

func (c *Controller) startWorker(ws *websocket.Conn, label string, triggerBackup bool) {
	go func() {
		log.Printf("%s Worker started", label)
		for i := 0; i < len(c.events); i++ {
			c.mu.Lock()
			event := c.events[i]
			c.mu.Unlock()

			for !c.ratelimiter.Allow() {
				log.Printf("%s rate-limited, sleeping...", label)
				time.Sleep(1 * time.Minute)
			}

			eventMsg, _ := json.Marshal(event)
			err := ws.WriteMessage(websocket.TextMessage, eventMsg)
			if err != nil {
				log.Printf("%s worker write error: %v", label, err)
				return
			}
		}

		log.Printf("%s Worker finished sending events", label)

		if triggerBackup {
			log.Println("Triggering backup worker")
			c.startBackup <- struct{}{}
		}

		for {
			_, msg, err := c.appConn.ReadMessage()
			if err != nil {
				log.Printf("%s idle proxy: app disconnected: %v", label, err)
				return
			}
			err = ws.WriteMessage(websocket.TextMessage, msg)
			if err != nil {
				log.Printf("%s idle proxy: provider disconnected: %v", label, err)
				return
			}
		}
	}()
}

func main() {
	controller := NewController()

	http.HandleFunc("/app/ws", controller.handleAppConnection)

	go func() {
		conn1, _, err := websocket.DefaultDialer.Dial("ws://provider/main", nil)
		if err != nil {
			log.Fatal("Main provider connection failed:", err)
		}
		controller.providerMain = conn1
		controller.startWorker(conn1, "Main", true)
	}()

	go func() {
		<-controller.startBackup // wait until main worker finishes
		conn2, _, err := websocket.DefaultDialer.Dial("ws://provider/backup", nil)
		if err != nil {
			log.Fatal("Backup provider connection failed:", err)
		}
		controller.providerBackup = conn2
		controller.startWorker(conn2, "Backup", false)
	}()

	log.Println("Controller listening on :8080")
	http.ListenAndServe(":8080", nil)
}