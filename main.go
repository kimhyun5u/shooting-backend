package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"sync"

	guuid "github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
)

var addr = flag.String("addr", ":3000", "http service address")

var rooms = make(map[string]*Room)
var roomReadyState = make(map[string]map[string]bool)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

type RoomState int
type ShootState int

const (
	Waiting RoomState = iota
	Playing
)
const (
	None ShootState = iota
	Rock
	Paper
	Scissors
)

type Client struct {
	id         string
	conn       *websocket.Conn
	shootState ShootState
}

type Room struct {
	id      string
	clients []*Client
	state   RoomState
	lock    *sync.RWMutex
}

func main() {
	flag.Parse()
	r := mux.NewRouter()

	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		var roomID = ""

		if err != nil {
			// log.Println(err)
			return
		}
		defer conn.Close()

		client := Client{
			id:         guuid.New().String(),
			conn:       conn,
			shootState: None,
		}

		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				log.Println(err)
				return
			}

			var data map[string]interface{}
			err = json.Unmarshal(message, &data)

			if err != nil {
				log.Println(err)
				return
			}
			if data["join"] != nil {
				roomID = data["join"].(string)
				if rooms[roomID] != nil {
					for _, c := range rooms[roomID].clients {
						if c.conn == client.conn {
							log.Println("Already joined rooms", roomID)
							return
						}
					}
				} else {
					rooms[roomID] = &Room{
						id:      roomID,
						clients: make([]*Client, 0),
						state:   Waiting,
						lock:    &sync.RWMutex{},
					}
				}
				rooms[roomID].clients = append(rooms[roomID].clients, &client)
				if roomReadyState[roomID] == nil {
					roomReadyState[roomID] = make(map[string]bool)
				}
				roomReadyState[roomID][client.id] = false
				log.Print("Joining rooms", roomID)

				res, err := json.Marshal(map[string]interface{}{"joined": client.id})
				if err != nil {
					log.Println(err)
					return
				}

				client.conn.WriteMessage(websocket.TextMessage, res)
			}

			if data["offer"] != nil {
				if roomID == "" {
					log.Println("No rooms joined")
					return
				}
				for _, c := range rooms[roomID].clients {
					if c.conn != client.conn {
						c.conn.WriteMessage(websocket.TextMessage, message)
					}
				}
			}

			if data["answer"] != nil {
				if roomID == "" {
					log.Println("No rooms joined")
					return
				}
				for _, c := range rooms[roomID].clients {
					if c.conn != client.conn {
						c.conn.WriteMessage(websocket.TextMessage, message)
					}
				}
			}

			if data["ice"] != nil {
				if roomID == "" {
					log.Println("No rooms joined")
					return
				}
				for _, c := range rooms[roomID].clients {
					if c.conn != client.conn {
						c.conn.WriteMessage(websocket.TextMessage, message)
					}
				}
			}
			if data["leave"] != nil {
				if roomID == "" {
					log.Println("No rooms joined")
					return
				}
				for i, c := range rooms[roomID].clients {
					if c.conn == client.conn {
						rooms[roomID].clients = append(rooms[roomID].clients[:i], rooms[roomID].clients[i+1:]...)
						log.Println("Leaving rooms", roomID)
						break
					}
				}
			}

			if data["fight"] != nil {
				if roomID == "" {
					log.Println("No rooms joined")
					return
				}
				roomReadyState[roomID][client.id] = true

				// check all client ready in the rooms
				ready := true
				for id, readyState := range roomReadyState[roomID] {
					if id != client.id && !readyState {
						ready = false
						break
					}
				}
				if ready {
					var res []byte
					res, err := json.Marshal(map[string]interface{}{"fight": "start"})
					if err != nil {
						log.Println(err)
						return
					}
					rooms[roomID].state = Playing
					for _, c := range rooms[roomID].clients {
						c.conn.WriteMessage(websocket.TextMessage, res)
					}
				} else {
					var res []byte
					res, err := json.Marshal(map[string]interface{}{"fight": "waiting"})
					if err != nil {
						log.Println(err)
						return
					}
					for _, c := range rooms[roomID].clients {
						if c.conn != client.conn {
							c.conn.WriteMessage(websocket.TextMessage, res)
						}
					}
				}
			}

			if data["shoot"] != nil {
				if rooms[roomID] == nil {
					log.Println("No rooms joined")
					return
				}

				if rooms[roomID].state != Playing {
					log.Println("Not playing")
					return
				}

				rooms[roomID].lock.Lock()
				// set shoot state for client
				for _, c := range rooms[roomID].clients {
					if c.conn == client.conn {
						c.shootState = ShootState(data["shoot"].(float64))
					}
				}
				rooms[roomID].lock.Unlock()

				// check all client shoot in the rooms
				allShoot := true
				for _, c := range rooms[roomID].clients {
					if c.shootState == None {
						allShoot = false
						break
					}
				}

				if allShoot {
					// check winner
					var winner *Client
					for _, c := range rooms[roomID].clients {
						if c.id == client.id {
							continue
						}
						if (c.shootState == Rock && client.shootState == Scissors) ||
							(c.shootState == Paper && client.shootState == Rock) ||
							(c.shootState == Scissors && client.shootState == Paper) {
							winner = c

						}
					}
					var res []byte
					if winner != nil {
						res, err = json.Marshal(map[string]interface{}{"result": "win", "winner": winner.id})
					} else {
						res, err = json.Marshal(map[string]interface{}{"result": "draw"})
					}
					if err != nil {
						log.Println(err)
						return
					}
					for _, c := range rooms[roomID].clients {
						c.conn.WriteMessage(websocket.TextMessage, res)
					}
					rooms[roomID].lock.Lock()
					for _, c := range rooms[roomID].clients {
						c.shootState = None
					}
					rooms[roomID].lock.Unlock()
				}
			}
		}

	})

	// r.Use(mux.CORSMethodMiddleware(r))

	log.Default().Println("Server started at", *addr)
	if err := http.ListenAndServe(*addr, r); err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}
