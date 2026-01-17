package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
)

type request struct {
	RPC    string           `json:"jsonrpc"`
	ID     *json.RawMessage `json:"id,omitempty"`
	Method string           `json:"method"`
	Params json.RawMessage  `json:"params,omitempty"`
}

type response struct {
	RPC    string           `json:"jsonrpc"`
	ID     *json.RawMessage `json:"id,omitempty"`
	Result interface{}      `json:"result,omitempty"`
	Error  interface{}      `json:"error,omitempty"`
}

type initializeResult struct {
	Capabilities serverCapabilities `json:"capabilities"`
}

type serverCapabilities struct {
	CompletionProvider completionProvider `json:"completionProvider"`
}

type completionProvider struct {
	TriggerCharacters []string `json:"triggerCharacters,omitempty"`
}

var logger *log.Logger

func main() {
	logFile, err := os.Create("/tmp/ginkgo-lsp.log")
	if err != nil {
		panic(err)
	}
	defer logFile.Close()

	logger = log.New(logFile, "[LSP] ", log.LstdFlags)
	logger.Println("Server started")

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Split(scanLSP)

	for scanner.Scan() {
		msg := scanner.Bytes()
		handleMessage(msg)
	}
}

func scanLSP(data []byte, atEOF bool) (advance int, token []byte, err error) {
	headerSep := []byte("\r\n\r\n")
	index := -1

	for i := 0; i < len(data)-len(headerSep)+1; i++ {
		if string(data[i:i+len(headerSep)]) == string(headerSep) {
			index = i
			break
		}
	}

	if index == -1 {
		if atEOF && len(data) > 0 {
			return 0, nil, fmt.Errorf("incomplete header")
		}
		return 0, nil, nil
	}

	headers := string(data[:index])
	contentLength := 0
	for _, line := range strings.Split(headers, "\r\n") {
		if strings.HasPrefix(line, "Content-Length: ") {
			lengthStr := strings.TrimPrefix(line, "Content-Length: ")
			contentLength, _ = strconv.Atoi(lengthStr)
		}
	}

	totalLength := index + len(headerSep) + contentLength
	if len(data) < totalLength {
		return 0, nil, nil
	}

	return totalLength, data[index+len(headerSep) : totalLength], nil
}

func handleMessage(msg []byte) {
	logger.Printf("Received: %s", string(msg))

	var req request
	if err := json.Unmarshal(msg, &req); err != nil {
		logger.Printf("Error unmarshaling: %v", err)
		return
	}

	switch req.Method {
	case "initialize":
		resp := initializeResult{
			Capabilities: serverCapabilities{
				CompletionProvider: completionProvider{
					TriggerCharacters: []string{"#"},
				},
			},
		}
		sendResponse(response{RPC: "2.0", ID: req.ID, Result: resp})
	case "shutdown":
		sendResponse(response{RPC: "2.0", ID: req.ID, Result: nil})
	case "exit":
		os.Exit(0)
	default:
		return
	}
}

func sendResponse(resp response) {
	bytes, err := json.Marshal(resp)
	if err != nil {
		logger.Printf("Error marshaling response: %v", err)
		return
	}

	msg := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(bytes), string(bytes))
	fmt.Print(msg)
	logger.Printf("Sent: %s", string(bytes))
}
