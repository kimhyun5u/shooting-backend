package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
)

var addr = flag.String("addr", ":3000", "http service address")
var room = map[string][]*websocket.Conn{}
var roomID = ""
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

func main() {
	flag.Parse()
	r := mux.NewRouter()

	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)

		if err != nil {
			// log.Println(err)
			return
		}
		defer conn.Close()

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
				for _, c := range room[roomID] {
					if c == conn {
						log.Println("Already joined room", roomID)
						return
					}
				}
				room[roomID] = append(room[roomID], conn)

				log.Print("Joining room", roomID)

				res, err := json.Marshal(map[string]interface{}{"joined": roomID})
				if err != nil {
					log.Println(err)
					return
				}

				conn.WriteMessage(websocket.TextMessage, res)
			}

			if data["offer"] != nil {
				if roomID == "" {
					log.Println("No room joined")
					return
				}
				for _, c := range room[roomID] {
					if c != conn {
						c.WriteMessage(websocket.TextMessage, message)
					}
				}
			}

			if data["answer"] != nil {
				if roomID == "" {
					log.Println("No room joined")
					return
				}
				for _, c := range room[roomID] {
					if c != conn {
						c.WriteMessage(websocket.TextMessage, message)
					}
				}
			}

			if data["ice"] != nil {
				if roomID == "" {
					log.Println("No room joined")
					return
				}
				for _, c := range room[roomID] {
					if c != conn {
						c.WriteMessage(websocket.TextMessage, message)
					}
				}
			}
			if data["leave"] != nil {
				if roomID == "" {
					log.Println("No room joined")
					return
				}
				for i, c := range room[roomID] {
					if c == conn {
						room[roomID] = append(room[roomID][:i], room[roomID][i+1:]...)
						log.Println("Leaving room", roomID)
						break
					}
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
