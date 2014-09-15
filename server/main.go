package main

import (
	"bufio"
	"compress/gzip"
	"encoding/binary"
	"flag"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// Time allowed to write the file to the client.
	writeWait = 10000 * time.Second

	// Time allowed to read the next pong message from the client.
	pongWait = 6000 * time.Second

	// Send pings to client with this period. Must be less than pongWait.
	pingPeriod = (pongWait * 9) / 10

	// Maximum packet_size
	maxPacketSize = 50000 // NOTICE: this value should be equal to upgrader.WriteBufferSize

	// Async packet header size
	asyncPacketHeaderSize = 4 // 4 bytes

	// Max async payload size
	asyncMaxPayloadSize = maxPacketSize - asyncPacketHeaderSize
)

var (
	addr     = flag.String("addr", ":5050", "http service address")
	upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     func(r *http.Request) bool { return true },
	}
)

// Cache requested files to memory
var Cache = make(map[string][]byte)

// Write async messages to client
func clientWriteAsync(ws *websocket.Conn, payload []byte, chan_id int, seq int) {
	// Check if payload is to big for this packet
	if len(payload) > asyncMaxPayloadSize {
		panic("Received a payload to big to fit on packet")
	}

	// Build packet header
	data := make([]byte, asyncPacketHeaderSize+len(payload))

	binary.BigEndian.PutUint16(data[0:], uint16(chan_id)) // short int, represents the chan_id
	binary.BigEndian.PutUint16(data[2:], uint16(seq))     // short int, represents the chan_id
	copy(data[asyncPacketHeaderSize:], payload)           //copy payload to data

	err := ws.WriteMessage(websocket.BinaryMessage, data)

	if err != nil {
		log.Println("paniking")
		panic(err)
	}
}

// Write sync messages to client
func clientWriteSync(ws *websocket.Conn, payload []byte) {
	log.Println("Sending ", len(payload), "chunk.")
	err := ws.WriteMessage(websocket.BinaryMessage, payload)

	if err != nil {
		panic(err)
	}
}

// Serve a file from the web (acts as proxy)
func serveWebContent(ws *websocket.Conn, url string, async bool, async_chanid int) {
	var hah []byte

	hah, ok := Cache[url]

	if ok {
		log.Println("Fetching from cache", url)
	} else {
		log.Println("Fetching from web", url)

		// Get
		client := new(http.Client)
		req, err := http.NewRequest("GET", url, nil)
		req.Header.Add("Accept-Encoding", "gzip,deflate,sdch")

		resp, err := client.Do(req)

		if err != nil {
			panic(err)
		}
		defer resp.Body.Close()

		// Check that the server actually sent compressed data
		var reader io.ReadCloser
		switch resp.Header.Get("Content-Encoding") {
		case "gzip":
			log.Println("Received gzipped", url)
			reader, err = gzip.NewReader(resp.Body)
			defer reader.Close()
		default:
			log.Println("Received", url)
			reader = resp.Body
		}

		//buffer := make([]byte, asyncMaxPayloadSize)
		hah, _ = ioutil.ReadAll(reader)

		// Cache hah
		Cache[url] = hah
	}

	seq := 0
	offset := 0
	offset_stop := len(hah)

	for {
		until := offset + asyncMaxPayloadSize
		seq++

		//log.Println(async_chanid, seq, offset, until, offset_stop)

		if until > offset_stop {
			clientWriteAsync(ws, hah[offset:offset_stop], async_chanid, seq)
			break
		}

		clientWriteAsync(ws, hah[offset:until], async_chanid, seq)
		offset = until
	}

	clientWriteAsync(ws, []byte(""), async_chanid, seq)

	/*
		for {
			seq = seq + 1
			r := io.LimitReader(reader, asyncMaxPayloadSize)
			n, err := r.Read(buffer)

			log.Println(time.Now().UnixNano() % 1e6 / 1e3)

			if err == io.EOF {
				if async == true {
					clientWriteAsync(ws, []byte(""), async_chanid, seq)
				} else {
					clientWriteSync(ws, []byte(""))
				}
				break
			} else if err != nil {
				panic(err)
			} else {
				if async == true {
					clientWriteAsync(ws, buffer[:n], async_chanid, seq)
				} else {
					clientWriteSync(ws, buffer[:n])
				}
			}
		}
	*/
	return
}

