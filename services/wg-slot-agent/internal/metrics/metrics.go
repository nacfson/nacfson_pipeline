package metrics

import (
	"bufio"
	"bytes"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	BufferAnnotation    = "oci-link.nacfson.io/fluent-bit-buffer-bytes"
	HandshakeAnnotation = "oci-link.nacfson.io/handshake-age-seconds"
	StaleAfter          = 180 * time.Second
)

func BufferBytes(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

type Handshakes struct {
	MaxAge time.Duration
	Stale  bool
	Peers  int
}

func ParseLatestHandshakes(out []byte, now time.Time) Handshakes {
	h := Handshakes{Stale: true}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 {
			continue
		}
		sec, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		h.Peers++
		age := StaleAfter + time.Second
		if sec > 0 {
			age = now.Sub(time.Unix(sec, 0))
		}
		if age > h.MaxAge {
			h.MaxAge = age
		}
		if age <= StaleAfter {
			h.Stale = false
		}
	}
	return h
}

func (h Handshakes) Annotations(bufferBytes int64) map[string]string {
	return map[string]string{
		BufferAnnotation:    strconv.FormatInt(bufferBytes, 10),
		HandshakeAnnotation: strconv.FormatInt(int64(h.MaxAge/time.Second), 10),
	}
}
