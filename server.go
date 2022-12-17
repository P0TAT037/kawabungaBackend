//TODO:
// - security...
// - random codes are not that random
// - what to store in the database....
// - admin controls
// - admin connecting from multiple devices causes problems
//  (throws a panic when one device id disconnected and the other one tries to send a message/disconnect)

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	_ "github.com/microsoft/go-mssqldb"
)

type device struct {
	Name string `json:"name"`
	Id   int    `json:"id"`
	ws   *websocket.Conn
}

var codes = make(map[string]int)
var CodesStack []string
var connections = make(map[int][]device)
var admins = make(map[int]*websocket.Conn)

var db *sql.DB = nil

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

func HandleRoutes(mux *mux.Router) {
	mux.HandleFunc("/", ping)
	mux.HandleFunc("/join", join).Methods("GET")
	mux.HandleFunc("/broadcast", broadcast).Methods("POST")
	mux.HandleFunc("/upload", upload).Methods("POST", "PUT", "PATCH", "DELETE")
	mux.HandleFunc("/signup", signUp).Methods("POST")
	mux.HandleFunc("/signin", signIn).Methods("GET")
	mux.HandleFunc("/invite", invite).Methods("GET")
	mux.HandleFunc("/admin", connectAdmin).Methods("GET")
	mux.HandleFunc("/disconnect", disconnectUser).Methods("GET")
	mux.HandleFunc("/disconnectAdmin", disconnectAdmin).Methods("GET")
	mux.HandleFunc("/send", send).Methods("POST")
}

func genCode(id int) string {
	var code string = ""

	exists := true
	for exists {
		for i := 0; i < 4; i++ {
			code += strconv.Itoa(rand.Intn(9))
		}

		_, exists = codes[code]
	}
	codes[code] = id
	CodesStack = append(CodesStack, code)
	return code
}

func main() {
	var err error
	//sqlserver://server:1234@localhost/mssqlserver01:1433?database=kawabunga
	// connString := `Server=localhost\MSSQLSERVER01,1433;Database=kawabunga;User Id=server;Password=1234;`
	db, _ = sql.Open("sqlserver", "Server=localhost\\ESLAM,1433;Database=kawabunga;User Id=server;Password=1234;")
	err = db.Ping()
	if err != nil {
		panic(err.Error())
	} else {
		fmt.Println("connected to the database")
	}
	defer db.Close()

	go func() {
		for {
			if len(CodesStack) != 0 {
				time.Sleep(time.Minute)
				delete(codes, CodesStack[len(CodesStack)-1])
				CodesStack = CodesStack[:len(CodesStack)-1]
				log.Println("Codes Updated")
			}
		}
	}()

	mux := mux.NewRouter()
	HandleRoutes(mux)

	log.Fatal(http.ListenAndServe(":8080", mux))
}

func ping(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("pong"))
}

func signUp(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	username := r.PostFormValue("username")
	password := r.PostFormValue("password")
	_, err := db.Query("INSERT INTO users (username, password) VALUES (@p1 , @p2)", username, password)
	if err != nil {
		w.Write([]byte("Error"))
		log.Println("SignUp:", err)
		return
	}
	w.Write([]byte("Success"))
	fmt.Println("user added")
}

func invite(w http.ResponseWriter, r *http.Request) {

	id := authnticate(r, w)
	if id == -1 {
		w.Write([]byte("Authentication Error. Try to sign in again"))
		return
	}

	code := genCode(id)

	w.Write([]byte(code))
	log.Println("generated code:", code)
}

func wsReader(ws *websocket.Conn) {
	for {
		t, p, err := ws.ReadMessage()
		if err != nil {
			log.Println("wsReader err:", err)
			return
		}
		log.Println("wsReader:", t, string(p))
		return
	}
}

func join(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("join:", err)
	}

	id, ok := codes[r.URL.Query().Get("code")]
	if !ok {
		ws.WriteMessage(websocket.TextMessage, []byte("wrong code"))
		ws.Close()
		return
	}

	ws.WriteMessage(websocket.TextMessage, []byte("connected"))
	go wsReader(ws)
	connections[id] = append(connections[id], device{Name: r.URL.Query().Get("name"), Id: len(connections[id]), ws: ws})
	ws.SetCloseHandler(func(code int, text string) error {
		log.Println("disconnecting user", id, ":", len(connections[id])-1)
		handleClosedUserConnection(id, len(connections[id])-1)
		return nil
	})
	updateAdmin(id)
	fmt.Println("client joined to user:", id)
}

