package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mithrel/ginkgo/internal/ipc"
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

type completionItem struct {
	Label string `json:"label"`
	Kind  int    `json:"kind,omitempty"`
}

type completionParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Position     position               `json:"position"`
}

type textDocumentIdentifier struct {
	URI string `json:"uri"`
}

type position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type completionList struct {
	IsIncomplete bool             `json:"isIncomplete"`
	Items        []completionItem `json:"items"`
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

	reader := bufio.NewReader(os.Stdin)
	for {
		msg, err := readMessage(reader)
		if err != nil {
			if err == io.EOF {
				return
			}
			logger.Printf("Error reading message: %v", err)
			return
		}
		handleMessage(msg)
	}
}

func readMessage(reader *bufio.Reader) ([]byte, error) {
	contentLength := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Content-Length: ") {
			lengthStr := strings.TrimPrefix(line, "Content-Length: ")
			length, err := strconv.Atoi(lengthStr)
			if err != nil {
				return nil, fmt.Errorf("invalid Content-Length: %w", err)
			}
			contentLength = length
		}
	}

	if contentLength <= 0 {
		return nil, fmt.Errorf("missing Content-Length")
	}

	msg := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, msg); err != nil {
		return nil, err
	}
	return msg, nil
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
				CompletionProvider: completionProvider{},
			},
		}
		sendResponse(response{RPC: "2.0", ID: req.ID, Result: resp})
	case "textDocument/completion":
		resp := completionList{Items: fetchTagCompletions(req.Params)}
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

func fetchTagCompletions(raw json.RawMessage) []completionItem {
	var params completionParams
	if err := json.Unmarshal(raw, &params); err != nil {
		logger.Printf("Error parsing completion params: %v", err)
		return nil
	}

	if !shouldCompleteTags(params) {
		return nil
	}

	sock, err := ipc.SocketPath()
	if err != nil {
		logger.Printf("Error resolving socket path: %v", err)
		return nil
	}

	ctx := context.Background()
	namespace := namespaceFromURI(params.TextDocument.URI)
	resp, err := ipc.Request(ctx, sock, ipc.Message{Name: "tag.list", Namespace: namespace})
	if err != nil {
		logger.Printf("Error fetching tags: %v", err)
		return nil
	}
	if !resp.OK {
		logger.Printf("Daemon error: %s", resp.Msg)
		return nil
	}

	items := make([]completionItem, 0, len(resp.Tags))
	for _, tag := range resp.Tags {
		items = append(items, completionItem{Label: tag.Tag, Kind: 1})
	}

	return items
}

func shouldCompleteTags(params completionParams) bool {
	line := currentLine(params.TextDocument.URI, params.Position.Line)
	if line == "" {
		return false
	}

	return strings.HasPrefix(line, "Tags: ")
}

func currentLine(uri string, line int) string {
	path := strings.TrimPrefix(uri, "file://")
	data, err := os.ReadFile(path)
	if err != nil {
		logger.Printf("Error reading file: %v", err)
		return ""
	}
	lines := strings.Split(string(data), "\n")
	if line < 0 || line >= len(lines) {
		return ""
	}
	return lines[line]
}

func namespaceFromURI(uri string) string {
	path := strings.TrimPrefix(uri, "file://")
	base := strings.TrimSuffix(filepath.Base(path), ".ginkgo.md")
	parts := strings.Split(base, ".")
	if len(parts) < 2 {
		return ""
	}
	namespace, err := url.PathUnescape(parts[0])
	if err != nil {
		logger.Printf("Error decoding namespace: %v", err)
		return ""
	}
	return namespace
}
