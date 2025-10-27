package transport

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"

	"google.golang.org/protobuf/proto"
)

// writeProto writes a length-prefixed protobuf message to w.
func writeProto(w io.Writer, m proto.Message) error {
	b, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	// Write varint length then payload
	var lenbuf [10]byte
	n := binary.PutUvarint(lenbuf[:], uint64(len(b)))
	if _, err := w.Write(lenbuf[:n]); err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// readProto reads a single length-prefixed protobuf message into dst.
func readProto(r io.Reader, dst proto.Message) error {
	br := bufio.NewReader(r)
	ln, err := binary.ReadUvarint(br)
	if err != nil {
		return err
	}
	if ln > 16<<20 { // 16MB safety
		return fmt.Errorf("message too large: %d", ln)
	}
	buf := make([]byte, ln)
	if _, err := io.ReadFull(br, buf); err != nil {
		return err
	}
	return proto.Unmarshal(buf, dst)
}