func updateAdmin(id int) {
	var deviceNames []string
	for i, device := range connections[id] {
		device.Id = i
		deviceNames = append(deviceNames, device.Name)
	}

	connsJson, err := json.Marshal(deviceNames)
	if err != nil {
		log.Println("updateAdmin:", err)
	}

	fmt.Println("json that should be sent:", string(connsJson))
	admins[id].WriteMessage(websocket.TextMessage, connsJson)
}

func broadcast(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()

	var id int = authnticate(r, w)
	if id == -1 {
		w.Write([]byte("Authentication Error. Try to sign in again"))
		return
	}

	data, _ := io.ReadAll(r.Body)
	for _, device := range connections[id] {
		if r.Header.Get("Content-Type") == "txt" {
			device.ws.WriteMessage(websocket.TextMessage, data)
		} else {
			device.ws.WriteMessage(websocket.BinaryMessage, data)
		}
	}
	log.Println("user", id, "broadcasted an image")
}

func send(w http.ResponseWriter, r *http.Request) {
	id := authnticate(r, w)
	if id == -1 {
		log.Println("send: user not found")
	}
	userIndex, _ := strconv.Atoi(r.URL.Query().Get("id"))
	data, _ := io.ReadAll(r.Body)
	connections[id][userIndex].ws.WriteMessage(websocket.BinaryMessage, data)
	log.Println("user", id, "sent an image to user", userIndex)
}

// this is just a paid extra feature to store data on the server
func upload(w http.ResponseWriter, r *http.Request) {

	id := authnticate(r, w)
	if id == -1 {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Println("upload: user not found")
		return
	}
	fmt.Println(id)
	data, _ := io.ReadAll(r.Body)
	os.WriteFile("E:/Projects/GO/data.jpg", data, 7777)
	_, err := db.Query("INSERT INTO data (data, owner) VALUES (@p1,@p2)", data, id)

	if err != nil {
		fmt.Println("upload:", err)
		return
	}
	fmt.Println("data received")
}

func authnticate(r *http.Request, w http.ResponseWriter) int {
	u, p, _ := r.BasicAuth()
	var id int
	err := db.QueryRow("SELECT userID FROM users WHERE username = @p1 AND password = @p2", u, p).Scan(&id)
	if err != nil {
		log.Println("authnticate", err)
		return -1
	} else {
		return id
	}
}

func signIn(w http.ResponseWriter, r *http.Request) {
	id := authnticate(r, w)
	if id == -1 {
		w.Write([]byte("-1"))
	} else {
		code := genCode(id)
		w.Write([]byte(code))
	}
}

func connectAdmin(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("connectAdmin:", err)
	}
	code := r.URL.Query().Get("code")
	id := codes[code]
	admins[id] = ws
}

func disconnectUser(w http.ResponseWriter, r *http.Request) {
	id := authnticate(r, w)
	if id == -1 {
		log.Println("disconnectUser: user not found")
		return
	}

	deviceIndex, _ := strconv.Atoi(r.URL.Query().Get("id"))
	ws := connections[id][deviceIndex].ws
	ws.WriteMessage(websocket.TextMessage, []byte("disconnected"))
	ws.Close()
	copy(connections[id][deviceIndex:], connections[id][deviceIndex+1:])
	connections[id][len(connections[id])-1] = device{}
	connections[id] = connections[id][:len(connections[id])-1]

	fmt.Println("device disconnected")
	fmt.Println("devices connected to user", id, ":", connections[id])

	updateAdmin(id)
}

func handleClosedUserConnection(id, deviceIndex int) {
	copy(connections[id][deviceIndex:], connections[id][deviceIndex+1:])
	connections[id][len(connections[id])-1] = device{}
	connections[id] = connections[id][:len(connections[id])-1]

	for i, device := range connections[id] {
		device.Id = i
		connections[id][i] = device
	}

	fmt.Println("device disconnected")
	fmt.Println("devices connected to user", id, ":", connections[id])

	updateAdmin(id)
}

func disconnectAdmin(w http.ResponseWriter, r *http.Request) {
	id := authnticate(r, w)
	if id == -1 {
		log.Println("disconnectAdmin: user not found")
		return
	}

	for _, device := range connections[id] {
		device.ws.WriteMessage(websocket.TextMessage, []byte("disconnected"))
		device.ws.Close()
	}
	delete(connections, id)
	admins[id].Close()
	delete(admins, id)
	log.Println("admin", id, "disconnected")
	log.Println("connected admins:", admins)
}
