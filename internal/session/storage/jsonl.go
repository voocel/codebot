package storage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

type jsonlScanResult struct {
	validBytes      int64
	endsWithNewline bool
}

// scanJSONLines reads JSONL without imposing a maximum line size.
//
// A process crash can leave the final write incomplete, so an invalid,
// unterminated final line is ignored. Any malformed complete line is reported:
// corruption in the middle of durable history must not be silently hidden.
func scanJSONLines(r io.Reader, decode func([]byte) error) (jsonlScanResult, error) {
	reader := bufio.NewReaderSize(r, 64*1024)
	result := jsonlScanResult{endsWithNewline: true}
	for lineNo := 1; ; lineNo++ {
		raw, readErr := reader.ReadBytes('\n')
		terminated := len(raw) > 0 && raw[len(raw)-1] == '\n'
		line := bytes.TrimSpace(raw)

		if len(line) > 0 {
			if !json.Valid(line) {
				if readErr == io.EOF && !terminated {
					return result, nil
				}
				return result, fmt.Errorf("decode JSONL line %d: invalid JSON", lineNo)
			}
			if err := decode(line); err != nil {
				return result, fmt.Errorf("decode JSONL line %d: %w", lineNo, err)
			}
		}
		if len(raw) > 0 {
			result.validBytes += int64(len(raw))
			result.endsWithNewline = terminated
		}

		switch readErr {
		case nil:
			continue
		case io.EOF:
			return result, nil
		default:
			return result, fmt.Errorf("read JSONL line %d: %w", lineNo, readErr)
		}
	}
}

// normalizeJSONLTail makes a successfully scanned file safe for future
// appends. It removes only bytes from an invalid unterminated tail and adds a
// missing line terminator after a valid final record.
func normalizeJSONLTail(f *os.File, scan jsonlScanResult) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() < scan.validBytes {
		return fmt.Errorf("JSONL file shrank during recovery")
	}
	if info.Size() > scan.validBytes {
		if err := f.Truncate(scan.validBytes); err != nil {
			return fmt.Errorf("truncate torn JSONL tail: %w", err)
		}
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	if scan.validBytes == 0 || scan.endsWithNewline {
		return nil
	}
	if _, err := f.Write([]byte{'\n'}); err != nil {
		return fmt.Errorf("terminate final JSONL line: %w", err)
	}
	return nil
}
