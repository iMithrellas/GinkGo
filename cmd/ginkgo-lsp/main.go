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

// main starts the ginkgo LSP server: it initializes logging to /tmp/ginkgo-lsp.log,
// then reads Content-Length framed JSON-RPC messages from stdin and dispatches them
// to handleMessage until EOF or a read error causes shutdown.
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

// readMessage reads a single length-prefixed LSP/JSON-RPC message from reader.
// It parses HTTP-style headers until a blank line, extracts the Content-Length
// header, validates it, and then reads exactly that many bytes. Returns an
// error if headers cannot be read, Content-Length is missing or invalid, or
// the message body cannot be fully read.
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

// handleMessage processes a raw JSON-RPC request message, dispatching supported methods and sending the corresponding responses.
//
// It logs the received message, unmarshals it into a request, and handles the following methods:
//  - "initialize": replies with server capabilities (including a completion provider).
//  - "textDocument/completion": replies with a completion list obtained from fetchTagCompletions.
//  - "shutdown": replies with a nil result.
//  - "exit": terminates the process.
// If unmarshalling fails or the method is unrecognized, the function returns without sending a response.
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

// sendResponse writes the provided JSON-RPC response to standard output framed with a Content-Length header and logs the sent payload; if marshaling fails it logs the error and returns without writing.
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

// fetchTagCompletions parses completion parameters from raw, determines whether tag completions should be provided, requests the list of tags for the document's namespace from the IPC daemon, and returns those tags as completionItems.
// If parameter parsing fails, completion is not applicable, socket resolution or the daemon request fails, or the daemon returns an error, it returns nil.
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

// shouldCompleteTags reports whether tag completions should be offered at the
// given position in the document. It returns true when the line at the
// specified position is non-empty and begins with "Tags: ".
func shouldCompleteTags(params completionParams) bool {
	line := currentLine(params.TextDocument.URI, params.Position.Line)
	if line == "" {
		return false
	}

	return strings.HasPrefix(line, "Tags: ")
}

// currentLine returns the content of the line at the given zero-based `line` index
// from the file identified by the `file://` `uri`.
// If the file cannot be read or the index is out of range, it returns an empty string.
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

// namespaceFromURI extracts the namespace from a file URI for a `.ginkgo.md` document.
// It removes the "file://" prefix, strips the ".ginkgo.md" suffix from the basename,
// splits the basename on ".", and URL-unescapes the first segment. Returns the decoded
// namespace, or an empty string if the filename does not contain at least two segments
// separated by '.' or if URL unescaping fails (the error is logged).
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