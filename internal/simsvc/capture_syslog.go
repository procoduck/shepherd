package simsvc

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"
)

// syslogReadTimeout bounds one idle connection. otelcol.exporter.syslog holds
// its connection open between batches, so a per-read deadline is what lets the
// listener notice a dead peer without waiting on TCP keepalive.
const syslogReadTimeout = 2 * time.Minute

// serveSyslog accepts RFC5424 octet-counted and newline-delimited syslog
// frames and records each message as a captured log line. It is a real
// listener rather than a discard socket because a refused connection makes
// otelcol.exporter.syslog report a transport error, which a user would read as
// their pipeline being broken.
func serveSyslog(ctx context.Context, ln net.Listener, h *Harness, logger *slog.Logger) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			logger.Warn("capture: syslog accept failed", "error", err)
			continue
		}
		go handleSyslogConn(conn, h, logger)
	}
}

func handleSyslogConn(conn net.Conn, h *Harness, logger *slog.Logger) {
	defer conn.Close() //nolint:errcheck // best-effort close on a capture socket
	reader := bufio.NewReader(conn)
	for {
		if err := conn.SetReadDeadline(time.Now().Add(syslogReadTimeout)); err != nil {
			return
		}
		msg, err := readSyslogFrame(reader)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				logger.Debug("capture: syslog read ended", "error", err)
			}
			return
		}
		if msg == "" {
			continue
		}
		if s := h.activeSink(); s != nil {
			s.addLogLine(LogLine{
				Labels:      map[string]string{"receiver": "syslog"},
				Line:        msg,
				TimestampMS: time.Now().UnixMilli(),
			})
			s.addOther(func(o *OtherCounts) { o.SyslogMessages++ })
		}
	}
}

// readSyslogFrame reads one message in either RFC6587 framing: octet counting
// ("<len> <msg>") or non-transparent framing (newline-terminated). Alloy's
// exporter defaults to octet counting, but the format is configurable, so the
// receiver has to handle both or half the syslog runs capture nothing.
func readSyslogFrame(r *bufio.Reader) (string, error) {
	first, err := r.ReadByte()
	if err != nil {
		return "", err
	}
	if first < '1' || first > '9' {
		if err := r.UnreadByte(); err != nil {
			return "", err
		}
		line, err := r.ReadString('\n')
		return strings.TrimRight(line, "\r\n"), err
	}

	digits := []byte{first}
	for {
		b, err := r.ReadByte()
		if err != nil {
			return "", err
		}
		if b == ' ' {
			break
		}
		if b < '0' || b > '9' {
			// Not octet counting after all: fall back to reading the rest of
			// the line and returning what we have.
			rest, err := r.ReadString('\n')
			return strings.TrimRight(string(digits)+string(b)+rest, "\r\n"), err
		}
		digits = append(digits, b)
		if len(digits) > 10 {
			return "", errors.New("syslog frame length is implausibly long")
		}
	}
	n, err := strconv.Atoi(string(digits))
	if err != nil {
		return "", err
	}
	if n > MaxLogLineBytes*4 {
		n = MaxLogLineBytes * 4
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}