// Serve a file from disk
func serveFile(ws *websocket.Conn, filepath string, async bool, async_chanid int) {
	log.Println("File requested", filepath)

	// Check if requested file exists
	if _, err := os.Stat(filepath); os.IsNotExist(err) {
		log.Printf("Requesed non-existing file: %s", filepath)
		return
	}

	// Open and serve file
	fp, err := os.Open(filepath)

	if err != nil {
		panic(err)
	}

	defer func() {
		if err := fp.Close(); err != nil {
			panic(err)
		}
	}()

	// Start reading and serving file
	reader := bufio.NewReader(fp)
	buffer := make([]byte, upgrader.WriteBufferSize) // Sync it to the writer buffer size

	for {
		// Read chunk
		n, err := reader.Read(buffer)

		if err == io.EOF {
			if async == true {
				clientWriteAsync(ws, []byte(""), async_chanid, 0)
			} else {
				clientWriteSync(ws, []byte(""))
			}
			break
		} else if err != nil {
			panic(err)
		}

		// Write to client
		if async == true {
			clientWriteAsync(ws, buffer[:n], async_chanid, 0)
		} else {
			clientWriteSync(ws, buffer[:n])
		}
	}
}

// Process commands and serve its files through the socket
func processCmd(ws *websocket.Conn, buf []byte) {
	// Defer's & recover from panics
	defer func() {
		if r := recover(); r != nil {
			log.Println("Recovered in processCmd()", r)
		}
	}()

	// Cmd byte buffer is already trimmed for null bytes
	cmd_str := string(buf)

	// Debug
	log.Println("Processing cmd: ", cmd_str)

	// Split cmd method from argument
	cmd := strings.SplitN(cmd_str, " ", 2)

	// Minimum # of args
	if len(cmd) < 2 {
		return
	}

	// Upper first part of cmd
	cmd[0] = strings.ToUpper(cmd[0])

	// Process cmd
	switch cmd[0] {
	case "GET":
		// Split the arguments
		args := strings.SplitN(cmd[1], " ", 2)

		// Check if it wants a sync or async protocol
		// the second argument will then become the message "channel" id the
		// client wants us to use
		var get string
		var async bool
		var async_chanid int
		var err error

		if len(args) == 2 {
			async = true
			get = args[1]
			async_chanid, err = strconv.Atoi(args[0])

			if err != nil || async_chanid > 65535 {
				// Invalid async id passed, not a number or to big
				return
			}
		} else {
			async = false
			get = args[0]
		}

		// Check if we should serve a local file or serve something frm the web
		if len(get) > 0 && get[0:1] == "/" {
			serveFile(ws, strings.TrimPrefix(get, "/"), async, async_chanid)
		} else if len(get) > 4 && get[0:4] == "http" {
			serveWebContent(ws, get, async, async_chanid)
		}
	}

}

func wsConnect(w http.ResponseWriter, r *http.Request) {
	// Accept & upgrade ws connection
	ws, err := upgrader.Upgrade(w, r, nil)

	if err != nil {
		if _, ok := err.(websocket.HandshakeError); !ok {
			log.Println(err)
		}
		return
	}

	// Start receiving commands
	// This is a text based protocol :)
	defer ws.Close()
	ws.SetReadLimit(1024)
	ws.SetReadDeadline(time.Now().Add(pongWait))
	ws.SetPongHandler(func(string) error { ws.SetReadDeadline(time.Now().Add(pongWait)); return nil })
	for {
		messageType, cmd, err := ws.ReadMessage()

		if err != nil {
			break
		}

		if messageType == websocket.TextMessage {
			go processCmd(ws, cmd)
		} else {
			log.Println("Invalid cmd received: ", cmd)
		}
	}
}

func main() {
	// One cpu
	runtime.GOMAXPROCS(1)

	// Parse args
	flag.Parse()

	// Ws Connect
	http.HandleFunc("/", wsConnect)

	// Bind
	if err := http.ListenAndServe(*addr, nil); err != nil {
		log.Fatal(err)
	}
}
