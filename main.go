package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
)

var addr = flag.String("addr", ":3000", "HTTP service address")

var (
	rooms          = make(map[string]*Room)
	roomReadyState = make(map[string]map[string]bool)
	upgrader       = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     func(r *http.Request) bool { return true },
	}
)

const (
	writeWait = 10 * time.Second
)

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
	roomID     string
}

func (c *Client) readPump() {
	defer c.conn.Close()
	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			log.Println("Read error:", err)
			c.leaveRoom()
			return
		}
		c.handleMessage(message)
	}
}

func (c *Client) writeMessage(messageType int, data []byte) error {
	c.conn.SetWriteDeadline(time.Now().Add(writeWait))
	return c.conn.WriteMessage(messageType, data)
}

func (c *Client) handleMessage(message []byte) {
	var data map[string]interface{}
	if err := json.Unmarshal(message, &data); err != nil {
		log.Println("Unmarshal error:", err)
		return
	}

	switch {
	case data["join"] != nil:
		c.handleJoin(data["join"].(string))
	case data["offer"] != nil:
		c.handleOffer(data)
	case data["answer"] != nil:
		c.handleAnswer(data)
	case data["ice"] != nil:
		c.handleIce(data)
	case data["leave"] != nil:
		c.handleLeave()
	case data["fight"] != nil:
		c.handleFight()
	case data["shoot"] != nil:
		c.handleShoot(data)
	}
}

func (c *Client) handleJoin(roomID string) {
	c.roomID = roomID
	room := getOrCreateRoom(roomID)

	if room.hasClient(c) {
		log.Println("Client already in room:", roomID)
		return
	}
	room.addClient(c)
	log.Printf("Client %s joined room %s", c.id, roomID)

	// Notify existing clients about the new client
	res, _ := json.Marshal(map[string]interface{}{"new": c.id})
	room.broadcastExcept(res, c)

	// Send joined confirmation to the client
	res, _ = json.Marshal(map[string]interface{}{"joined": c.id})
	c.writeMessage(websocket.TextMessage, res)
}

func (c *Client) handleOffer(data map[string]interface{}) {
	if c.roomID == "" {
		log.Println("No room joined")
		return
	}
	room := rooms[c.roomID]
	offer, _ := json.Marshal(data)
	toClientID := data["to"].(string)
	room.sendToClient(toClientID, offer)
}

func (c *Client) handleAnswer(data map[string]interface{}) {
	if c.roomID == "" {
		log.Println("No room joined")
		return
	}
	room := rooms[c.roomID]
	answer, _ := json.Marshal(data)
	toClientID := data["to"].(string)
	room.sendToClient(toClientID, answer)
}

func (c *Client) handleIce(data map[string]interface{}) {
	if c.roomID == "" {
		log.Println("No room joined")
		return
	}
	room := rooms[c.roomID]
	ice, _ := json.Marshal(data)
	room.broadcastExcept(ice, c)
}

func (c *Client) handleLeave() {
	c.leaveRoom()
}

func (c *Client) handleFight() {
	if c.roomID == "" {
		log.Println("No room joined")
		return
	}

	room := rooms[c.roomID]

	if room.activePlayers != nil && room.activePlayers[c.id] == nil {
		log.Println("Client not an active player:", c.id)
		return
	}
	roomReadyState[c.roomID][c.id] = true

	if room.allReady() {
		res, _ := json.Marshal(map[string]interface{}{"fight": "start"})
		room.state = Playing
		room.initActivePlayers()
		room.broadcast(res)
	} else {
		res, _ := json.Marshal(map[string]interface{}{"fight": "waiting"})
		room.broadcastExcept(res, c)
	}
}

func (c *Client) handleShoot(data map[string]interface{}) {
	if c.roomID == "" {
		log.Println("No room joined")
		return
	}
	room := rooms[c.roomID]
	if room == nil || room.state != Playing {
		log.Println("Room not in playing state:", c.roomID)
		return
	}

	if room.activePlayers[c.id] == nil {
		log.Println("Client not an active player:", c.id)
		return
	}

	shootValue := ShootState(int(data["shoot"].(float64)))
	room.setClientShootState(c.id, shootValue)

	if room.allActivePlayersShot() {
		winners, losers := room.determineWinnersAndLosers()
		fmt.Println("Who survived:", room.activePlayers)
		room.updateActivePlayers(winners)
		fmt.Println("Winners:", winners)
		fmt.Println("Losers:", losers)

		if len(winners) == len(room.activePlayers) && len(losers) == 0 {
			// All players drew, no one is eliminated
			res, _ := json.Marshal(map[string]interface{}{"result": "draw"})
			room.resetForNextRound()
			room.broadcast(res)
		} else if len(room.activePlayers) == 1 {
			// Final winner
			finalWinner := room.getFinalWinner()
			res, _ := json.Marshal(map[string]interface{}{"result": "final_win", "winner": finalWinner.id})
			room.broadcast(res)
			room.resetForNextGame()
		} else {
			// Some players are eliminated, proceed to next round
			// Inform each client about their status
			for _, client := range room.clients {
				var res []byte
				if _, isWinner := room.activePlayers[client.id]; isWinner {
					res, _ = json.Marshal(map[string]interface{}{"result": "win"})
				} else if containsClient(losers, client) {
					res, _ = json.Marshal(map[string]interface{}{"result": "lose"})
				}
				client.writeMessage(websocket.TextMessage, res)
			}
			room.resetForNextRound()
		}
	}
}

func (c *Client) leaveRoom() {
	if c.roomID == "" {
		return
	}
	room := rooms[c.roomID]
	if room == nil {
		return
	}
	room.removeClient(c)
	log.Printf("Client %s left room %s", c.id, c.roomID)
	c.roomID = ""
}

type Room struct {
	id            string
	clients       map[string]*Client
	state         RoomState
	lock          sync.RWMutex
	activePlayers map[string]*Client
}

func getOrCreateRoom(roomID string) *Room {
	room, exists := rooms[roomID]
	if !exists {
		room = &Room{
			id:      roomID,
			clients: make(map[string]*Client),
			state:   Waiting,
		}
		rooms[roomID] = room
		roomReadyState[roomID] = make(map[string]bool)
	}
	return room
}

func (r *Room) addClient(c *Client) {
	r.lock.Lock()
	defer r.lock.Unlock()
	r.clients[c.id] = c
	roomReadyState[r.id][c.id] = false
}

func (r *Room) removeClient(c *Client) {
	r.lock.Lock()
	defer r.lock.Unlock()
	delete(r.clients, c.id)
	delete(roomReadyState[r.id], c.id)
	if len(r.clients) == 0 {
		delete(rooms, r.id)
		delete(roomReadyState, r.id)
	}
}

func (r *Room) hasClient(c *Client) bool {
	r.lock.RLock()
	defer r.lock.RUnlock()
	_, exists := r.clients[c.id]
	return exists
}

func (r *Room) broadcast(message []byte) {
	r.lock.RLock()
	defer r.lock.RUnlock()
	for _, client := range r.clients {
		client.writeMessage(websocket.TextMessage, message)
	}
}

func (r *Room) broadcastExcept(message []byte, exclude *Client) {
	r.lock.RLock()
	defer r.lock.RUnlock()
	for _, client := range r.clients {
		if client.id != exclude.id {
			client.writeMessage(websocket.TextMessage, message)
		}
	}
}

func (r *Room) sendToClient(clientID string, message []byte) {
	r.lock.RLock()
	defer r.lock.RUnlock()
	if client, exists := r.clients[clientID]; exists {
		client.writeMessage(websocket.TextMessage, message)
	}
}

func (r *Room) allReady() bool {
	if r.activePlayers != nil {
		for clientID := range r.activePlayers {
			if !roomReadyState[r.id][clientID] {
				return false
			}
		}
		return true
	} else {
		for clientID := range r.clients {
			if !roomReadyState[r.id][clientID] {
				return false
			}
		}
		return true
	}
}

func (r *Room) initActivePlayers() {
	r.lock.Lock()
	defer r.lock.Unlock()
	if r.activePlayers == nil {
		r.activePlayers = make(map[string]*Client)
		for id, client := range r.clients {
			r.activePlayers[id] = client
		}
	}
}

func (r *Room) setClientShootState(clientID string, shootState ShootState) {
	r.lock.Lock()
	defer r.lock.Unlock()
	if client, exists := r.activePlayers[clientID]; exists {
		client.shootState = shootState
	}
}

func (r *Room) allActivePlayersShot() bool {
	r.lock.RLock()
	defer r.lock.RUnlock()
	for _, client := range r.activePlayers {
		if client.shootState == None {
			return false
		}
	}
	return true
}

func (r *Room) determineWinnersAndLosers() (winners []*Client, losers []*Client) {
	r.lock.RLock()
	defer r.lock.RUnlock()

	choices := make(map[ShootState][]*Client)
	for _, client := range r.activePlayers {
		choices[client.shootState] = append(choices[client.shootState], client)
	}

	// If all made the same choice or all three choices are present, it's a draw
	if len(choices) == 1 || len(choices) == 3 {
		// All players proceed to next round
		winners = make([]*Client, 0, len(r.activePlayers))
		for _, client := range r.activePlayers {
			winners = append(winners, client)
		}
		return winners, nil
	}

	// If two choices are present, determine which choice beats the other
	if len(choices) == 2 {
		var winningChoice, losingChoice ShootState

		_, okRock := choices[Rock]
		_, okPaper := choices[Paper]
		_, okScissors := choices[Scissors]

		if okRock && okPaper {
			winningChoice, losingChoice = Paper, Rock
		} else if okPaper && okScissors {
			winningChoice, losingChoice = Scissors, Paper
		} else if okScissors && okRock {
			winningChoice, losingChoice = Rock, Scissors
		}

		winners = choices[winningChoice]
		losers = choices[losingChoice]
		return winners, losers
	}

	// Should not reach here
	return nil, nil
}

func (r *Room) updateActivePlayers(winners []*Client) {
	r.lock.Lock()
	defer r.lock.Unlock()

	newActivePlayers := make(map[string]*Client)
	for _, winner := range winners {
		newActivePlayers[winner.id] = winner
	}
	r.activePlayers = newActivePlayers
}

func (r *Room) resetForNextRound() {
	r.lock.Lock()
	defer r.lock.Unlock()

	for _, client := range r.activePlayers {
		roomReadyState[r.id][client.id] = false
		client.shootState = None
	}
}

func (r *Room) getFinalWinner() *Client {
	r.lock.RLock()
	defer r.lock.RUnlock()
	for _, client := range r.activePlayers {
		return client // There should be only one
	}
	return nil
}

func (r *Room) resetForNextGame() {
	r.lock.Lock()
	defer r.lock.Unlock()
	r.state = Waiting
	r.activePlayers = nil
	for _, client := range r.clients {
		client.shootState = None
		roomReadyState[r.id][client.id] = false
	}
}

func containsClient(clients []*Client, client *Client) bool {
	for _, c := range clients {
		if c.id == client.id {
			return true
		}
	}
	return false
}

func main() {
	flag.Parse()
	r := mux.NewRouter()

	r.HandleFunc("/", serveWs)

	log.Printf("Server started at %s", *addr)
	if err := http.ListenAndServe(*addr, r); err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}

func serveWs(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Upgrade error:", err)
		return
	}

	client := &Client{
		id:         uuid.New().String(),
		conn:       conn,
		shootState: None,
		roomID:     "",
	}

	go client.readPump()
}
